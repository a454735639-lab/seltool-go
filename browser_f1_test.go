package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestF1CookieDomainPreservation(t *testing.T) {
	cases := []struct {
		domain, host string
		hostOnly     bool
		ok           bool
	}{
		{".example.test", "www.example.test", false, true},
		{"www.example.test", "example.test", true, false},
		{"www.example.test", "www.example.test", true, true},
		{"example.test", "www.example.test", true, false},
		{"else.test", "example.test", false, false},
	}
	for _, c := range cases {
		ck := Cookie{Name: "sid", Value: "fixture", Domain: c.domain, Path: "/", HostOnly: c.hostOnly}
		p, err := cookieParams(ck, "https://"+c.host+"/")
		if (err == nil) != c.ok {
			t.Errorf("domain=%s host=%s err=%v", c.domain, c.host, err)
		}
		if err == nil && !c.hostOnly && p.Domain != c.domain {
			t.Errorf("source domain changed: %q", p.Domain)
		}
		if err == nil && c.hostOnly && (p.Domain != "" || p.URL == "") {
			t.Errorf("host-only scope lost: %+v", p)
		}
	}
}
func TestF1CookiePrefixes(t *testing.T) {
	good := Cookie{Name: "__Host-sid", Value: "fixture", Domain: "example.test", Path: "/", Secure: true, HostOnly: true}
	if _, err := cookieParams(good, "https://example.test/"); err != nil {
		t.Fatal(err)
	}
	good.Path = "/other"
	if _, err := cookieParams(good, "https://example.test/"); err == nil {
		t.Fatal("invalid __Host path accepted")
	}
	good.Path = "/"
	good.Secure = false
	if _, err := cookieParams(good, "https://example.test/"); err == nil {
		t.Fatal("insecure __Host accepted")
	}
	good.Name = "__Secure-sid"
	if _, err := cookieParams(good, "https://example.test/"); err == nil {
		t.Fatal("insecure __Secure accepted")
	}
}
func TestF1ResourcePatterns(t *testing.T) {
	p := resourcePatterns([]string{" .PNG ", "gif", "png", "", "../bad"})
	want := []string{"*.png", "*.png?*", "*.gif", "*.gif?*"}
	if len(p) != len(want) {
		t.Fatalf("%v", p)
	}
	for i := range want {
		if p[i] != want[i] {
			t.Fatalf("%v", p)
		}
	}
}
func TestF1ProfileReusedPIDIsTreatedAsStale(t *testing.T) {
	// 容器重启后 PID 重新分配，锁里的 PID 可能属于无关进程；
	// 只有确认是 Chrome 才拒绝，否则按残留清理。
	dir := t.TempDir()
	h, _ := os.Hostname()
	lock := filepath.Join(dir, "SingletonLock")
	if err := os.Symlink(h+"-"+strconv.Itoa(os.Getpid()), lock); err != nil {
		t.Fatal(err)
	}
	if isChromeProcess(os.Getpid()) {
		t.Skip("test process looks like chrome")
	}
	if err := prepareProfile(dir); err != nil {
		t.Fatalf("stale lock should be cleared: %v", err)
	}
	if _, err := os.Lstat(lock); !os.IsNotExist(err) {
		t.Fatal("stale lock not removed")
	}
}
func TestF1NonChromeProcessIsNotOwner(t *testing.T) {
	if isChromeProcess(os.Getpid()) {
		t.Fatal("test binary must not be treated as chrome")
	}
	if isChromeProcess(0) || isChromeProcess(-1) {
		t.Fatal("invalid pid must be false")
	}
}
func TestF1ProfileLockExclusion(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release, err := acquireProfile(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if r, e := acquireProfile(ctx, dir); e == nil {
		r()
		t.Fatal("shared profile accepted")
	}
}
func TestF1LoginPathUsesLocation(t *testing.T) {
	if !matchesLoginPath("https://site.test/login.php?returnto=home", "/login.php") {
		t.Fatal("URL login redirect not detected")
	}
	if matchesLoginPath("https://site.test/home", "/login.php") {
		t.Fatal("home misclassified")
	}
}

func TestF1MissingDomainIsHostOnly(t *testing.T) {
	p, err := cookieParams(Cookie{Name: "sid", Value: "fixture", Path: "/"}, "https://example.test/")
	if err != nil {
		t.Fatal(err)
	}
	if p.Domain != "" || p.URL != "https://example.test/" {
		t.Fatalf("missing domain widened: %+v", p)
	}
}

func TestF1ParentTargetKeepsChildCookieScope(t *testing.T) {
	// 目标 pttime.org，Cookie 在 www.pttime.org：写入子域 URL，不扩到父域。
	p, err := cookieParams(Cookie{Name: "sid", Value: "v", Domain: "www.pttime.org", Path: "/"}, "https://pttime.org/")
	if err != nil {
		t.Fatal(err)
	}
	if p.URL != "https://www.pttime.org/" {
		t.Fatalf("cookie url widened: %+v", p)
	}
	if p.Domain != "www.pttime.org" {
		t.Fatalf("cookie domain changed: %+v", p)
	}
	// 反向：目标 www，Cookie 在父域 —— 保留父域作用域，正常写入。
	p, err = cookieParams(Cookie{Name: "sid", Value: "v", Domain: ".pttime.org", Path: "/"}, "https://www.pttime.org/")
	if err != nil {
		t.Fatal(err)
	}
	if p.Domain != ".pttime.org" {
		t.Fatalf("parent domain lost: %+v", p)
	}
}

func TestF1EffectiveTargetURL(t *testing.T) {
	// 会话只在 www 子域 → 改用它
	u, sw := effectiveTargetURL(Target{URL: "https://gamegamept.com/"}, []Cookie{{Name: "c_secure_pass", Value: "v", Domain: "www.gamegamept.com", HostOnly: true}})
	if sw != "www.gamegamept.com" || u != "https://www.gamegamept.com/" {
		t.Fatalf("got %s %s", u, sw)
	}
	// 目标域自己有 Cookie → 不改
	u, sw = effectiveTargetURL(Target{URL: "https://a.com/"}, []Cookie{{Name: "sid", Value: "v", Domain: ".a.com"}})
	if sw != "" || u != "https://a.com/" {
		t.Fatalf("unexpected switch: %s %s", u, sw)
	}
	// 多个不同子域 → 不猜
	u, sw = effectiveTargetURL(Target{URL: "https://a.com/"}, []Cookie{{Domain: "www.a.com"}, {Domain: "cdn.a.com"}})
	if sw != "" {
		t.Fatalf("ambiguous switch: %s", sw)
	}
	// 保留路径与查询
	u, sw = effectiveTargetURL(Target{URL: "https://a.com/attendance.php?x=1"}, []Cookie{{Domain: "www.a.com"}})
	if sw != "www.a.com" || u != "https://www.a.com/attendance.php?x=1" {
		t.Fatalf("path lost: %s", u)
	}
}
