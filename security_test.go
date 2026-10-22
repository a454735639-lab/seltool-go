package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

// 登录限速：连续失败达到阈值后必须拒绝，且锁定期过后自动恢复。
func TestLoginLimiterLockout(t *testing.T) {
	l := newLoginLimiter()
	keys := []string{"ip:1.2.3.4", "user:admin"}
	for i := 0; i < maxLoginFailures; i++ {
		if ok, _ := l.Allow(keys...); !ok {
			t.Fatalf("第 %d 次失败前不应被锁", i+1)
		}
		l.Fail(keys...)
	}
	ok, wait := l.Allow(keys...)
	if ok {
		t.Fatal("达到失败阈值后应拒绝登录")
	}
	if wait <= 0 || wait > loginLockout {
		t.Fatalf("等待时间异常: %v", wait)
	}

	// 登录成功会清空记录。
	l.Reset(keys...)
	if ok, _ := l.Allow(keys...); !ok {
		t.Fatal("Reset 后应恢复放行")
	}
}

// 只锁 IP 不锁用户名：换用户名仍受同一 IP 的限制。
func TestLoginLimiterPerIP(t *testing.T) {
	l := newLoginLimiter()
	for i := 0; i < maxLoginFailures; i++ {
		l.Fail("ip:10.0.0.9", "user:u"+string(rune('a'+i)))
	}
	if ok, _ := l.Allow("ip:10.0.0.9", "user:someone-else"); ok {
		t.Fatal("同一 IP 换用户名也应被锁")
	}
	if ok, _ := l.Allow("ip:10.0.0.10", "user:someone-else"); !ok {
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
func TestIsHTTPSBehindProxy(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.17.0.1:54321"
	r.Header.Set("X-Forwarded-Proto", "https")
	if !isHTTPS(r) {
		t.Fatal("内网反代带 X-Forwarded-Proto=https 时应判定为 HTTPS")
	}
	// 外部直连客户端伪造该头不应生效。
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.RemoteAddr = "203.0.113.9:443"
	r2.Header.Set("X-Forwarded-Proto", "https")
	if isHTTPS(r2) {
		t.Fatal("公网直连伪造 X-Forwarded-Proto 不应被采信")
	}
}
