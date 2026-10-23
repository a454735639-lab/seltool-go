package main

import (
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type ExecResult int

const (
	ExecAccepted ExecResult = iota
	ExecBusy
	ExecTargetNotFound
)

type BatchResult int

const (
	BatchAccepted BatchResult = iota
	BatchBusy
	BatchNothingToRetry
)

type BatchSubmission struct {
	Result     BatchResult
	TargetURLs []string
}

// Automation 串联 CookieCloud、浏览器、历史记录与通知。
type Automation struct {
	cfg          *Config
	store        *TargetStore
	history      *History
	cookies      *CookieCloud
	localStorage map[string][]LocalStorage // CookieCloud 同步的 localStorage（按域名）
	webhook      *Webhook
	alerts       *AlertState

	running atomic.Bool
	runMu   sync.Mutex // 串行化浏览器实例的创建与销毁
	// browsers 按代理模式缓存浏览器：proxy=走全局代理，direct=目标直连（不带代理）。
	// 两种模式各用一个独立的 profile 目录，避免 Chromium 的 profile 单实例锁互相阻塞。
	browsers map[string]*Browser

	// errMu 只保护 lastError。此前它复用 runMu，导致浏览器启动期间（可能长达
	// 数十秒）面板查询任务状态全部阻塞，Shutdown 也无法及时进入。
	errMu     sync.RWMutex
	lastError error

	// stopCh 用于让重试等待等长睡眠提前退出；Shutdown 时关闭。
	stopCh   chan struct{}
	stopOnce sync.Once
}

func NewAutomation(cfg *Config, store *TargetStore, history *History, cookies *CookieCloud, webhook *Webhook, alerts *AlertState) *Automation {
	return &Automation{cfg: cfg, store: store, history: history, cookies: cookies, webhook: webhook, alerts: alerts, stopCh: make(chan struct{})}
}

func (a *Automation) IsRunning() bool { return a.running.Load() }

func (a *Automation) TaskError() error {
	a.errMu.RLock()
	defer a.errMu.RUnlock()
	return a.lastError
}

func (a *Automation) setTaskError(err error) {
	a.errMu.Lock()
	a.lastError = err
	a.errMu.Unlock()
}

// sleepOrStop 等待 d，期间收到停止信号则立即返回 false。
func (a *Automation) sleepOrStop(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-a.stopCh:
		return false
	}
}

func (a *Automation) acquire() bool {
	if a.running.CompareAndSwap(false, true) {
		a.setTaskError(nil)
		return true
	}
	logf("任务仍在执行中，请稍候再试")
	a.webhook.Send("任务仍在执行中", "任务仍在执行中，请稍候再试")
	return false
}

// ExecuteOnce 同步执行全部目标，供定时任务与启动任务使用。
func (a *Automation) ExecuteOnce(notificationName string) bool {
	if !a.acquire() {
		return false
	}
	defer a.running.Store(false)

	targets := a.store.Get()
	a.executeTargetTask(targets, notificationName, func() {
		a.doExecuteTargets(targets, notificationName+"任务")
	})
	return true
}

// ExecuteAsync 异步执行全部目标，供 Web 手动触发使用。
func (a *Automation) ExecuteAsync(notificationName string) bool {
	if !a.acquire() {
		return false
	}
	targets := a.store.Get()
	go func() {
		defer recoverAndLog("执行任务 " + notificationName)
		defer a.running.Store(false)
		a.executeTargetTask(targets, notificationName, func() {
			a.doExecuteTargets(targets, notificationName+"任务")
		})
	}()
	return true
}

func (a *Automation) ExecuteTargetAsync(targetURL string) ExecResult {
	target, ok := a.store.Find(targetURL)
	if !ok {
		return ExecTargetNotFound
	}
	if !a.acquire() {
		return ExecBusy
	}
	go func() {
		defer recoverAndLog("目标单独重试 " + target.DisplayName())
		defer a.running.Store(false)
		a.executeTargetTask([]Target{target}, "目标单独重试", func() {
			a.doExecuteTarget(target)
		})
	}()
	return ExecAccepted
}

func (a *Automation) ExecuteFailedAsync() BatchSubmission {
	failed := a.findFailedOrUnrun()
	if len(failed) == 0 {
		return BatchSubmission{Result: BatchNothingToRetry}
	}
	if !a.acquire() {
		return BatchSubmission{Result: BatchBusy}
	}
	urls := make([]string, 0, len(failed))
	for _, t := range failed {
		urls = append(urls, t.URL)
	}
	go func() {
		defer recoverAndLog("失败目标重试")
		defer a.running.Store(false)
		a.executeTargetTask(failed, "失败目标重试", func() {
			a.doExecuteTargets(failed, "失败目标重试任务")
		})
	}()
	return BatchSubmission{Result: BatchAccepted, TargetURLs: urls}
}

