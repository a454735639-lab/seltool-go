package main

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

//go:embed web/*
var webFS embed.FS

const (
	sessionCookie = "SELTOOL_SESSION"
	csrfHeader    = "X-CSRF-TOKEN"
)

type Server struct {
	cfg        *Config
	accounts   *AccountStore
	sessions   *SessionStore
	targets    *TargetStore
	history    *History
	templates  *TemplateStore
	automation *Automation
	alerts     *AlertState
	settings   *SettingsStore
	cookies    *CookieCloud
	notify     *NotifyStore
	webhook    *Webhook
	schedule   *ScheduleStore
	scheduler  *Scheduler
	ptSites    *PTSiteTable
	ptIcons    *PTIconTable
	mpSync     *MoviePilotSyncStore
	autoImport *AutoImportStore
	// loginLimit 登录失败限速（IP 硬锁 + 用户名递增延迟）。
	loginLimit *loginLimiter
	// loginSlots 限制并发登录尝试数：用户名维度的递增延迟是在 handler 里
	// time.Sleep 出来的，不限并发就等于给了攻击者一个堆 goroutine 的开关。
	loginSlots chan struct{}
	// wecomReplay 企微回调防重放（时间戳窗口 + MsgId 去重）。
	wecomReplay *replayGuard
	// setup 首次注册的一次性令牌，防止公网抢注管理员。
	setup *setupGuard
}

func NewServer(cfg *Config, accounts *AccountStore, sessions *SessionStore, targets *TargetStore,
	history *History, templates *TemplateStore, automation *Automation, alerts *AlertState,
	settings *SettingsStore, cookies *CookieCloud, notify *NotifyStore, webhook *Webhook,
	schedule *ScheduleStore, scheduler *Scheduler, ptSites *PTSiteTable, ptIcons *PTIconTable,
	mpSync *MoviePilotSyncStore, autoImport *AutoImportStore) *Server {
	return &Server{
		cfg: cfg, accounts: accounts, sessions: sessions, targets: targets,
		history: history, templates: templates, automation: automation, alerts: alerts,
		settings: settings, cookies: cookies, notify: notify, webhook: webhook,
		schedule: schedule, scheduler: scheduler, ptSites: ptSites, ptIcons: ptIcons, mpSync: mpSync,
		autoImport: autoImport, loginLimit: newLoginLimiter(), wecomReplay: newReplayGuard(),
		loginSlots: make(chan struct{}, maxConcurrentLogins),
		setup:      newSetupGuard(filepath.Dir(cfg.Automation.UserAccountFile), accounts.HasUser()),
	}
}

