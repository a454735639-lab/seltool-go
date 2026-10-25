package main

// Cookie 失效时的按需刷新。
//
// 原实现每轮任务开始都向 CookieCloud 拉一次全量数据。改为：
//  1. 任务开始优先用本地缓存（CookieCloud 缓存文件），不发请求；
//  2. 某个目标因 Cookie 类原因失败时，才拉一次最新数据；
//  3. 拉到新数据后只重试这一个目标，且同一轮任务内最多刷新一次，
//     避免站点侧问题（如 502）反复触发远端请求。

import "strings"

// isCookieFailure 判断失败原因是否属于「Cookie 失效」这一类。
// 站点故障、证书问题、连接超时等不是 Cookie 问题，不该触发刷新。
func isCookieFailure(reason string) bool {
	m := strings.ToLower(reason)
	switch {
	case strings.Contains(reason, "Cloudflare 人机挑战"):
		// CF 挑战与 Cookie 无关：刷新 CookieCloud 不会让挑战通过，避免无谓的远端请求。
		// 这条必须在「登录状态检查失败」之前判断 —— CF 归因里嵌了原始原因。
		return false
	case strings.Contains(reason, "Cookie 缺失"):
		return true
	case strings.Contains(reason, "登录状态检查失败"):
		// 页面含登录表单 / 被重定向到登录页 / 标题命中登录关键字
		return true
	case strings.Contains(reason, "被重定向到登录页"):
		return true
	case strings.Contains(reason, "页面含登录表单"):
		return true
	case strings.Contains(m, "cookie"):
		return true
	}
	return false
}

// refreshCookieData 拉取最新 CookieCloud 数据。
// 返回新的数据与是否成功；失败时调用方应沿用旧数据。
func (a *Automation) refreshCookieData(current map[string][]Cookie, reason string) (map[string][]Cookie, bool) {
	logf("检测到 Cookie 失效（%s），尝试从 CookieCloud 刷新", reason)
	fresh, err := a.cookies.FetchAllCookies()
	if err != nil {
		logf("CookieCloud 刷新失败，沿用本轮已有 Cookie: %v", err)
		return current, false
	}
	if len(fresh) == 0 {
		logf("CookieCloud 刷新返回空数据，沿用本轮已有 Cookie")
		return current, false
	}
	logf("CookieCloud 刷新成功：%d 个域名", len(fresh))
	return fresh, true
}
