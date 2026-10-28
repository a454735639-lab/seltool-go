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
		// Windows 上创建符号链接需要管理员权限或开发者模式，这不是被测逻辑的问题。
		t.Skipf("当前环境无法创建符号链接: %v", err)
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

// 回归：磁盘 HTTP 缓存必须清理，但身份数据（Cookies / Login Data / Local Storage）必须保留。
// 背景：cspt.top 早先一次真实 403 被写进 profile 的磁盘缓存后，每次运行都从缓存返回该 403，
// 表现为该站永久失败（cookie 其实是好的）。清缓存后立即恢复已登录首页。
func TestF1ProfileCacheIsClearedButIdentityKept(t *testing.T) {
	dir := t.TempDir()

	// 造出生产 profile 里实测观察到的缓存目录布局
	cacheFiles := []string{
		filepath.Join("Default", "Cache", "Cache_Data", "f_000001"),
		filepath.Join("Default", "Code Cache", "js", "0"),
		filepath.Join("Default", "GPUCache", "d"),
		filepath.Join("Default", "Service Worker", "CacheStorage", "x"),
		filepath.Join("GrShaderCache", "GPUCache", "y"),
	}
	for _, rel := range cacheFiles {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	identity := []string{
		filepath.Join("Default", "Cookies"),
		filepath.Join("Default", "Login Data"),
		filepath.Join("Default", "Local Storage", "leveldb", "CURRENT"),
	}
	for _, rel := range identity {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := clearProfileCache(dir); err != nil {
		t.Fatalf("clearProfileCache: %v", err)
	}
	for _, rel := range cacheFiles {
		if _, err := os.Lstat(filepath.Join(dir, rel)); !os.IsNotExist(err) {
			t.Errorf("缓存残留未清: %s", rel)
		}
	}
	for _, rel := range identity {
		if _, err := os.Lstat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("身份数据被误删: %s (%v)", rel, err)
		}
	}
	// 幂等：目录已不存在时不得报错（每轮启动都会调用）
	if err := clearProfileCache(dir); err != nil {
		t.Fatalf("重复清理不应报错: %v", err)
	}
}

// 回归：prepareProfile 是启动路径上的必经点，必须真的清掉缓存。
func TestF1PrepareProfileClearsCache(t *testing.T) {
	dir := t.TempDir()
	cacheFile := filepath.Join(dir, "Default", "Cache", "Cache_Data", "f_000001")
	if err := os.MkdirAll(filepath.Dir(cacheFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cacheFile, []byte("stale403"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareProfile(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(cacheFile); !os.IsNotExist(err) {
		t.Fatal("prepareProfile 未清除磁盘缓存，历史错误响应会「复活」")
	}
}