// tryAcquireLoginSlot 取一个并发登录名额；满员返回 ok=false。
// release 始终非 nil，调用方直接 defer 即可。
func (s *Server) tryAcquireLoginSlot() (release func(), ok bool) {
	if s.loginSlots == nil {
		return func() {}, true
	}
	select {
	case s.loginSlots <- struct{}{}:
		return func() { <-s.loginSlots }, true
	default:
		return nil, false
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// 静态与认证页
	static, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /auth.css", http.FileServer(http.FS(static)))
	mux.HandleFunc("GET /login.html", func(w http.ResponseWriter, r *http.Request) {
		serveEmbedded(w, r, static, "login.html")
	})
	mux.HandleFunc("GET /register.html", func(w http.ResponseWriter, r *http.Request) {
		serveEmbedded(w, r, static, "register.html")
	})
	// 原版 Spring 把静态目录直接暴露，/index.html 与 / 等价；这里保持一致。
	indexHandler := func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.currentSession(r); !ok {
			http.Redirect(w, r, "/login.html", http.StatusFound)
			return
		}
		serveEmbedded(w, r, static, "index.html")
	}
	mux.HandleFunc("GET /{$}", indexHandler)
	mux.HandleFunc("GET /index.html", indexHandler)

	// 容器健康检查用：不涉及任何数据，也不需要登录。
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	})

	// 浏览器会无条件请求 favicon，给个空响应避免 404 噪音
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// 版本信息（公开，仅返回版本号字符串）
	mux.HandleFunc("GET /api/version", s.handleVersion)

	// 认证
	mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("POST /api/auth/register", s.handleRegister)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.requireAuth(s.requireCSRF(s.handleLogout)))

	// 自动化
	mux.HandleFunc("GET /api/automation/targets", s.requireAuth(s.handleGetTargets))
	mux.HandleFunc("PUT /api/automation/targets", s.requireAuth(s.requireCSRF(s.handleSaveTargets)))
	mux.HandleFunc("GET /api/automation/target-templates", s.requireAuth(s.handleTemplates))
	mux.HandleFunc("GET /api/automation/targets/history", s.requireAuth(s.handleHistory))
	mux.HandleFunc("GET /api/automation/targets/history/latest", s.requireAuth(s.handleLatestHistory))
	mux.HandleFunc("GET /api/automation/status", s.requireAuth(s.handleStatus))
	mux.HandleFunc("POST /api/automation/run", s.requireAuth(s.requireCSRF(s.handleRun)))
	mux.HandleFunc("POST /api/automation/targets/retry", s.requireAuth(s.requireCSRF(s.handleRetry)))
	mux.HandleFunc("POST /api/automation/targets/retry-failed", s.requireAuth(s.requireCSRF(s.handleRetryFailed)))

	// CookieCloud 设置（Web 面板可改，保存后立即生效）
	mux.HandleFunc("GET /api/settings/cookiecloud", s.requireAuth(s.handleGetCookieCloud))
	mux.HandleFunc("PUT /api/settings/cookiecloud", s.requireAuth(s.requireCSRF(s.handleSaveCookieCloud)))
	mux.HandleFunc("POST /api/settings/cookiecloud/test", s.requireAuth(s.requireCSRF(s.handleTestCookieCloud)))
	mux.HandleFunc("POST /api/settings/cookiecloud/sync", s.requireAuth(s.requireCSRF(s.handleSyncCookieCloud)))
	mux.HandleFunc("GET /api/settings/cookiecloud/sync-status", s.requireAuth(s.handleSyncStatus))

	// 通知设置（企业微信）
	mux.HandleFunc("GET /api/settings/notify", s.requireAuth(s.handleGetNotify))
	mux.HandleFunc("PUT /api/settings/notify", s.requireAuth(s.requireCSRF(s.handleSaveNotify)))
	mux.HandleFunc("POST /api/settings/notify/test", s.requireAuth(s.requireCSRF(s.handleTestNotify)))
	// 企业微信「接收消息」回调（企微服务器调用，公开、按白名单鉴权）
	mux.HandleFunc("GET /wecom/callback", s.handleWeComCallback)
	mux.HandleFunc("POST /wecom/callback", s.handleWeComCallback)
	mux.HandleFunc("POST /api/settings/wecom/quick-menu", s.requireAuth(s.requireCSRF(s.handleCreateWeComMenu)))
	mux.HandleFunc("GET /api/settings/schedule", s.requireAuth(s.handleGetSchedule))
	mux.HandleFunc("PUT /api/settings/schedule", s.requireAuth(s.requireCSRF(s.handleSaveSchedule)))
	mux.HandleFunc("GET /api/settings/schedule/presets", s.requireAuth(s.handleSchedulePresets))
	mux.HandleFunc("GET /api/settings/selfcheck", s.requireAuth(s.handleSelfCheck))
	mux.HandleFunc("GET /api/settings/pt-icons", s.requireAuth(s.handlePTIcons))
	mux.HandleFunc("PUT /api/settings/pt-icons", s.requireAuth(s.requireCSRF(s.handleReplacePTIcons)))
	mux.HandleFunc("POST /api/settings/pt-icons/reset", s.requireAuth(s.requireCSRF(s.handleResetPTIcons)))
	mux.HandleFunc("GET /api/settings/pt-sites", s.requireAuth(s.handleGetPTSites))
	mux.HandleFunc("PUT /api/settings/pt-sites", s.requireAuth(s.requireCSRF(s.handleSavePTSites)))
	mux.HandleFunc("POST /api/settings/pt-sites/reset", s.requireAuth(s.requireCSRF(s.handleResetPTSites)))
	mux.HandleFunc("GET /api/settings/moviepilot", s.requireAuth(s.handleGetMoviePilot))
	mux.HandleFunc("PUT /api/settings/moviepilot", s.requireAuth(s.requireCSRF(s.handleSaveMoviePilot)))
	mux.HandleFunc("POST /api/settings/moviepilot/sync", s.requireAuth(s.requireCSRF(s.handleSyncMoviePilot)))
	mux.HandleFunc("GET /api/settings/auto-import", s.requireAuth(s.handleGetAutoImport))
	mux.HandleFunc("PUT /api/settings/auto-import", s.requireAuth(s.requireCSRF(s.handleSaveAutoImport)))

	// 目标批量导入
	mux.HandleFunc("GET /api/automation/targets/candidates", s.requireAuth(s.handleCandidates))
	mux.HandleFunc("POST /api/automation/targets/import", s.requireAuth(s.requireCSRF(s.handleImportTargets)))

	return withSecurityHeaders(mux)
}

func serveEmbedded(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		// 纵深防御：前端目前全走同源静态资源，图标是 data: URI。
		// 万一还有未覆盖的注入点，CSP 能挡住外连与内联脚本执行。
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// ---- 会话 ----

func (s *Server) currentSession(r *http.Request) (*Session, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, false
	}
	sess, ok := s.sessions.Get(cookie.Value)
	if !ok || sess.Username == "" {
		return nil, false
	}
	return sess, true
}

// ensureSession 为匿名访问者建立带 CSRF token 的会话，登录/注册都需要它。
func (s *Server) ensureSession(w http.ResponseWriter, r *http.Request) (*Session, string) {
	cookie, err := r.Cookie(sessionCookie)
	if err == nil {
		if sess, ok := s.sessions.Get(cookie.Value); ok {
			return sess, cookie.Value
		}
	}
	id, sess := s.sessions.Create("")
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(r),
		Expires:  time.Now().Add(30 * 24 * time.Hour),
	})
	return sess, id
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.currentSession(r); !ok {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "未登录"})
				return
			}
			http.Redirect(w, r, "/login.html", http.StatusFound)
			return
		}
		next(w, r)
	}
}

func (s *Server) requireCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.sessionForCSRF(r)
		if !ok {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "会话已失效，请重新登录"})
			return
		}
		got := r.Header.Get(csrfHeader)
		if subtle.ConstantTimeCompare([]byte(got), []byte(sess.CSRF)) != 1 {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "CSRF 校验失败，请刷新页面重试"})
			return
		}
		next(w, r)
	}
}

func (s *Server) sessionForCSRF(r *http.Request) (*Session, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, false
	}
	return s.sessions.Get(cookie.Value)
}

