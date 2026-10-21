package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// ErrBrowserClosed 表示浏览器进程已退出，需要重建会话。
var ErrBrowserClosed = errors.New("浏览器已关闭")

type Browser struct {
	cfg            BrowserConfig
	allocCancel    context.CancelFunc
	browserCtx     context.Context
	browserCancel  context.CancelFunc
	profileRelease func()
}

// NewBrowser 启动一个 Chromium 实例。后续每个目标在其上开独立 tab。
func NewBrowser(cfg BrowserConfig) (*Browser, error) {
	// 不用 DefaultExecAllocatorOptions：它带 --enable-automation，
	// 会被 Cloudflare / 雷池之类的人机验证直接判定为机器人。
	opts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-background-timer-throttling", true),
		chromedp.Flag("disable-backgrounding-occluded-windows", true),
		chromedp.Flag("disable-breakpad", true),
		chromedp.Flag("disable-client-side-phishing-detection", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-hang-monitor", true),
		chromedp.Flag("disable-popup-blocking", true),
		chromedp.Flag("disable-prompt-on-repost", true),
		chromedp.Flag("disable-renderer-backgrounding", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("force-color-profile", "srgb"),
		chromedp.Flag("metrics-recording-only", true),
		chromedp.Flag("safebrowsing-disable-auto-update", true),
		chromedp.Flag("password-store", "basic"),
		chromedp.Flag("use-mock-keychain", true),
		// 隐藏自动化特征，navigator.webdriver 变成 false
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		// ── 内存控制 ──
		// V8 堆上限。PT 站页面都很轻，默认可用到 ~2GB，限到 256MB 足以跑完，
		// 且能避免单页内存膨胀（实测运行峰值可降 150MB 以上）。
		chromedp.Flag("js-flags", "--max-old-space-size=256"),
		// 每个渲染进程的内存上限（KB），超出触发回收而不是继续涨
		chromedp.Flag("renderer-process-limit", "2"),
		// 关掉一批常驻后台服务，省内存也省流量
		chromedp.Flag("disable-features", strings.Join([]string{
			"Translate",         // 翻译（我们永远不用）
			"BackForwardCache",  // 前进后退缓存（单页导航用不到）
			"AcceptCHFrame",     // 客户端提示
			"MediaRouter",       // 投屏
			"OptimizationHints", // 优化提示服务
			"CalculateNativeWinOcclusion",
			"InterestFeedContentSuggestions",
			"GlobalMediaControls", // 全局媒体控件
			"HardwareMediaKeyHandling",
		}, ",")),
		chromedp.Flag("disable-background-networking", true),
		// 不传 disable-background-timer-throttling：保留定时器，避免后台标签页被节流卡住
		chromedp.Flag("disable-component-update", true),
		chromedp.Flag("disable-domain-reliability", true),

		chromedp.Flag("no-pings", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("disable-breakpad", true),
		// 禁用 GPU 光栅化，减少显存与合成内存占用
		chromedp.Flag("disable-gpu-compositing", true),
		// 不传 disable-software-rasterizer：保留软光栅，WebGL 需要
	}

	if cfg.Headless {
		opts = append(opts, chromedp.Flag("headless", "new"))
	}
	if cfg.NoSandbox {
		opts = append(opts, chromedp.Flag("no-sandbox", true))
	}
	if cfg.DisableDevShmUsage {
		opts = append(opts, chromedp.Flag("disable-dev-shm-usage", true))
	}
	if cfg.DisableImages {
		opts = append(opts, chromedp.Flag("blink-settings", "imagesEnabled=false"))
	}
	var profileRelease func()
	if dir := strings.TrimSpace(cfg.UserDataDir); dir != "" {
		var err error
		profileRelease, err = acquireProfile(context.Background(), dir)
		if err != nil {
			return nil, fmt.Errorf("浏览器 profile 不可用: %w", err)
		}
		opts = append(opts, chromedp.UserDataDir(dir))
	}
	if strings.TrimSpace(cfg.BinaryPath) != "" {
		opts = append(opts, chromedp.ExecPath(strings.TrimSpace(cfg.BinaryPath)))
	}
	if strings.TrimSpace(cfg.Proxy) != "" {
		opts = append(opts, chromedp.ProxyServer(strings.TrimSpace(cfg.Proxy)))
	}
	for _, arg := range cfg.Arguments {
		arg = strings.TrimSpace(arg)
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if hasValue {
			opts = append(opts, chromedp.Flag(name, value))
		} else {
			opts = append(opts, chromedp.Flag(name, true))
		}
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)

	b := &Browser{
		cfg:            cfg,
		allocCancel:    allocCancel,
		browserCtx:     browserCtx,
		browserCancel:  browserCancel,
		profileRelease: profileRelease,
	}

	// 立即启动，避免首个目标承担冷启动耗时；失败要立刻暴露。
	if err := chromedp.Do(browserCtx); err != nil {
		b.Close()
		return nil, fmt.Errorf("启动浏览器失败: %w", err)
	}
	return b, nil
}

// Alive 向浏览器发一次 GetVersion 探测，只有真实响应才算活着。
func (b *Browser) Alive() bool {
	if b == nil || b.browserCtx == nil || b.browserCtx.Err() != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(b.browserCtx, 3*time.Second)
	defer cancel()
	_, err := chromedp.CallBrowser(ctx, browser.GetVersion, cdp.Empty{})
	return err == nil
}

func (b *Browser) Close() {
	if b == nil {
		return
	}
	if b.browserCancel != nil {
		b.browserCancel()
	}
	if b.allocCancel != nil {
		b.allocCancel()
	}
	if b.profileRelease != nil {
		b.profileRelease()
	}
}

// newTab 创建一个标签页并用原始 ctx 完成绑定。
//
// 必须先用不带超时的 ctx 调一次 Do：chromedp 会把 target 绑定到第一次调用的 ctx 上，
// 若那次调用用的是 context.WithTimeout 派生 ctx，函数返回时 defer cancel() 会连带
// 销毁绑定，后续所有命令都收不到响应（表现为永久挂起）。
func (b *Browser) newTab() (context.Context, context.CancelFunc, error) {
	tabCtx, tabCancel := chromedp.NewContext(b.browserCtx)
	if err := chromedp.Do(tabCtx); err != nil {
		tabCancel()
		return nil, nil, fmt.Errorf("创建标签页失败: %w", err)
	}
	patterns := resourcePatterns(b.cfg.BlockResourceSuffixes)
	if len(patterns) > 0 {
		// Chromium 155 accepts the legacy urls field; newer CDP bindings removed
		// that field, so send the stable wire command explicitly.
		err := b.do(tabCtx, 5*time.Second, chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
			if _, err := cdp.Call(ctx, t, network.Enable, network.EnableParams{}); err != nil {
				return err
			}
			type blocked struct {
				URLs []string `json:"urls"`
			}
			_, err := cdp.Call(ctx, t, cdp.Command[blocked, cdp.Empty]{Method: "Network.setBlockedURLs"}, blocked{patterns})
			return err
		}))
		if err != nil {
			tabCancel()
			return nil, nil, fmt.Errorf("资源后缀拦截配置失败: %w", err)
		}
	}
	return tabCtx, tabCancel, nil
}