// findFailedOrUnrun 找出没有运行记录、或最近一次失败的目标。
func (a *Automation) findFailedOrUnrun() []Target {
	latest := a.history.Latest()
	var out []Target
	for _, t := range a.store.Get() {
		record, ok := latest[t.URL]
		if !ok || !record.Success {
			out = append(out, t)
		}
	}
	return out
}

func (a *Automation) executeTargetTask(targets []Target, notificationName string, task func()) {
	previous := a.history.Latest()
	started := time.Now()
	var taskFailure error
	func() {
		defer func() {
			if r := recover(); r != nil {
				taskFailure = fmt.Errorf("任务异常: %v", r)
			}
		}()
		task()
	}()
	a.sendTargetTaskNotification(targets, notificationName, previous, taskFailure, time.Since(started))
	a.alerts.DrainTargetFailures()
}

func (a *Automation) sendTargetTaskNotification(targets []Target, notificationName string, previous map[string]RunRecord, taskFailure error, elapsed time.Duration) {
	latest := a.history.Latest()

	var failed []Target
	for _, t := range targets {
		record, ok := latest[t.URL]
		prev, hadPrev := previous[t.URL]
		if !ok || !record.Success || (hadPrev && record == prev) {
			failed = append(failed, t)
		}
	}

	content := buildTaskMessage(notificationName, targets, failed, latest, previous, taskFailure, elapsed, a.history.Get)

	if a.cfg != nil && a.cfg.Automation.Notify.OnlyOnFailure && taskFailure == nil && len(failed) == 0 {
		logf("%s 全部成功，按配置跳过通知", notificationName)
		return
	}

	status := notificationName + "完成"
	if taskFailure != nil || len(failed) > 0 {
		status = notificationName + "完成（存在失败）"
	}
	a.webhook.Send(status, content)
}

// isTransientFailure 判断这类失败值不值得批次结束后再试一次。
// 站点 5xx / 超时 / 连接被拒 / 页面白屏 / 人机验证都可能是一过性的；
// 登录过期（本轮已刷新过 Cookie）、域名解析失败、证书失效、403 拒绝访问
// 重试没有意义。
func isTransientFailure(cat string) bool {
	switch cat {
	case "连接超时", "站点拒绝连接", "站点连不上", "页面打不开",
		"人机验证拦截", "网站协议异常", "跳转次数过多":
		return true
	}
	return strings.HasPrefix(cat, "站点故障")
}

// retryTransientFailures 在整批跑完后，对瞬时失败的目标各重试一次。
func (a *Automation) retryTransientFailures(targets []Target, cookieData map[string][]Cookie) {
	var retry []Target
	for _, t := range targets {
		rec, ok := a.lastFailure(t.URL)
		if !ok {
			continue
		}
		cat, _ := classifyFailure(t, rec.Message)
		if !isTransientFailure(cat) {
			continue
		}
		// 域名已经解析不出来（站点关停/换域名），重试只会白等一轮
		if host := hostOf(t.URL); host != "" && !hostResolves(host) {
			continue
		}
		retry = append(retry, t)
	}
	if len(retry) == 0 {
		return
	}

	// 上限 10 个：失败太多说明是大面积故障，逐个重试只会拖长任务
	const retryCap = 10
	skipped := 0
	if len(retry) > retryCap {
		skipped = len(retry) - retryCap
		retry = retry[:retryCap]
	}

	const wait = 60 * time.Second
	logf("发现 %d 个瞬时失败，等待 %d 秒后自动重试一次%s", len(retry), int(wait.Seconds()),
		func() string {
			if skipped > 0 {
				return fmt.Sprintf("（另有 %d 个超出上限未重试）", skipped)
			}
			return ""
		}())
	if !a.sleepOrStop(wait) {
		logf("等待重试期间收到停止信号，取消本轮自动重试")
		return
	}

	for i, t := range retry {
		logf("自动重试 %d/%d：%s", i+1, len(retry), t.DisplayName())
		browser, err := a.browserFor(t)
		if err != nil {
			logf("自动重试 [%s] 启动浏览器失败: %v", t.DisplayName(), err)
			a.history.Record(t.URL, false, buildFailureMessage(err))
			continue
		}
		if !a.visitTarget(browser, t, cookieData) {
			logf("浏览器已关闭，停止自动重试")
			return
		}
	}
}