// ---- 认证接口 ----

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.ensureSession(w, r)
	authenticated := sess.Username != ""
	username := ""
	if authenticated {
		username = sess.Username
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"registered":     s.accounts.HasUser(),
		"authenticated":  authenticated,
		"username":       username,
		"csrfToken":      sess.CSRF,
		"csrfHeaderName": csrfHeader,
	})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionForCSRF(r)
	if !ok || subtle.ConstantTimeCompare([]byte(r.Header.Get(csrfHeader)), []byte(sess.CSRF)) != 1 {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "CSRF 校验失败，请刷新页面重试"})
		return
	}
	var body struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		SetupToken string `json:"setupToken"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式错误"})
		return
	}
	// 首启抢注防护：还没有用户时必须提供一次性初始化令牌。
	if !s.accounts.HasUser() {
		if !s.setup.Verify(body.SetupToken) {
			logf("首次注册被拒绝：初始化令牌缺失或不匹配（来源=%s）", clientIP(r))
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": "初始化令牌不正确。首次部署需填写容器日志或 config/setup-token.txt 中的令牌",
			})
			return
		}
	}
	err := s.accounts.Register(body.Username, body.Password)
	switch {
	case err == nil:
		s.setup.Consume()
		writeJSON(w, http.StatusCreated, map[string]any{"registered": true, "message": "注册成功，请登录"})
	case errors.Is(err, errUserExists):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionForCSRF(r)
	if !ok || subtle.ConstantTimeCompare([]byte(r.Header.Get(csrfHeader)), []byte(sess.CSRF)) != 1 {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "CSRF 校验失败，请刷新页面重试"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式错误"})
		return
	}
	username := strings.TrimSpace(r.PostFormValue("username"))
	password := r.PostFormValue("password")

	// 限速分两个维度：
	//   IP 维度硬锁（攻击者自己的地址，锁了不牵连别人）；
	//   用户名维度只递增延迟，绝不拒绝 —— 否则知道用户名就能把管理员锁死。
	ip := clientIP(r)
	if ok, wait := s.loginLimit.AllowIP(ip); !ok {
		minutes := int(wait.Minutes()) + 1
		logf("登录被限速：来源=%s 用户名=%q 剩余=%s", ip, username, wait.Round(time.Second))
		w.Header().Set("Retry-After", fmt.Sprint(int(wait.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error": fmt.Sprintf("该网络尝试次数过多，请在 %d 分钟后重试", minutes),
		})
		return
	}
	// 递增延迟是「占着连接睡觉」，必须限量：否则慢速撞库可以把并发登录堆到
	// 任意数量，每个都占一个 goroutine 加一次 bcrypt。
	releaseSlot, ok := s.tryAcquireLoginSlot()
	if !ok {
		w.Header().Set("Retry-After", "5")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "登录请求过于频繁，请稍后重试"})
		return
	}
	defer releaseSlot()
	if d := s.loginLimit.Penalty(username); d > 0 {
		logf("登录递增延迟：用户名=%q 延迟=%s", username, d)
		time.Sleep(d)
	}

	if !s.accounts.Authenticate(username, password) {
		s.loginLimit.Fail(ip, username)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "用户名或密码错误"})
		return
	}
	s.loginLimit.Reset(ip, username)

	// 登录成功换发新会话，避免会话固定攻击。
	oldCookie, _ := r.Cookie(sessionCookie)
	if oldCookie != nil {
		s.sessions.Delete(oldCookie.Value)
	}
	id, newSess := s.sessions.Create(username)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(r),
		Expires:  time.Now().Add(30 * 24 * time.Hour),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"username":      newSess.Username,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.Delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
	})
	writeJSON(w, http.StatusOK, map[string]any{"loggedOut": true})
}

// ---- 自动化接口 ----

func (s *Server) handleGetTargets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.targets.Get())
}

func (s *Server) handleSaveTargets(w http.ResponseWriter, r *http.Request) {
	var targets []Target
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&targets); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式错误"})
		return
	}
	saved, err := s.targets.Save(targets)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	// 目标保存成功后清理已删除目标的历史记录，避免文件无限增长。
	keep := make(map[string]bool, len(saved))
	for _, t := range saved {
		keep[strings.TrimSpace(t.URL)] = true
	}
	if n := s.history.Prune(keep); n > 0 {
		logf("已清理 %d 条已删除目标的历史记录", n)
	}

	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.templates.All())
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	url := r.URL.Query().Get("url")
	writeJSON(w, http.StatusOK, s.history.Get(url))
}

func (s *Server) handleLatestHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.history.Latest())
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"running": s.automation.IsRunning()})
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if !s.automation.ExecuteAsync("立即执行") {
		writeJSON(w, http.StatusConflict, map[string]any{"accepted": false, "message": "任务仍在执行中，请稍候再试"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "message": "任务已提交"})
}

func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	targetURL := r.URL.Query().Get("url")
	switch s.automation.ExecuteTargetAsync(targetURL) {
	case ExecAccepted:
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "message": "目标重试已提交"})
	case ExecBusy:
		writeJSON(w, http.StatusConflict, map[string]any{"accepted": false, "message": "任务仍在执行中，请稍候再试"})
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"accepted": false, "message": "目标不存在，请先保存配置"})
	}
}

func (s *Server) handleRetryFailed(w http.ResponseWriter, r *http.Request) {
	sub := s.automation.ExecuteFailedAsync()
	switch sub.Result {
	case BatchAccepted:
		writeJSON(w, http.StatusAccepted, map[string]any{
			"accepted":   true,
			"message":    "未运行或失败目标的重试任务已提交",
			"targetUrls": sub.TargetURLs,
		})
	case BatchBusy:
		writeJSON(w, http.StatusConflict, map[string]any{"accepted": false, "message": "任务仍在执行中，请稍候再试"})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"accepted": false, "message": "没有需要重试的目标"})
	}
}

// ---- CookieCloud 设置 ----

func (s *Server) handleGetCookieCloud(w http.ResponseWriter, r *http.Request) {
	cfg := s.cookies.Config()
	writeJSON(w, http.StatusOK, map[string]any{
		"url": cfg.URL,
		// KEY 原文不回显：它是能直接拉走全部 PT 登录态的凭据，
		// 一旦被 XSS 或肩窥读走，等于把所有站的 cookie 交出去。
		"hasKey":      strings.TrimSpace(cfg.Key) != "",
		"hasPassword": strings.TrimSpace(cfg.Password) != "",
		// 提示前端：地址要填到 API 根，扩展里用的就是这一串
		"hint": "形如 https://你的域名/apps/cookiecloud",
	})
}

func (s *Server) handleSaveCookieCloud(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL      string `json:"url"`
		Key      string `json:"key"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式错误"})
		return
	}

	// 前端不回显 KEY/密码，留空表示沿用当前生效值（可能来自 yaml/env，而不只是设置文件）
	password := body.Password
	if strings.TrimSpace(password) == "" {
		password = s.cookies.Config().Password
	}
	key := strings.TrimSpace(body.Key)
	if key == "" {
		key = s.cookies.Config().Key
	}

	saved, err := s.settings.Save(CookieCloudSettings{
		URL:      body.URL,
		Key:      key,
		Password: password,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	cfg := s.cookies.Config()
	cfg.URL = saved.URL
	cfg.Key = saved.Key
	cfg.Password = saved.Password
	s.cookies.Update(cfg)

	writeJSON(w, http.StatusOK, map[string]any{
		"url":         saved.URL,
		"hasKey":      strings.TrimSpace(saved.Key) != "",
		"hasPassword": saved.Password != "",
		"message":     "已保存并生效",
	})
}

// handleCandidates 预览从 CookieCloud 识别到的 PT 站，不落盘。
func (s *Server) handleCandidates(w http.ResponseWriter, r *http.Request) {
	data, lsData, err := s.cookies.FetchAll()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "CookieCloud 获取失败：" + err.Error()})
		return
	}
	domains := detectPTSitesWithStorage(data, lsData)

	// 标出哪些已经在目标列表里：全都在时不该只显示「未识别到 PT 站」，
	// 那会让人以为识别功能坏了。
	seenReg := map[string]bool{}
	if s.targets != nil {
		for _, t := range s.targets.Get() {
			if reg := registeredDomain(t.URL); reg != "" {
				seenReg[reg] = true
			}
		}
	}
	type candidate struct {
		Domain string `json:"domain"`
		Name   string `json:"name"`
		Exists bool   `json:"exists"`
		Source string `json:"source"` // cookie / localStorage / both
	}
	// 统计每个归一化域名带哪些凭据，前端可据此提示「该站靠 localStorage 登录」
	cookieDom := map[string]bool{}
	for raw, list := range data {
		if len(list) > 0 {
			cookieDom[normalizeDomain(raw)] = true
		}
	}
	lsDom := map[string]bool{}
	for raw, list := range lsData {
		if len(list) > 0 {
			lsDom[normalizeDomain(raw)] = true
		}
	}
	list := make([]candidate, 0, len(domains))
	fresh := 0
	for _, d := range domains {
		host := normalizeDomain(d)
		exists := seenReg[registeredDomain(host)]
		if !exists {
			fresh++
		}
		source := ""
		switch {
		case cookieDom[host] && lsDom[host]:
			source = "both"
		case lsDom[host]:
			source = "localStorage"
		case cookieDom[host]:
			source = "cookie"
		}
		list = append(list, candidate{Domain: host, Name: siteDisplayName(host), Exists: exists, Source: source})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"candidates": list,
		"total":      len(list),
		"fresh":      fresh,
	})
}