// effectiveTargetURL 处理「目标写根域、会话 Cookie 只在 www 子域」的情况。
// 只有目标域本身拿不到任何 Cookie、且恰好一个子域持有 Cookie 时才改用它，
// 避免把会话作用域扩大到父域，也避免乱跳无关子域。
func effectiveTargetURL(target Target, cookies []Cookie) (string, string) {
	host := hostOf(target.URL)
	if host == "" || len(cookies) == 0 {
		return target.URL, ""
	}
	direct := false
	subs := map[string]bool{}
	for _, ck := range cookies {
		d := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ck.Domain), "."))
		switch {
		case d == "" || d == host || strings.HasSuffix(host, "."+d):
			direct = true
		case strings.HasSuffix(d, "."+host):
			subs[d] = true
		}
	}
	if direct || len(subs) != 1 {
		return target.URL, ""
	}
	var only string
	for d := range subs {
		only = d
	}
	u, err := url.Parse(target.URL)
	if err != nil {
		return target.URL, ""
	}
	u.Host = only
	return u.String(), only
}

// Visit 执行单个目标的完整流程。
//
// 拆成两个独立 tab：Chromium 在跨进程导航后会重建 CDP session，
// 同一 tab 内的第二次命令常常收不到响应（表现为永久挂起），
// 而每个 tab 的第一条命令始终正常。Cookie 与 localStorage 都属于浏览器 profile，
// 换 tab 后依然可见。
func (b *Browser) Visit(target Target, cookies []Cookie) error {
	timeout := b.cfg.PageLoadTimeout.Std()
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}

	visitURL, switched := effectiveTargetURL(target, cookies)
	if switched != "" {
		logf("目标 [%s] 的会话 Cookie 只在 %s，本轮改用该主机访问", target.DisplayName(), switched)
	}

	if err := b.warmup(target, cookies, visitURL, timeout); err != nil {
		return err
	}

	tabCtx, tabCancel, err := b.newTab()
	if err != nil {
		return b.classify(target, err)
	}
	defer tabCancel()

	start := time.Now()
	if err := b.do(tabCtx, timeout, chromedp.Navigate(visitURL)); err != nil {
		return b.classify(target, fmt.Errorf("打开目标页面失败: %w", err))
	}
	logf("目标 [%s] 页面导航耗时 %d ms", target.DisplayName(), time.Since(start).Milliseconds())

	// 诊断：确认注入的 Cookie 在页面上下文里可见
	if visible, err := runValue(tabCtx, 5*time.Second, chromedp.Evaluate[string](`document.cookie`)); err == nil {
		names := make([]string, 0, 8)
		for _, part := range strings.Split(visible, ";") {
			if k, _, ok := strings.Cut(strings.TrimSpace(part), "="); ok && k != "" {
				names = append(names, k)
			}
		}
		logf("目标 [%s] 页面可见 Cookie(%d): %v", target.DisplayName(), len(names), names)
	}

	ok, reason := b.checkStatus(tabCtx, target)
	if !ok {
		return fmt.Errorf("%s", reason)
	}

	if stay := b.cfg.PageStaySeconds; stay > 0 {
		select {
		case <-time.After(time.Duration(stay) * time.Second):
		case <-tabCtx.Done():
			return b.classify(target, fmt.Errorf("停留期间浏览器已关闭"))
		}
	}
	logf("目标 [%s] 执行完成", target.DisplayName())
	return nil
}