func (a *Automation) doExecuteTargets(targets []Target, taskName string) {
	logf("开始执行%s，共 %d 个目标", taskName, len(targets))

	// 异常（panic）路径也要回收浏览器：否则该轮约 300MB 的 Chromium 与全部 tab
	// 上下文只能等下轮复用或进程退出，异常轮次内存不回。
	closed := false
	defer func() {
		if !closed {
			a.closeBrowser()
		}
	}()

	// 任务开始时拉最新 CookieCloud 数据，确保用上浏览器里刚登录的站点；
	// 若某个目标仍因 Cookie 失效失败，循环内会再刷新一次并重试该目标。
	cookieData, lsData, err := a.cookies.FetchAll()
	a.localStorage = lsData
	if err != nil {
		a.setTaskError(err)
		logf("%s的 CookieCloud 获取失败且不允许回退缓存，中止任务: %v", taskName, err)
		for _, t := range targets {
			a.history.Record(t.URL, false, "CookieCloud 获取失败，任务已中止")
		}
		return
	}

	interrupted := false
	refreshed := false // 同一轮任务内最多刷新一次，避免反复请求远端
	for i, t := range targets {
		logf("开始处理第%d个目标 %s", i+1, t.DisplayName())
		// 每个目标按自己的代理模式取浏览器：直连目标不挂全局代理。
		browser, err := a.browserFor(t)
		if err != nil {
			a.setTaskError(err)
			logf("%s启动浏览器失败: %v", taskName, err)
			a.history.Record(t.URL, false, buildFailureMessage(err))
			continue
		}
		if !a.visitTarget(browser, t, cookieData) {
			interrupted = true
			break
		}

		// 若本目标刚失败且原因是 Cookie 失效，刷新一次 CookieCloud 并重试该目标。
		if !refreshed {
			if rec, ok := a.lastFailure(t.URL); ok && isCookieFailure(rec.Message) {
				if fresh, ok2 := a.refreshCookieData(cookieData, rec.Message); ok2 {
					cookieData = fresh
					refreshed = true
					logf("用最新 Cookie 重试目标 [%s]", t.DisplayName())
					if !a.visitTarget(browser, t, cookieData) {
						interrupted = true
						break
					}
				}
			}
		}
	}
	if !interrupted {
		// 批次里偶尔会碰到站点瞬时 5xx / 超时：同一个站过一两分钟单独访问又好了。
		// 这里统一再试一次，避免「批量失败、单跑成功」的困惑。
		a.retryTransientFailures(targets, cookieData)
		a.closeBrowser()
		closed = true
	}
	logf("%s执行结束", taskName)

	// 任务结束后释放大文件的页缓存，避免 memory.current 长期虚高
	a.dropPageCacheAfterTask()
}

func (a *Automation) doExecuteTarget(target Target) {
	logf("开始单独重试目标 [%s] %s", target.DisplayName(), target.URL)

	cookieData, lsData, err := a.cookies.FetchAll()
	a.localStorage = lsData
	if err != nil {
		logf("单独重试目标 [%s] 时 CookieCloud 获取失败: %v", target.DisplayName(), err)
		a.history.Record(target.URL, false, "CookieCloud 获取失败，任务已中止")
		return
	}

	browser, err := a.browserFor(target)
	if err != nil {
		logf("单独重试目标 [%s] 启动浏览器失败: %v", target.DisplayName(), err)
		a.history.Record(target.URL, false, buildFailureMessage(err))
		return
	}

	if completed := a.visitTarget(browser, target, cookieData); completed {
		// 失败且属 Cookie 失效时刷新一次并重试
		if rec, ok := a.lastFailure(target.URL); ok && isCookieFailure(rec.Message) {
			if fresh, ok2 := a.refreshCookieData(cookieData, rec.Message); ok2 {
				logf("用最新 Cookie 重试目标 [%s]", target.DisplayName())
				a.visitTarget(browser, target, fresh)
			}
		}
		a.closeBrowser()
	}
	logf("目标 [%s] 单独重试结束", target.DisplayName())
}

// visitTarget 返回 false 表示浏览器已关闭，应中断本轮剩余目标。
func (a *Automation) visitTarget(browser *Browser, target Target, cookieData map[string][]Cookie) bool {
	// 目标未手动配置 localStorage 时，使用 CookieCloud 同步的数据补齐。
	if len(target.LocalStorage) == 0 {
		target.LocalStorage = a.cookies.LocalStorageFor(a.localStorage, target)
	}
	logf("开始打开目标 [%s] %s", target.DisplayName(), target.URL)

	cookies := a.cookies.FilterForTarget(cookieData, target)
	if err := browser.Visit(target, cookies); err != nil {
		if errors.Is(err, ErrBrowserClosed) {
			a.history.Record(target.URL, false, "浏览器已关闭")
			a.handleBrowserClosed(target, err)
			return false
		}
		logf("目标 [%s] 执行失败 %s: %v", target.DisplayName(), target.URL, err)
		a.alerts.AddTargetFailure("目标: " + target.DisplayName() + "\nURL: " + target.URL)
		a.history.Record(target.URL, false, buildFailureMessage(err))
		return true
	}

	a.history.Record(target.URL, true, "执行成功")
	return true
}

