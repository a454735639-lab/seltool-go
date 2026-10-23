package main

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// 出站 URL 校验：内网、回环、云元数据地址必须被拒。
func TestGuardOutboundURL(t *testing.T) {
	blocked := []string{
		"http://127.0.0.1/admin",
		"http://localhost:8080/",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.5/",
		"http://192.168.3.20:5000/",
		"http://172.16.1.1/",
		"http://[::1]/",
		"http://0.0.0.0/",
		"file:///etc/passwd",
		"gopher://example.com/",
	}
	for _, u := range blocked {
		if err := guardOutboundURL(u); err == nil {
			t.Errorf("应拒绝 %s，但通过了", u)
		}
	}

	allowed := []string{
		"http://example.com/a.png",
		"https://img.example.com:8443/a.png",
	}
	for _, u := range allowed {
		if err := guardOutboundURL(u); err != nil {
			t.Errorf("应放行 %s，却被拒: %v", u, err)
		}
	}
}

// 连接时二次校验：DNS 解析出内网地址也要拦（防 rebinding）。
func TestGuardDialRejectsPrivateIP(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "::1", "fe80::1"} {
		if _, err := guardDialAddress(context.Background(), net.JoinHostPort(ip, "80")); err == nil {
			t.Errorf("拨号目标 %s 应被拒绝", ip)
		}
	}
	if _, err := guardDialAddress(context.Background(), "93.184.216.34:80"); err != nil {
		t.Errorf("公网地址不应被拒: %v", err)
	}
}

// 回调防重放：时间戳窗口 + MsgId 去重。
func TestReplayGuard(t *testing.T) {
	g := newReplayGuard()
	now := time.Now().Unix()

	if !g.Check(now, "msg-1") {
		t.Fatal("首次消息应通过")
	}
	if g.Check(now, "msg-1") {
		t.Fatal("同一 MsgId 重放必须被拒")
	}
	if g.Check(now-3600, "msg-old") {
		t.Fatal("超出时间窗口必须被拒")
	}
	if g.Check(now+3600, "msg-future") {
		t.Fatal("未来时间戳必须被拒")
	}
	if !g.Check(now, "msg-2") {
		t.Fatal("不同 MsgId 应通过")
	}
}

// 企微 markdown 转义：站点名不能变成可点击链接。
func TestMdEsc(t *testing.T) {
	got := mdEsc("[点我领奖](http://evil)")
	if strings.HasPrefix(got, "[") || strings.Contains(got, "](") {
		t.Fatalf("方括号未被转义: %q", got)
	}
	if !strings.Contains(got, `\[`) {
		t.Fatalf("应输出转义后的方括号: %q", got)
	}
}

// 匿名会话不能把已登录会话挤掉。
func TestAnonSessionsCannotEvictAuthed(t *testing.T) {
	store := NewSessionStore()
	authedID, _ := store.Create("admin")

	for i := 0; i < maxAnonSession*3; i++ {
		store.Create("")
	}

	store.mu.RLock()
	defer store.mu.RUnlock()
	if _, ok := store.sessions[authedID]; !ok {
		t.Fatal("已登录会话被匿名流量挤掉了")
	}
	anon := 0
	for _, s := range store.sessions {
		if s.Username == "" {
			anon++
		}
	}
	if anon > maxAnonSession {
		t.Fatalf("匿名会话数 %d 超过上限 %d", anon, maxAnonSession)
	}
}