// warmup 在独立 tab 里打开预热页，注入 Cookie 与 localStorage，然后关掉该 tab。
func (b *Browser) warmup(target Target, cookies []Cookie, visitURL string, timeout time.Duration) error {
	tabCtx, tabCancel, err := b.newTab()
	if err != nil {
		return b.classify(target, err)
	}
	defer tabCancel()

	warmupURL := buildWarmupURLFor(target, visitURL)
	logf("先访问预热地址 [%s] %s", target.DisplayName(), warmupURL)
	if err := b.do(tabCtx, timeout, chromedp.Navigate(warmupURL)); err != nil {
		return b.classify(target, fmt.Errorf("访问预热地址失败: %w", err))
	}

	if len(cookies) == 0 {
		logf("目标 [%s] 未解析到任何 CookieCloud Cookie，配置域名 [%s]", target.DisplayName(), target.CookieDomain)
	} else {
		logf("目标 [%s] 共解析到 %d 个 CookieCloud Cookie", target.DisplayName(), len(cookies))
	}

	host := hostOf(target.URL)
	if err := b.clearCookiesForHost(tabCtx, host); err != nil {
		logf("清除域名 [%s] 下的旧 Cookie 失败: %v", host, err)
	}

	// Preserve each source cookie's scope; never rewrite a www cookie to apex.
	seen := map[string]bool{}
	injected := 0
	for _, ck := range cookies {
		key := ck.Name + "|" + ck.Domain + "|" + defaultString(ck.Path, "/") + fmt.Sprint(ck.HostOnly)
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := b.addCookieForTarget(tabCtx, ck, target.URL); err != nil {
			logf("跳过 Cookie [%s]: %v", ck.Name, err)
			continue
		}
		injected++
	}
	logf("目标 [%s] 成功注入 %d 个 Cookie（去重前 %d 个）", target.DisplayName(), injected, len(cookies))

	for _, item := range target.LocalStorage {
		if strings.TrimSpace(item.Key) == "" {
			continue
		}
		keyLit, _ := json.Marshal(item.Key)
		valLit, _ := json.Marshal(item.Value)
		script := fmt.Sprintf(`(() => { try { localStorage.setItem(%s, %s); return true } catch (e) { return false } })()`, keyLit, valLit)
		ok, err := runValue(tabCtx, timeout, chromedp.Evaluate[bool](script))
		if err != nil {
			return b.classify(target, fmt.Errorf("写入 localStorage[%s] 失败: %w", item.Key, err))
		}
		if !ok {
			return fmt.Errorf("写入 localStorage 失败（源策略或存储不可用）")
		}
	}
	return nil
}

