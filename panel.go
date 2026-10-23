package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// 企业微信工作台 H5 快捷页（/pt-panel?k=密钥）调用的接口。
// 页面本身公开，数据与操作都靠 ?key= 访问密钥鉴权（面板「通知设置 → 快捷指令页」生成）。

// panelTokenTTL 是快捷页令牌的有效期。企微里点开菜单通常只停留几分钟，
// 2 小时足够覆盖「看状态 → 重试 → 等结果」。
const panelTokenTTL = 2 * time.Hour

// panelTokenStore 保存短期令牌。
//
// 为什么不再让 ?k=<密钥> 直接调用数据接口：URL 会进反代 access_log、浏览器
// 历史、企微菜单配置和截图，密钥一旦随每次请求出现，等于长期半公开。现在的
// 流程是「页面带 ?k= 打开一次 → 立刻换取 2 小时令牌 → 页面用 history.replaceState
// 把 k 从地址栏抹掉 → 之后所有接口只认 X-Panel-Token」。
type panelTokenStore struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func newPanelTokenStore() *panelTokenStore { return &panelTokenStore{m: map[string]time.Time{}} }

// Issue 生成新令牌。返回错误表示随机源不可用。
func (p *panelTokenStore) Issue() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, exp := range p.m {
		if now.After(exp) {
			delete(p.m, k)
		}
	}
	// 上限兜底：正常用不到，被刷时整表清空，反正令牌可以重新换。
	if len(p.m) >= 512 {
		p.m = map[string]time.Time{}
	}
	p.m[tok] = now.Add(panelTokenTTL)
	return tok, nil
}

func (p *panelTokenStore) Valid(tok string) bool {
	if tok == "" {
		return false
	}
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	exp, ok := p.m[tok]
	if !ok {
		return false
	}
	if now.After(exp) {
		delete(p.m, tok)
		return false
	}
	return true
}

// panelKeyMatches 校验提交的密钥是否与配置一致（未配置则一律拒绝）。
func (s *Server) panelKeyMatches(key string) bool {
	ns := s.notify.Current()
	if ns.PanelKey == "" || key == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(ns.PanelKey), []byte(key)) == 1
}

// panelAuthOK 校验 X-Panel-Token 请求头。
func (s *Server) panelAuthOK(r *http.Request) bool {
	return s.panelTokens.Valid(strings.TrimSpace(r.Header.Get("X-Panel-Token")))
}

// handlePanelToken 用页面 URL 里的一次性密钥换短期令牌。
// 这个接口是唯一还接受明文密钥的地方，因此带失败限速。
func (s *Server) handlePanelToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		body.Key = r.FormValue("key")
	}
	limitKeys := []string{"panel:" + clientIP(r)}
	if ok, wait := s.panelLimit.Allow(limitKeys...); !ok {
		w.Header().Set("Retry-After", fmt.Sprint(int(wait.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "尝试次数过多，请稍后再试"})
		return
	}
	if !s.panelKeyMatches(strings.TrimSpace(body.Key)) {
		s.panelLimit.Fail(limitKeys...)
		logf("快捷页密钥校验失败：来源=%s", clientIP(r))
		panelDeny(w)
		return
	}
	s.panelLimit.Reset(limitKeys...)
	tok, err := s.panelTokens.Issue()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "令牌生成失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "expiresIn": int(panelTokenTTL.Seconds())})
}

func panelDeny(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, map[string]any{"error": "密钥无效或未配置"})
}

// handlePanelSummary 返回状态概览：运行态、总数、成功/失败、失败站列表。
func (s *Server) handlePanelSummary(w http.ResponseWriter, r *http.Request) {
	if !s.panelAuthOK(r) {
		panelDeny(w)
		return
	}
	targets := s.targets.Get()
	latest := s.history.Latest()
	norm := func(u string) string { return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(u), "/")) }
	byURL := make(map[string]Target, len(targets))
	for _, t := range targets {
		byURL[norm(t.URL)] = t
	}
	success, failed := 0, 0
	type fItem struct {
		Name   string `json:"name"`
		URL    string `json:"url"`
		Reason string `json:"reason"`
	}
	list := []fItem{}
	// 只统计当前仍存在的目标：历史里可能残留已删除目标的记录，
	// 否则失败清单会长期挂着不存在的目标，且 success+failed 与 total 对不上。
	seen := make(map[string]bool, len(targets))
	for url, rec := range latest {
		t, ok := byURL[norm(url)]
		if !ok {
			continue
		}
		seen[norm(url)] = true
		if rec.Success {
			success++
			continue
		}
		failed++
		cat, _ := classifyFailure(t, rec.Message)
		name := t.DisplayName()
		if name == "" {
			name = url
		}
		list = append(list, fItem{Name: name, URL: url, Reason: cat})
	}
	notRun := len(targets) - len(seen)
	if notRun < 0 {
		notRun = 0
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{
		"running":    s.automation.IsRunning(),
		"total":      len(targets),
		"success":    success,
		"failed":     failed,
		"notRun":     notRun,
		"failedList": list,
	})
}

// handlePanelRetryFailed 一键重试所有失败/未运行目标。
func (s *Server) handlePanelRetryFailed(w http.ResponseWriter, r *http.Request) {
	if !s.panelAuthOK(r) {
		panelDeny(w)
		return
	}
	sub := s.automation.ExecuteFailedAsync()
	switch sub.Result {
	case BatchAccepted:
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "count": len(sub.TargetURLs)})
	case BatchBusy:
		writeJSON(w, http.StatusConflict, map[string]any{"accepted": false, "message": "任务执行中，请稍候"})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"accepted": false, "message": "没有失败目标"})
	}
}

// handlePanelRetry 重试单个目标（?url= 指定）。
func (s *Server) handlePanelRetry(w http.ResponseWriter, r *http.Request) {
	if !s.panelAuthOK(r) {
		panelDeny(w)
		return
	}
	url := r.URL.Query().Get("url")
	switch s.automation.ExecuteTargetAsync(url) {
	case ExecAccepted:
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
	case ExecBusy:
		writeJSON(w, http.StatusConflict, map[string]any{"accepted": false, "message": "任务执行中，请稍候"})
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "目标不存在"})
	}
}

// handleCreateWeComMenu 调用企微 API 在工作台创建「PT 保活」快捷菜单。
func (s *Server) handleCreateWeComMenu(w http.ResponseWriter, r *http.Request) {
	ns := s.notify.Current()
	key := ns.PanelKey
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请先填「快捷指令页密钥」并保存"})
		return
	}
	host := ns.PanelHost
	if host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请先填「外网域名」并保存"})
		return
	}
	if !strings.HasPrefix(host, "http") {
		host = "https://" + host
	}
	statusURL := host + "/pt-panel?k=" + url.QueryEscape(key)
	retryURL := statusURL + "&action=retryall"
	wecom := NewWeCom(ns.toConfig())
	if err := wecom.CreateQuickMenu(statusURL, retryURL); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "已创建，刷新企业微信工作台即可见「PT 保活」菜单"})
}
