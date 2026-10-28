package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 登录限速：同一来源 IP 连续失败达到阈值后必须拒绝，且锁定期过后自动恢复。
func TestLoginLimiterLockout(t *testing.T) {
	l := newLoginLimiter()
	const ip = "1.2.3.4"
	for i := 0; i < maxLoginFailuresPerIP; i++ {
		if ok, _ := l.AllowIP(ip); !ok {
			t.Fatalf("第 %d 次失败前不应被锁", i+1)
		}
		l.Fail(ip, "admin")
	}
	ok, wait := l.AllowIP(ip)
	if ok {
		t.Fatal("达到失败阈值后应拒绝登录")
	}
	if wait <= 0 || wait > loginLockout {
		t.Fatalf("等待时间异常: %v", wait)
	}

	// 登录成功会清空记录。
	l.Reset(ip, "admin")
	if ok, _ := l.AllowIP(ip); !ok {
		t.Fatal("Reset 后应恢复放行")
	}
}

// 用户名维度绝不拒绝，只递增延迟 —— 否则知道用户名即可把管理员永久锁死。
func TestLoginLimiterUserNeverLocked(t *testing.T) {
	l := newLoginLimiter()
	for i := 0; i < maxLoginFailuresPerIP*3; i++ {
		l.Fail("ip:9.9.9.9", "admin")
	}
	if d := l.Penalty("admin"); d <= 0 {
		t.Fatal("大量失败后应产生递增延迟")
	} else if d > maxLoginDelay {
		t.Fatalf("延迟应封顶在 %v，实际 %v", maxLoginDelay, d)
	}
	// 换一台机器来登录管理员账号，仍然只是延迟，不会被拒。
	if ok, _ := l.AllowIP("ip:8.8.8.8"); !ok {
		t.Fatal("其它 IP 不应因用户名失败而被牵连")
	}
}

// 只锁 IP 不锁用户名：换用户名仍受同一 IP 的限制。
func TestLoginLimiterPerIP(t *testing.T) {
	l := newLoginLimiter()
	for i := 0; i < maxLoginFailuresPerIP; i++ {
		l.Fail("10.0.0.9", "u"+string(rune('a'+i%26)))
	}
	if ok, _ := l.AllowIP("10.0.0.9"); ok {
		t.Fatal("同一 IP 换用户名也应被锁")
	}
	if ok, _ := l.AllowIP("10.0.0.10"); !ok {
		t.Fatal("其它 IP 不应被牵连")
	}
}

// 企微回调白名单：空 = 拒绝（旧实现是放行所有人），且 * 不再有通配语义。
func TestContainsWeComUserDenyByDefault(t *testing.T) {
	cases := []struct {
		whitelist string
		from      string
		want      bool
	}{
		{"", "ZhangSan", false},
		{"   ", "ZhangSan", false},
		{"*", "ZhangSan", false},
		{"ZhangSan", "ZhangSan", true},
		{"ZhangSan,LiSi", "LiSi", true},
		{"ZhangSan,LiSi", "WangWu", false},
		{"ZhangSan", "", false},
		// 以前 contains("*") 会让这种输入变成全放行。
		{"zhang*li", "ZhangSan", false},
	}
	for _, c := range cases {
		if got := containsWeComUser(c.whitelist, c.from); got != c.want {
			t.Errorf("containsWeComUser(%q,%q)=%v, want %v", c.whitelist, c.from, got, c.want)
		}
	}
}

// 会话必须有 TTL：过期后 Get 必须失败并顺手删除。
func TestSessionExpiry(t *testing.T) {
	st := NewSessionStore()
	id, sess := st.Create("admin")
	if _, ok := st.Get(id); !ok {
		t.Fatal("新建会话应可读取")
	}
	// 直接把过期时间推到过去，模拟自然过期。
	sess.ExpiresAt = time.Now().Add(-time.Minute)
	if _, ok := st.Get(id); ok {
		t.Fatal("过期会话不应通过校验")
	}
	if _, ok := st.sessions[id]; ok {
		t.Fatal("过期会话应被删除")
	}
}