// do 带看门狗：单条 CDP 命令挂住时靠外层 select 兜底，避免整轮任务卡死。
func (b *Browser) do(ctx context.Context, timeout time.Duration, steps ...chromedp.Action[chromedp.Void]) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- chromedp.Do(ctx, steps...)
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("CDP 命令超时（%s）: %w", timeout, ctx.Err())
	}
}

func runValue[T any](ctx context.Context, timeout time.Duration, action chromedp.Action[T]) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type outcome struct {
		value T
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		v, err := chromedp.Run(ctx, action)
		done <- outcome{v, err}
	}()

	select {
	case r := <-done:
		return r.value, r.err
	case <-ctx.Done():
		return zero, fmt.Errorf("CDP 命令超时（%s）: %w", timeout, ctx.Err())
	}
}

// clearCookiesForHost 删掉浏览器里属于该 host 的所有 Cookie。
// 持久化 profile 会跨轮次累积旧 Cookie，只按 name 删除不够，必须整域清空。
func (b *Browser) clearCookiesForHost(ctx context.Context, host string) error {
	if host == "" {
		return nil
	}
	return b.do(ctx, 20*time.Second, chromedp.Func(func(c context.Context, t *chromedp.Target) error {
		res, err := cdp.Call(c, t, network.GetCookies, network.GetCookiesParams{})
		if err != nil {
			return err
		}
		removed := 0
		for _, ck := range res.Cookies {
			d := strings.TrimPrefix(ck.Domain, ".")
			if d != host && !strings.HasSuffix(host, "."+d) && !strings.HasSuffix(d, "."+host) {
				continue
			}
			if _, err := cdp.Call(c, t, network.DeleteCookies, network.DeleteCookiesParams{
				Name:   ck.Name,
				Domain: ck.Domain,
				Path:   ck.Path,
			}); err != nil {
				return err
			}
			removed++
		}
		if removed > 0 {
			logf("已清除域名 [%s] 下的 %d 个旧 Cookie", host, removed)
		}
		return nil
	}))
}

// deleteCookie 按 name+domain 删除同名 Cookie，避免新旧值冲突。
func (b *Browser) deleteCookie(ctx context.Context, ck Cookie) error {
	return b.do(ctx, 15*time.Second, chromedp.Func(func(c context.Context, t *chromedp.Target) error {
		params := network.DeleteCookiesParams{
			Name: ck.Name,
			Path: defaultString(ck.Path, "/"),
		}
		if d := strings.TrimSpace(ck.Domain); d != "" {
			params.Domain = d
		}
		_, err := cdp.Call(c, t, network.DeleteCookies, params)
		return err
	}))
}

func (b *Browser) addCookieForTarget(ctx context.Context, ck Cookie, targetURL string) error {
	params, err := cookieParams(ck, targetURL)
	if err != nil {
		return err
	}
	return b.do(ctx, 15*time.Second, chromedp.Func(func(c context.Context, t *chromedp.Target) error {
		_, err := cdp.Call(c, t, network.SetCookie, params)
		return err
	}))
}

func (b *Browser) classify(target Target, err error) error {
	if isBrowserClosed(err) {
		return fmt.Errorf("%w: %v", ErrBrowserClosed, err)
	}
	return err
}