// proxyModeFor 决定某个目标使用哪种网络出口：direct（直连）或 proxy（走代理）。
// 优先级：目标 proxyMode > 目标 noProxy（旧字段）> 全局 browser.proxy-mode > 默认直连。
func (a *Automation) proxyModeFor(t Target) string {
	switch strings.ToLower(strings.TrimSpace(t.ProxyMode)) {
	case "proxy":
		return "proxy"
	case "direct":
		return "direct"
	}
	if t.NoProxy {
		return "direct"
	}
	if strings.EqualFold(strings.TrimSpace(a.cfg.Automation.Browser.ProxyMode), "proxy") {
		return "proxy"
	}
	return "direct"
}

// browserFor 返回（必要时创建）目标所需出口模式下的浏览器实例。
// 两种模式各用一个独立的 profile 目录，避免 Chromium 的 profile 单实例锁互相阻塞。
func (a *Automation) browserFor(t Target) (*Browser, error) {
	mode := a.proxyModeFor(t)

	a.runMu.Lock()
	defer a.runMu.Unlock()

	if a.browsers == nil {
		a.browsers = make(map[string]*Browser, 2)
	}
	if b := a.browsers[mode]; b != nil && b.Alive() {
		return b, nil
	}
	if b := a.browsers[mode]; b != nil {
		b.Close()
		delete(a.browsers, mode)
	}

	cfg := a.cfg.Automation.Browser
	if mode == "proxy" && strings.TrimSpace(cfg.Proxy) == "" {
		// 没配代理地址时「走代理」无从谈起，按直连处理，避免静默失败。
		logf("目标 [%s] 设置为走代理，但未配置 browser.proxy，改按直连处理", t.DisplayName())
		mode = "direct"
	}
	cfg.ProxyMode = mode
	if mode == "direct" {
		cfg.Proxy = ""
		if dir := strings.TrimSpace(cfg.UserDataDir); dir != "" {
			cfg.UserDataDir = dir + "-direct"
		}
	} else if dir := strings.TrimSpace(cfg.UserDataDir); dir != "" {
		// 两种出口用各自独立的 profile，避免互相污染，也避开 Chromium 的单实例锁。
		cfg.UserDataDir = dir + "-proxy"
	}

	b, err := NewBrowser(cfg)
	if err != nil {
		return nil, err
	}
	logf("已创建新的浏览器会话（出口：%s）", mode)
	a.browsers[mode] = b
	return b, nil
}

func (a *Automation) closeBrowser() {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	for mode, b := range a.browsers {
		b.Close()
		delete(a.browsers, mode)
	}
}

func (a *Automation) handleBrowserClosed(target Target, err error) {
	a.runMu.Lock()
	for mode, b := range a.browsers {
		b.Close()
		delete(a.browsers, mode)
	}
	a.runMu.Unlock()

	a.webhook.Send("浏览器已关闭", "执行过程中浏览器被手动关闭\n目标: "+target.DisplayName()+"\nURL: "+target.URL)
	logf("目标 [%s] 执行过程中浏览器被关闭: %v", target.DisplayName(), err)
}

func (a *Automation) Shutdown() {
	// 让正在进行的重试等待立刻退出，避免关停要干等 60s。
	a.stopOnce.Do(func() { close(a.stopCh) })
	a.closeBrowser()
}

func buildFailureMessage(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "执行失败"
	}
	first := strings.SplitN(message, "\n", 2)[0]
	if len([]rune(first)) > 300 {
		first = string([]rune(first)[:300]) + "…"
	}
	return "执行失败：" + first
}

// recoverAndLog 兜住后台 goroutine 的 panic。
//
// executeTargetTask 内部已有 recover 处理「任务体」的异常，但 goroutine
// 在它之外的动作（状态复位、通知发送等）若 panic，会导致整个进程退出。
// 这里统一兜底，把 panic 转成日志而不是崩溃。
func recoverAndLog(what string) {
	if r := recover(); r != nil {
		logf("%s 发生异常: %v\n%s", what, r, debug.Stack())
	}
}

// lastFailure 取该目标最近一条运行记录（用于判断失败原因）。
func (a *Automation) lastFailure(url string) (RunRecord, bool) {
	list := a.history.Get(url)
	if len(list) == 0 {
		return RunRecord{}, false
	}
	return list[0], true
}
