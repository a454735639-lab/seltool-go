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
	cfg     *Config
	store   *TargetStore
	history *History
	cookies *CookieCloud
	webhook *Webhook
	alerts  *AlertState

	running   atomic.Bool
	runMu     sync.Mutex // 串行化浏览器实例的创建与销毁
	browser   *Browser
	lastError error
}

func NewAutomation(cfg *Config, store *TargetStore, history *History, cookies *CookieCloud, webhook *Webhook, alerts *AlertState) *Automation {
	return &Automation{cfg: cfg, store: store, history: history, cookies: cookies, webhook: webhook, alerts: alerts}
}

func (a *Automation) IsRunning() bool { return a.running.Load() }

func (a *Automation) TaskError() error       { a.runMu.Lock(); defer a.runMu.Unlock(); return a.lastError }
func (a *Automation) setTaskError(err error) { a.runMu.Lock(); a.lastError = err; a.runMu.Unlock() }

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

func (a *Automation) doExecuteTargets(targets []Target, taskName string) {
	logf("开始执行%s，共 %d 个目标", taskName, len(targets))

	// 任务开始时拉最新 CookieCloud 数据，确保用上浏览器里刚登录的站点；
	// 若某个目标仍因 Cookie 失效失败，循环内会再刷新一次并重试该目标。
	cookieData, err := a.cookies.FetchAllCookies()
	if err != nil {
		a.setTaskError(err)
		logf("%s的 CookieCloud 获取失败且不允许回退缓存，中止任务: %v", taskName, err)
		for _, t := range targets {
			a.history.Record(t.URL, false, "CookieCloud 获取失败，任务已中止")
		}
		return
	}

	browser, err := a.getOrCreateBrowser()
	if err != nil {
		a.setTaskError(err)
		logf("%s启动浏览器失败: %v", taskName, err)
		for _, t := range targets {
			a.history.Record(t.URL, false, buildFailureMessage(err))
		}
		return
	}

	interrupted := false
	refreshed := false // 同一轮任务内最多刷新一次，避免反复请求远端
	for i, t := range targets {
		logf("开始处理第%d个目标 %s", i+1, t.DisplayName())
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
		a.closeBrowser()
	}
	logf("%s执行结束", taskName)

	// 任务结束后释放大文件的页缓存，避免 memory.current 长期虚高
	a.dropPageCacheAfterTask()
}

func (a *Automation) doExecuteTarget(target Target) {
	logf("开始单独重试目标 [%s] %s", target.DisplayName(), target.URL)

	cookieData, err := a.cookies.FetchAllCookies()
	if err != nil {
		logf("单独重试目标 [%s] 时 CookieCloud 获取失败: %v", target.DisplayName(), err)
		a.history.Record(target.URL, false, "CookieCloud 获取失败，任务已中止")
		return
	}

	browser, err := a.getOrCreateBrowser()
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

func (a *Automation) getOrCreateBrowser() (*Browser, error) {
	a.runMu.Lock()
	defer a.runMu.Unlock()

	if a.browser != nil && a.browser.Alive() {
		return a.browser, nil
	}
	if a.browser != nil {
		a.browser.Close()
		a.browser = nil
	}
	b, err := NewBrowser(a.cfg.Automation.Browser)
	if err != nil {
		return nil, err
	}
	logf("已创建新的浏览器会话")
	a.browser = b
	return b, nil
}

func (a *Automation) closeBrowser() {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	if a.browser != nil {
		a.browser.Close()
		a.browser = nil
	}
}

func (a *Automation) handleBrowserClosed(target Target, err error) {
	a.runMu.Lock()
	if a.browser != nil {
		a.browser.Close()
		a.browser = nil
	}
	a.runMu.Unlock()

	a.webhook.Send("浏览器已关闭", "执行过程中浏览器被手动关闭\n目标: "+target.DisplayName()+"\nURL: "+target.URL)
	logf("目标 [%s] 执行过程中浏览器被关闭: %v", target.DisplayName(), err)
}

func (a *Automation) Shutdown() {
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