// checkStatus 判定登录态，优先级：
//  1. 目标配置了 okSelector / failSelector → 按 DOM 选择器
//  2. NexusPHP 通用标记（logout.php / usercp.php 为已登录，takelogin.php 为未登录）
//  3. 标题关键字（兼容原版行为）
func (b *Browser) checkStatus(ctx context.Context, target Target) (bool, string) {
	deadline := time.Now().Add(25 * time.Second)
	var lastTitle string
	for {
		title, err := runValue(ctx, 8*time.Second, chromedp.Title())
		if err != nil {
			logf("目标 [%s] 读取标题失败: %v", target.DisplayName(), err)
			return false, "登录状态检查失败（读取页面标题失败）"
		}
		if title != "" {
			lastTitle = title
		}

		if target.LoginPath != "" {
			location, err := runValue(ctx, 5*time.Second, chromedp.Evaluate[string](`location.href`))
			if err != nil {
				return false, "登录状态检查失败（无法读取当前URL）"
			}
			if matchesLoginPath(location, target.LoginPath) {
				return false, "登录状态检查失败（被重定向到登录页）"
			}
		}

		switch {
		case target.FailSelector != "" || target.OKSelector != "":
			if hit, err := b.selectorExists(ctx, target.FailSelector); err != nil {
				logf("目标 [%s] 失败选择器求值出错: %v", target.DisplayName(), err)
			} else if hit {
				return false, "登录状态检查失败（命中失败选择器）"
			}
			if hit, err := b.selectorExists(ctx, target.OKSelector); err != nil {
				logf("目标 [%s] 成功选择器求值出错: %v", target.DisplayName(), err)
			} else if hit {
				return true, ""
			}
		default:
			// NexusPHP 站点的通用特征，比标题关键字可靠得多
			if page, err := runValue(ctx, 8*time.Second, chromedp.OuterHTML(chromedp.CSS("html"))); err == nil && page != "" {
				lower := strings.ToLower(page)
				if strings.Contains(lower, "takelogin.php") || strings.Contains(lower, "login.php?returnto") {
					return false, "登录状态检查失败（页面含登录表单）"
				}
				if strings.Contains(lower, "logout.php") || strings.Contains(lower, "usercp.php") {
					return true, ""
				}
				// SPA 站（如 Rousi Pro）未登录时页面会渲染「登录/注册」入口，
				// 且标题也叫「首页」，光看标题会误判为已登录。
				// href="/login" 的按钮 + 登录/注册文字同时命中才算未登录。
				if strings.Contains(lower, `href="/login"`) && containsAny(lower, ">登录<", ">注册<", ">sign in<", ">sign up<", ">register<") {
					return false, "登录状态检查失败（页面有登录/注册入口）"
				}
			}
			if title != "" {
				if containsAny(title, failTitles...) {
					return false, "登录状态检查失败（标题命中失败关键字：" + title + "）"
				}
				if containsAny(title, successTitles...) {
					return true, ""
				}
			}
		}

		if time.Now().After(deadline) {
			// 诊断：看看页面实际能看到哪些 Cookie
			if visible, err := runValue(ctx, 5*time.Second, chromedp.Evaluate[string](`document.cookie`)); err == nil {
				names := make([]string, 0, 8)
				for _, part := range strings.Split(visible, ";") {
					if k, _, ok := strings.Cut(strings.TrimSpace(part), "="); ok && k != "" {
						names = append(names, k)
					}
				}
				logf("目标 [%s] 页面可见 Cookie: %v", target.DisplayName(), names)
			}
			return false, "登录状态检查失败（等待页面加载超时，标题：" + lastTitle + "）"
		}
		select {
		case <-time.After(700 * time.Millisecond):
		case <-ctx.Done():
			return false, "登录状态检查失败（浏览器已关闭）"
		}
	}
}

func (b *Browser) selectorExists(ctx context.Context, selector string) (bool, error) {
	if strings.TrimSpace(selector) == "" {
		return false, nil
	}
	selLit, _ := json.Marshal(selector)
	script := fmt.Sprintf(`document.querySelectorAll(%s).length`, selLit)
	count, err := runValue(ctx, 8*time.Second, chromedp.Evaluate[int](script))
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

var (
	failTitles    = []string{"登录", "登録", "登錄", "异地", "2fa", "Login"}
	successTitles = []string{"首页", "首頁"}
)

func containsAny(haystack string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

func buildWarmupURL(target Target) string {
	return buildWarmupURLFor(target, target.URL)
}

// buildWarmupURLFor 用实际要访问的 URL 构造预热地址，保证预热和目标同源。
func buildWarmupURLFor(target Target, visitURL string) string {
	u, err := url.Parse(strings.TrimSpace(visitURL))
	if err != nil || u.Host == "" {
		return visitURL
	}
	return u.Scheme + "://" + u.Host + target.EffectiveWarmupPath()
}

func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func isBrowserClosed(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrBrowserClosed) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"websocket",
		"connection reset",
		"broken pipe",
		"target closed",
		"browser closed",
		"session deleted",
		"invalid session id",
		"not connected to devtools",
		"chrome failed to start",
		"failed to unmarshal",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