// handleImportTargets 支持两种来源：mode=auto 从 CookieCloud 识别，mode=manual 解析粘贴文本。
func (s *Server) handleImportTargets(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode      string   `json:"mode"`
		Input     string   `json:"input"`
		Domains   []string `json:"domains"`
		Overwrite bool     `json:"overwrite"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式错误"})
		return
	}

	var incoming []Target
	switch body.Mode {
	case "auto":
		if len(body.Domains) > 0 {
			incoming = targetsFromDomains(body.Domains)
			break
		}
		data, lsData, err := s.cookies.FetchAll()
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": "CookieCloud 获取失败：" + err.Error()})
			return
		}
		domains := detectPTSitesWithStorage(data, lsData)
		if len(domains) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"imported": 0, "skipped": 0, "names": []string{},
				"message": "没有识别到 PT 站，请确认 CookieCloud 里有数据"})
			return
		}
		incoming, _ = BuildImportTargets(domains, s.targets.Get(), data)
	case "manual":
		incoming = ParseManualTargets(body.Input)
		if len(incoming) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "没有解析到有效地址"})
			return
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "mode 必须是 auto 或 manual"})
		return
	}

	// 读-改-写必须原子：否则与定时自动导入并发时，后写者会覆盖前者。
	var added []Target
	var updated, skipped int
	saved, err := s.targets.Update(func(existing []Target) ([]Target, error) {
		a, u, sk := mergeTargetsOverwrite(incoming, existing, body.Overwrite)
		added, updated, skipped = a, u, sk
		return append(existing, a...), nil
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	names := make([]string, 0, len(added))
	for _, t := range added {
		names = append(names, t.Name)
	}
	msg := fmt.Sprintf("已导入 %d 个目标，跳过 %d 个已存在的", len(added), skipped)
	if updated > 0 {
		msg = fmt.Sprintf("已导入 %d 个，更新 %d 个，跳过 %d 个", len(added), updated, skipped)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"imported": len(added),
		"updated":  updated,
		"skipped":  skipped,
		"total":    len(saved),
		"names":    names,
		"message":  msg,
	})
}

// mergeTargets 按规范化 URL 合并，跳过已存在的站点。
//
// 去重键用 canonicalURLKey（host + 路径，忽略末尾斜杠），并额外按 host 精确匹配，
// 这样「同一站带不带末尾斜杠」「导入两次」都不会产生重复。
// 注意 www 与 apex 仍视为不同 host —— 两者 Cookie 域不同，合并会丢登录态。

// targetsFromDomains 把域名列表直接转成目标（不做去重，交给合并阶段处理）。
func targetsFromDomains(domains []string) []Target {
	out := make([]Target, 0, len(domains))
	for _, d := range domains {
		host := normalizeDomain(d)
		if host == "" || registeredDomain(host) == "" {
			continue
		}
		out = append(out, Target{
			Name:         siteDisplayName(host),
			URL:          "https://" + host + "/",
			WarmupPath:   "/favicon.ico",
			LocalStorage: []LocalStorage{},
		})
	}
	return out
}

// mergeTargetsOverwrite 在 mergeTargets 基础上支持「重复时覆盖」。
// 覆盖只更新显示名，不改 URL —— 已有目标可能带签到路径（如 /attendance.php），
// 改 URL 会把用户配好的路径冲掉。
func mergeTargetsOverwrite(incoming, existing []Target, overwrite bool) (added []Target, updated, skipped int) {
	seenKey := map[string]bool{}
	regIdx := map[string]int{}
	hostIdx := map[string]int{}
	// addedIdx 记录本轮新增项在 added 里的下标。
	// 不能复用 hostIdx：它的值是 existing 的下标，拿它去索引一个还没落库的
	// 新目标会直接越界 panic（同域不同路径的第二次导入就会触发）。
	addedIdx := map[string]int{}
	for i, t := range existing {
		raw := strings.TrimSpace(t.URL)
		if raw == "" {
			continue
		}
		seenKey[canonicalURLKey(raw)] = true
		if reg := registeredDomain(raw); reg != "" {
			if _, ok := regIdx[reg]; !ok {
				regIdx[reg] = i
			}
		}
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			hostIdx[strings.ToLower(u.Hostname())] = i
		}
	}
	apply := func(i int, t Target) bool {
		if !overwrite {
			return false
		}
		if n := strings.TrimSpace(t.Name); n != "" && existing[i].Name != n {
			existing[i].Name = n
			return true
		}
		return false
	}
	applyAdded := func(i int, t Target) bool {
		if !overwrite {
			return false
		}
		if n := strings.TrimSpace(t.Name); n != "" && added[i].Name != n {
			added[i].Name = n
			return true
		}
		return false
	}
	for _, t := range incoming {
		raw := strings.TrimSpace(t.URL)
		if raw == "" {
			skipped++
			continue
		}
		if seenKey[canonicalURLKey(raw)] {
			if i, ok := regIdx[registeredDomain(raw)]; ok && apply(i, t) {
				updated++
			} else {
				skipped++
			}
			continue
		}
		host := ""
		if u, err := url.Parse(raw); err == nil {
			host = strings.ToLower(u.Hostname())
		}
		if i, ok := hostIdx[host]; ok && host != "" {
			if apply(i, t) {
				updated++
			} else {
				skipped++
			}
			continue
		}
		// 同主机在本轮里已经新增过：按「重复」处理，改名也只作用于待新增的那条。
		if i, ok := addedIdx[host]; ok && host != "" {
			if applyAdded(i, t) {
				updated++
			} else {
				skipped++
			}
			continue
		}
		if reg := registeredDomain(raw); reg != "" {
			if i, ok := regIdx[reg]; ok {
				if apply(i, t) {
					updated++
				} else {
					skipped++
				}
				continue
			}
		}
		seenKey[canonicalURLKey(raw)] = true
		if host != "" {
			addedIdx[host] = len(added)
		}
		added = append(added, t)
	}
	return added, updated, skipped
}

func mergeTargets(incoming, existing []Target) (added []Target, skipped int) {
	seenKey := map[string]bool{}
	seenHost := map[string]bool{}
	for _, t := range existing {
		raw := strings.TrimSpace(t.URL)
		if raw == "" {
			continue
		}
		seenKey[canonicalURLKey(raw)] = true
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			seenHost[strings.ToLower(u.Hostname())] = true
		}
	}
	for _, t := range incoming {
		raw := strings.TrimSpace(t.URL)
		if raw == "" {
			skipped++
			continue
		}
		key := canonicalURLKey(raw)
		if seenKey[key] {
			skipped++
			continue
		}
		host := ""
		if u, err := url.Parse(raw); err == nil {
			host = strings.ToLower(u.Hostname())
		}
		if host != "" && seenHost[host] {
			skipped++
			continue
		}
		seenKey[key] = true
		if host != "" {
			seenHost[host] = true
		}
		added = append(added, t)
	}
	return added, skipped
}

func (s *Server) handleTestCookieCloud(w http.ResponseWriter, r *http.Request) {
	var draft struct {
		URL      *string `json:"url"`
		Key      *string `json:"key"`
		Password *string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&draft); err != nil && err != io.EOF {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "请求格式错误"})
		return
	}
	cfg := s.cookies.Config()
	if draft.URL != nil {
		cfg.URL = strings.TrimSpace(*draft.URL)
	}
	if draft.Key != nil {
		cfg.Key = strings.TrimSpace(*draft.Key)
	}
	if draft.Password != nil && *draft.Password != "" {
		cfg.Password = *draft.Password
	}
	// A fresh client cannot read or mutate active settings/cache during a probe.
	probe := NewCookieCloud(cfg, NewAlertState())
	domains, cookies, reachable, err := probe.Probe()
	if err != nil || !reachable {
		detail := "无法连接到服务器"
		if err != nil {
			detail = err.Error()
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok":    false,
			"error": "连接失败：" + detail,
		})
		return
	}
	msg := "连接成功"
	if cookies == 0 {
		msg = "已连接，但该 KEY 下没有数据 —— 请先在浏览器扩展里点一次「同步」"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"domains": domains,
		"cookies": cookies,
		"message": msg,
	})
}

// ---- 通知设置 ----

func (s *Server) handleGetNotify(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.notify.Current().mask())
}

func (s *Server) handleSaveNotify(w http.ResponseWriter, r *http.Request) {
	var body NotifySettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式错误"})
		return
	}
	if body.Mode == "bot" && strings.TrimSpace(body.WebhookURL) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "群机器人模式必须填写 Webhook 地址"})
		return
	}
	// 应用模式必须三项齐全才发得出去：WeCom.Enabled() 要求 corpid + corpsecret + agentid。
	// 旧实现漏了 corpsecret，于是「没填 Secret」的实例也提示保存成功，而实际发送时
	// 因为 Enabled()==false 被静默跳过 —— 日志里连一行都没有。
	// 面板不回显 Secret，留空表示沿用已保存的值，所以要和当前值合并后再判。
	if body.Mode == "app" {
		cur := s.notify.Current()
		secret := strings.TrimSpace(body.CorpSecret)
		if secret == "" {
			secret = strings.TrimSpace(cur.CorpSecret)
		}
		switch {
		case strings.TrimSpace(body.CorpID) == "":
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "应用模式必须填写企业ID（corpid）"})
			return
		case body.AgentID <= 0:
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "应用模式必须填写 AgentId"})
			return
		case secret == "":
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "应用模式必须填写应用 Secret（corpsecret），否则消息发不出去"})
			return
		}
	}
	saved, err := s.notify.Save(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	s.webhook.SetWeCom(saved.toConfig())
	if s.cfg != nil {
		s.cfg.Automation.Notify.OnlyOnFailure = saved.OnlyOnFailure
	}
	out := saved.mask()
	out["message"] = "已保存并生效"
	if !NewWeCom(saved.toConfig()).Enabled() {
		// 兜底：任何模式下没配齐都如实告诉用户，别让「已保存并生效」变成空话。
		out["message"] = "已保存，但通知尚未启用：当前模式的必填项不完整"
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTestNotify 用当前表单里的配置发一条测试消息，不落盘。
func (s *Server) handleTestNotify(w http.ResponseWriter, r *http.Request) {
	var body NotifySettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil && err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式错误"})
		return
	}
	// 未填的项沿用已保存值（密码不回显）
	cur := s.notify.Current()
	if body.CorpSecret == "" {
		body.CorpSecret = cur.CorpSecret
	}
	if body.Mode == "" {
		body.Mode = cur.Mode
	}
	if body.WebhookURL == "" {
		body.WebhookURL = cur.WebhookURL
	}
	if body.CorpID == "" {
		body.CorpID = cur.CorpID
	}
	if body.AgentID == 0 {
		body.AgentID = cur.AgentID
	}
	if body.ToUser == "" {
		body.ToUser = cur.ToUser
	}
	if body.Proxy == "" {
		body.Proxy = cur.Proxy
	}

	wc := NewWeCom(body.toConfig())
	if !wc.Enabled() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "配置不完整，无法测试"})
		return
	}
	// 用独立实例发测试，避免污染正式 token 缓存
	if err := wc.sendTest(); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "测试消息已发送，请查看企业微信"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// ---- 定时任务设置 ----

func (s *Server) handleGetSchedule(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}
	if s.schedule != nil {
		cur := s.schedule.Current()
		out["enabled"] = cur.Enabled
		out["cron"] = cur.Cron
		out["zone"] = cur.Zone
	}
	if s.scheduler != nil {
		snap := s.scheduler.Snapshot()
		out["enabled"] = snap.Enabled
		out["cron"] = snap.Cron
		out["zone"] = snap.Zone
		out["describe"] = snap.Describe
		out["nextRun"] = snap.NextRun
		if e := s.scheduler.LastError(); e != "" {
			out["lastError"] = e
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSchedulePresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"presets": schedulePresets(),
		"zones":   []string{"Asia/Shanghai", "Asia/Tokyo", "UTC", "America/New_York", "Europe/London"},
	})
}

func (s *Server) handleSaveSchedule(w http.ResponseWriter, r *http.Request) {
	var body ScheduleSettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式错误"})
		return
	}
	if s.schedule == nil || s.scheduler == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "定时任务未启用（启动时带了 -no-schedule）"})
		return
	}

	// 先应用再落盘：调度起不来就不该写进配置文件
	if err := s.scheduler.Apply(body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	saved, err := s.schedule.Save(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	snap := s.scheduler.Snapshot()
	out := map[string]any{
		"enabled":  snap.Enabled,
		"cron":     snap.Cron,
		"zone":     snap.Zone,
		"describe": snap.Describe,
		"nextRun":  snap.NextRun,
		"message":  "已保存并立即生效",
	}
	if !saved.Enabled {
		out["message"] = "已保存，定时任务已停用"
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- 站点中文名表 ----

func (s *Server) handleGetPTSites(w http.ResponseWriter, r *http.Request) {
	if s.ptSites == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "站点表未初始化"})
		return
	}
	version, source, count := s.ptSites.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"version": version,
		"source":  source,
		"count":   count,
		"sites":   s.ptSites.All(),
	})
}

func (s *Server) handleSavePTSites(w http.ResponseWriter, r *http.Request) {
	if s.ptSites == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "站点表未初始化"})
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "读取请求体失败"})
		return
	}
	version, count, err := s.ptSites.Replace(raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "站点表已更新",
		"version": version,
		"count":   count,
	})
}

func (s *Server) handleResetPTSites(w http.ResponseWriter, r *http.Request) {
	if s.ptSites == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "站点表未初始化"})
		return
	}
	if err := s.ptSites.Reset(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	version, _, count := s.ptSites.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "已恢复内置站点表",
		"version": version,
		"count":   count,
	})
}

// ---- CookieCloud 同步 ----

// handleSyncCookieCloud 手动触发一次全量 Cookie 拉取，立即更新缓存。
func (s *Server) handleSyncCookieCloud(w http.ResponseWriter, r *http.Request) {
	if s.cookies == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "CookieCloud 未初始化"})
		return
	}
	start := time.Now()
	data, lsData, err := s.cookies.FetchAll()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok":    false,
			"error": "同步失败：" + err.Error(),
		})
		return
	}
	syncCookiesOK(data)
	total := 0
	for _, list := range data {
		total += len(list)
	}
	lsTotal := 0
	for _, list := range lsData {
		lsTotal += len(list)
	}
	msg := fmt.Sprintf("同步成功：%d 个域名 / %d 条 Cookie", len(data), total)
	if lsTotal > 0 {
		msg += fmt.Sprintf("，%d 个域名 / %d 条 localStorage", len(lsData), lsTotal)
	}

	// CookieCloud 里出现的新 PT 站，按开关自动加入目标列表
	added := []string{}
	if s.autoImport != nil && s.autoImport.Current().Enabled && s.targets != nil {
		if res, err := RunAutoImport(data, lsData, s.targets); err != nil {
			logf("自动导入失败: %v", err)
		} else if len(res.Added) > 0 {
			added = res.Added
			msg += fmt.Sprintf("，自动新增 %d 个目标", len(res.Added))
			logf("CookieCloud 自动导入 %d 个新站点: %s", len(res.Added), res.Summary())
			if s.autoImport.Current().Notify && s.webhook != nil {
				s.webhook.Send("CookieCloud 自动导入", fmt.Sprintf("从 CookieCloud 发现 %d 个新站点，已自动加入目标列表：\n%s",
					len(res.Added), res.Summary()))
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"message":  msg,
		"domains":  len(data),
		"cookies":  total,
		"elapsed":  time.Since(start).Milliseconds(),
		"imported": added,
	})
}

// handleSyncStatus 返回上次同步时间与缓存概况，供面板展示。
func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	last, domains, cookies := lastSyncInfo()
	writeJSON(w, http.StatusOK, map[string]any{
		"lastSync": last,
		"domains":  domains,
		"cookies":  cookies,
	})
}

// ---- MoviePilot 站点同步 ----

func (s *Server) handleGetMoviePilot(w http.ResponseWriter, r *http.Request) {
	if s.mpSync == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "MoviePilot 同步未初始化"})
		return
	}
	out := s.mpSync.Current().mask()
	if s.ptSites != nil {
		ver, src, cnt := s.ptSites.Snapshot()
		out["ptSitesVersion"] = ver
		out["ptSitesSource"] = src
		out["ptSitesCount"] = cnt
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSaveMoviePilot(w http.ResponseWriter, r *http.Request) {
	if s.mpSync == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "MoviePilot 同步未初始化"})
		return
	}
	var body MoviePilotSyncConfig
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式错误"})
		return
	}
	saved, err := s.mpSync.Save(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	// 自动同步任务重排
	if s.scheduler != nil {
		s.scheduler.SetMoviePilot(saved)
	}
	out := saved.mask()
	out["message"] = "已保存"
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSyncMoviePilot(w http.ResponseWriter, r *http.Request) {
	if s.mpSync == nil || s.ptSites == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "MoviePilot 同步未初始化"})
		return
	}
	// 表单里填了未保存的地址/Token 时优先用表单值（Token 留空则沿用已保存的）
	// 注意：这里必须一并处理 enabled / cron。之前只取 URL 和 Token，
	// 用户勾了「每天自动同步」再点同步按钮，开关会被静默丢弃，看着像没生效。
	body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
	var draft MoviePilotSyncConfig
	_ = json.Unmarshal(body, &draft)
	var present map[string]json.RawMessage
	_ = json.Unmarshal(body, &present)

	cur := s.mpSync.Current()
	if strings.TrimSpace(draft.URL) != "" {
		cur.URL = strings.TrimRight(strings.TrimSpace(draft.URL), "/")
	}
	if strings.TrimSpace(draft.Token) != "" {
		cur.Token = strings.TrimSpace(draft.Token)
	}
	if _, ok := present["enabled"]; ok {
		cur.Enabled = draft.Enabled
	}
	if _, ok := present["cron"]; ok && strings.TrimSpace(draft.Cron) != "" {
		cur.Cron = strings.TrimSpace(draft.Cron)
	}

	start := time.Now()
	n, err := SyncFromMoviePilot(cur, s.ptSites)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	// 同步成功后把本次生效的地址与 Token 一并落盘，避免用户以为「同步」没保存
	saved := false
	if strings.TrimSpace(cur.URL) != "" && strings.TrimSpace(cur.Token) != "" {
		if _, e := s.mpSync.Save(cur); e == nil {
			saved = true
			if s.scheduler != nil {
				s.scheduler.SetMoviePilot(cur)
			}
		}
	}

	// 同步站点名后再补图标（只补本地没有的，已有不覆盖）
	iconAdded := 0
	if ic, ierr := SyncIconsFromMoviePilot(cur, s.ptIcons); ierr != nil {
		logf("同步站点图标失败: %v", ierr)
	} else {
		iconAdded = ic
	}

	msg := fmt.Sprintf("同步成功：%d 个站点", n)
	if iconAdded > 0 {
		msg += fmt.Sprintf("，新增 %d 个图标", iconAdded)
	}
	if saved {
		msg += "，配置已保存"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": msg,
		"count":   n,
		"saved":   saved,
		"elapsed": time.Since(start).Milliseconds(),
	})
}

// ---- 自动导入开关 ----

func (s *Server) handleGetAutoImport(w http.ResponseWriter, r *http.Request) {
	if s.autoImport == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "自动导入未初始化"})
		return
	}
	c := s.autoImport.Current()
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": c.Enabled,
		"notify":  c.Notify,
	})
}

func (s *Server) handleSaveAutoImport(w http.ResponseWriter, r *http.Request) {
	if s.autoImport == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "自动导入未初始化"})
		return
	}
	var body AutoImportSettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式错误"})
		return
	}
	saved, err := s.autoImport.Save(body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	// 保存后重排每日同步任务，让新开关立即生效
	if s.scheduler != nil {
		s.scheduler.SetMoviePilot(s.mpSync.Current())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": saved.Enabled,
		"notify":  saved.Notify,
		"message": "已保存",
	})
}

// handlePTIcons 返回站点图标。
//
// 默认只返回当前目标用得到的图标（?all=1 才返回全量）：
// 图标是 base64 内嵌的，全量有 1.8MB，每次打开面板都传一遍太浪费。
func (s *Server) handlePTIcons(w http.ResponseWriter, r *http.Request) {
	if s.ptIcons == nil {
		writeJSON(w, http.StatusOK, map[string]any{"count": 0, "icons": map[string]string{}})
		return
	}
	if r.URL.Query().Get("all") == "1" {
		writeJSON(w, http.StatusOK, map[string]any{
			"count": s.ptIcons.Count(),
			"icons": s.ptIcons.All(),
			"total": s.ptIcons.Count(),
		})
		return
	}

	// 每个目标返回一个能命中的键。
	//
	// 注意不能按「图标内容」去重：多个站点可能共用同一张图，
	// 若因内容重复而跳过，该 host 就没有对应键，前端会显示不出图标。
	// 这里按 host 去重，同一 host 只出现一次；内容重复由前端缓存自然吸收。
	picked := map[string]string{}
	if s.targets != nil {
		for _, t := range s.targets.Get() {
			host := normalizeIconKey(hostOf(t.URL))
			if host == "" {
				continue
			}
			// 依次尝试：完整 host → 去 www → 注册域名，命中一个就停
			var uri string
			for _, key := range []string{host, strings.TrimPrefix(host, "www."), registeredDomain(host)} {
				if key == "" {
					continue
				}
				if uri = s.ptIcons.Lookup(key); uri != "" {
					break
				}
			}
			if uri == "" {
				continue
			}
			picked[host] = uri
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"count": len(picked),
		"icons": picked,
		"total": s.ptIcons.Count(),
	})
}

// handleReplacePTIcons 用请求体覆盖图标表并落盘。
func (s *Server) handleReplacePTIcons(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体读取失败"})
		return
	}
	if s.ptIcons == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "图标表未初始化"})
		return
	}
	n, err := s.ptIcons.Replace(raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": n, "message": "图标表已更新"})
}

// handleResetPTIcons 删除用户图标文件，回退内置。
func (s *Server) handleResetPTIcons(w http.ResponseWriter, r *http.Request) {
	if s.ptIcons == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "图标表未初始化"})
		return
	}
	n, err := s.ptIcons.Reset()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": n, "message": "已恢复内置图标表"})
}
