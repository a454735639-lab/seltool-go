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
// 旧实现把「来源 IP」和「用户名」放在同一套硬锁逻辑里：任一维度失败 5 次就锁
// 15 分钟。问题是用户名维度对攻击者毫无成本 —— 任何人连发 5 个错误口令就能把
// 管理员锁在门外，而且每次再试都会续期，等于一个永久的登录拒绝服务。
//
// 现在按维度分开：
//   - IP 维度：仍然硬锁（这是攻击者自己的地址，锁了不影响别人）；
//   - 用户名维度：不锁，改为递增延迟（第 4 次起 250ms 起翻倍，上限 3s）。
//     攻击者撞库的成本照样上去了，但合法用户哪怕在被攻击期间也能登进来，
//     只是要多等一两秒。
const (
	maxLoginFailuresPerIP = 20 // 单 IP 10 分钟内失败次数上限
	loginFailWindow       = 10 * time.Minute
	loginLockout          = 15 * time.Minute

	userDelayAfterFails = 3 // 用户名维度第几次失败开始加延迟
	userDelayBase       = 250 * time.Millisecond
	maxLoginDelay       = 3 * time.Second
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

func ipKey(ip string) string { return "ip:" + ip }

func userKey(name string) string { return "user:" + strings.ToLower(strings.TrimSpace(name)) }

// AllowIP 判断该来源 IP 是否被锁；被锁时返回剩余时间。
func (l *loginLimiter) AllowIP(ip string) (bool, time.Duration) {
	if ip == "" {
		return true, 0
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	k := ipKey(ip)
	e := l.items[k]
	if e == nil || e.lockedAt.IsZero() {
		return true, 0
	}
	if left := loginLockout - now.Sub(e.lockedAt); left > 0 {
		return false, left
	}
	// 锁定期已过，清零重新计数。
	delete(l.items, k)
	return true, 0
}

// Penalty 返回该用户名应附加的延迟。只延时，不拒绝 —— 避免「知道用户名就能
// 锁死对方账号」的拒绝服务。
func (l *loginLimiter) Penalty(username string) time.Duration {
	if strings.TrimSpace(username) == "" {
		return 0
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.items[userKey(username)]
	if e == nil {
		return 0
	}
	if now.Sub(e.firstAt) > loginFailWindow {
		return 0
	}
	if e.fails < userDelayAfterFails {
		return 0
	}
	shift := e.fails - userDelayAfterFails
	if shift > 4 {
		shift = 4
	}
	d := userDelayBase << uint(shift)
	if d > maxLoginDelay {
		d = maxLoginDelay
	}
	return d
}

// Fail 记录一次失败。
func (l *loginLimiter) Fail(ip, username string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

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

	bump := func(key string, lockAt int) {
		if key == "" {
			return
		}
		e := l.items[key]
		if e == nil {
			e = &loginFailEntry{firstAt: now}
			l.items[key] = e
		}
		if now.Sub(e.firstAt) > loginFailWindow {
			e.fails = 0
			e.firstAt = now
			e.lockedAt = time.Time{}
		}
		e.fails++
		if lockAt > 0 && e.fails >= lockAt {
			e.lockedAt = now
			e.fails = 0
		}
	}
	bump(ipKey(ip), maxLoginFailuresPerIP)
	bump(userKey(username), 0) // 用户名维度永不硬锁
}

// Reset 在登录成功后清除记录。
func (l *loginLimiter) Reset(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.items, ipKey(ip))
	delete(l.items, userKey(username))
}

// clientIP 取请求来源 IP。
//
// 反代（nginx `$proxy_add_x_forwarded_for`）会把真实客户端地址追加在
// X-Forwarded-For 的最右边，左边的段是客户端自己可以随便写的。旧实现取第一段，
// 于是伪造 X-Forwarded-For 就能无限轮换「IP」绕过限速。这里改取最后一段。
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
	parts := strings.Split(xff, ",")
	last := strings.TrimSpace(parts[len(parts)-1])
	if net.ParseIP(last) != nil {
		return last
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