// 会话总数上限：匿名请求不能把内存撑爆。
func TestSessionCap(t *testing.T) {
	st := NewSessionStore()
	for i := 0; i < maxSessions+64; i++ {
		st.Create("")
	}
	st.mu.RLock()
	n := len(st.sessions)
	st.mu.RUnlock()
	if n > maxSessions {
		t.Fatalf("会话数 %d 超过上限 %d", n, maxSessions)
	}
}

// 图标 data URI：MIME 前缀被污染时即便 base64 段是合法 PNG 也必须拒绝。
func TestLooksLikeImageRejectsMimeInjection(t *testing.T) {
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	if !looksLikeImage("data:image/png;base64," + png) {
		t.Fatal("正常 PNG data URI 应被接受")
	}
	bad := []string{
		`data:x"><img src=x onerror=alert(1)>;base64,` + png,
		"data:text/html;base64," + png,
		"data:image/svg+xml;base64," + png,
		"https://example.com/a.png",
	}
	for _, b := range bad {
		if looksLikeImage(b) {
			t.Errorf("应拒绝: %.60s", b)
		}
	}
}

// 反代终止 TLS 时也要判定为 HTTPS，否则会话 cookie 永远不带 Secure。
// 只有显式列入 trusted-proxies 的来源才采信转发头。
func TestIsHTTPSBehindProxy(t *testing.T) {
	// 默认只信任回环：容器/同网段的来源不能仅凭「私网」就采信转发头。
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.17.0.1:54321"
	r.Header.Set("X-Forwarded-Proto", "https")
	if isHTTPS(r) {
		t.Fatal("未列入 trusted-proxies 的私网来源不应采信 X-Forwarded-Proto")
	}

	t.Cleanup(func() { _ = setTrustedProxies(nil) })
	if err := setTrustedProxies([]string{"172.17.0.0/16"}); err != nil {
		t.Fatalf("配置可信代理失败: %v", err)
	}
	if !isHTTPS(r) {
		t.Fatal("加入 trusted-proxies 后带 X-Forwarded-Proto=https 应判定为 HTTPS")
	}
	if !isHTTPS(loopbackRequest("X-Forwarded-Proto", "https")) {
		t.Fatal("回环反代应始终可信")
	}

	// 外部直连客户端伪造该头不应生效。
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.RemoteAddr = "203.0.113.9:443"
	r2.Header.Set("X-Forwarded-Proto", "https")
	if isHTTPS(r2) {
		t.Fatal("公网直连伪造 X-Forwarded-Proto 不应被采信")
	}
}

func loopbackRequest(header, value string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:54321"
	r.Header.Set(header, value)
	return r
}

// 限速依赖 clientIP：不可信来源必须按 TCP 对端计，否则每次换个
// X-Forwarded-For 就是新 key，20 次/10 分钟的 IP 硬锁形同虚设。
func TestClientIPIgnoresUntrustedForwardedFor(t *testing.T) {
	t.Cleanup(func() { _ = setTrustedProxies(nil) })

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.168.1.50:1234"
	r.Header.Set("X-Forwarded-For", "1.1.1.1")
	if got := clientIP(r); got != "192.168.1.50" {
		t.Fatalf("不可信私网来源应返回对端地址，实际 %q", got)
	}

	r.Header.Set("X-Forwarded-For", "2.2.2.2")
	if got := clientIP(r); got != "192.168.1.50" {
		t.Fatalf("伪造第二个地址也不应生效，实际 %q", got)
	}

	if err := setTrustedProxies([]string{"192.168.1.0/24"}); err != nil {
		t.Fatalf("配置可信代理失败: %v", err)
	}
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.7")
	if got := clientIP(r); got != "203.0.113.7" {
		t.Fatalf("应取最靠近服务端的一跳，实际 %q", got)
	}
	// 右侧一跳本身也是可信代理时继续往左找。
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 192.168.1.9")
	if got := clientIP(r); got != "203.0.113.7" {
		t.Fatalf("应跳过可信代理跳到真实客户端，实际 %q", got)
	}
}
