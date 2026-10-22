package main

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 登录失败限速。
//
// 此前 /api/auth/login 没有任何限流：bcrypt 单次约 60ms，脚本并发撞库几分钟就能
// 试完常见弱口令（本服务默认口令是 pass12345 这类）。这里按「来源 IP」与「用户名」
// 两条维度各自计数，任一维度达到阈值即锁定一段时间。
const (
	maxLoginFailures = 5
	loginFailWindow  = 10 * time.Minute
	loginLockout     = 15 * time.Minute
)

type loginFailEntry struct {
	fails    int
	firstAt  time.Time
	lockedAt time.Time
}

type loginLimiter struct {
	mu    sync.Mutex
	items map[string]*loginFailEntry
	sweep int
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{items: make(map[string]*loginFailEntry)}
}

// Allow 判断这组键是否放行；被锁定时返回剩余等待时间。
func (l *loginLimiter) Allow(keys ...string) (bool, time.Duration) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	var worst time.Duration
	for _, k := range keys {
		e := l.items[k]
		if e == nil || e.lockedAt.IsZero() {
			continue
		}
		if left := loginLockout - now.Sub(e.lockedAt); left > 0 {
			if left > worst {
				worst = left
			}
		} else {
			// 锁定期已过，清零重新计数。
			delete(l.items, k)
		}
	}
	return worst == 0, worst
}

// Fail 记录一次失败；同一窗口内累计达到阈值则开始锁定。
func (l *loginLimiter) Fail(keys ...string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	// 顺手清理长期不活跃的条目，避免被随机用户名撑大内存。
	l.sweep++
	if l.sweep >= 512 {
		l.sweep = 0
		for k, e := range l.items {
			last := e.firstAt
			if e.lockedAt.After(last) {
				last = e.lockedAt
			}
			if now.Sub(last) > loginFailWindow+loginLockout {
				delete(l.items, k)
			}
		}
	}
	for _, k := range keys {
		if k == "" {
			continue
		}
		e := l.items[k]
		if e == nil {
			e = &loginFailEntry{firstAt: now}
			l.items[k] = e
		}
		if now.Sub(e.firstAt) > loginFailWindow {
			e.fails = 0
			e.firstAt = now
			e.lockedAt = time.Time{}
		}
		e.fails++
		if e.fails >= maxLoginFailures {
			e.lockedAt = now
			e.fails = 0
		}
	}
}

// Reset 在登录成功后清除该键的失败记录。
func (l *loginLimiter) Reset(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.items, k)
	}
}

// clientIP 取请求来源 IP；反代在私有地址时才采信 X-Forwarded-For 的第一段。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	if !isPrivateAddr(host) {
		return host
	}
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return host
	}
	first := strings.TrimSpace(strings.Split(xff, ",")[0])
	if net.ParseIP(first) != nil {
		return first
	}
	return host
}

func isPrivateAddr(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// isHTTPS 判断本次请求是否走 HTTPS。反代终止 TLS 时 r.TLS 恒为 nil，
// 只靠它会让 Secure 永远为 false（会话 cookie 可被明文链路窃取）。
// 因此仅在来源是内网/回环（即本机反代）时才采信 X-Forwarded-Proto，避免外部
// 客户端伪造该头导致自己拿不到 cookie。
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	if !isPrivateAddr(host) {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Ssl")), "on")
}
