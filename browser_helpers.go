package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
)

var resourceSuffix = regexp.MustCompile(`^[a-z0-9]+$`)

func resourcePatterns(suffixes []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range suffixes {
		s = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), ".")
		if !resourceSuffix.MatchString(s) || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, "*."+s, "*."+s+"?*")
	}
	return out
}

// Never widen the original cookie scope to make another host appear authenticated.
func cookieParams(ck Cookie, targetURL string) (network.SetCookieParams, error) {
	u, err := url.Parse(targetURL)
	if err != nil || u.Hostname() == "" {
		return network.SetCookieParams{}, fmt.Errorf("invalid cookie target")
	}
	if strings.TrimSpace(ck.Domain) == "" {
		ck.HostOnly = true
	}
	host := strings.ToLower(u.Hostname())
	d := strings.ToLower(strings.TrimPrefix(ck.Domain, "."))
	if d == "" {
		d = host
	}
	if ck.HostOnly && host != d {
		return network.SetCookieParams{}, fmt.Errorf("host-only cookie host mismatch")
	}
	if !ck.HostOnly && host != d && !strings.HasSuffix(host, "."+d) && !strings.HasSuffix(d, "."+host) {
		return network.SetCookieParams{}, fmt.Errorf("cookie domain mismatch")
	}
	// 目标 host 是 Cookie 域的父域（目标 pttime.org，Cookie 在 www.pttime.org）：
	// Cookie 的真实作用域是子域，按子域 URL 写入，既不扩大作用域也能让站点跳转后带上。
	cookieURL := targetURL
	if !ck.HostOnly && d != host && strings.HasSuffix(d, "."+host) {
		cookieURL = u.Scheme + "://" + d + defaultString(ck.Path, "/")
	}
	if strings.HasPrefix(ck.Name, "__Secure-") && (!ck.Secure || u.Scheme != "https") {
		return network.SetCookieParams{}, fmt.Errorf("invalid __Secure- cookie")
	}
	if strings.HasPrefix(ck.Name, "__Host-") && (!ck.Secure || u.Scheme != "https" || defaultString(ck.Path, "/") != "/" || strings.HasPrefix(ck.Domain, ".") || d != host) {
		return network.SetCookieParams{}, fmt.Errorf("invalid __Host- cookie")
	}
	if d != "" && !ck.HostOnly && strings.HasSuffix(d, ".") {
		return network.SetCookieParams{}, fmt.Errorf("invalid cookie domain")
	}
	secure, httpOnly := ck.Secure, ck.HTTPOnly
	p := network.SetCookieParams{Name: ck.Name, Value: ck.Value, URL: cookieURL, Path: defaultString(ck.Path, "/"), Secure: &secure, HTTPOnly: &httpOnly}
	if !ck.HostOnly && !strings.HasPrefix(ck.Name, "__Host-") {
		p.Domain = ck.Domain
	}
	return p, nil
}

func matchesLoginPath(location, path string) bool {
	if path == "" {
		return false
	}
	u, e := url.Parse(location)
	if e != nil {
		return false
	}
	return strings.Contains(strings.ToLower(u.Path), strings.ToLower(path))
}

// The application lock prevents two instances from sharing a profile. Chrome's
// own socket/lock must still be checked before clearing confirmed stale symlinks.
func acquireProfile(ctx context.Context, dir string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := openProfileLock(filepath.Join(dir, ".seltool-owner.lock"))
	if err != nil {
		return nil, fmt.Errorf("browser profile already owned: %w", err)
	}
	if err = lockFileExclusive(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("browser profile already owned")
	}
	var once sync.Once
	release := func() { once.Do(func() { _ = unlockFile(f); _ = f.Close() }) }
	if err = prepareProfile(dir); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// profileCacheDirs 列出 Chromium 会写入磁盘的网络缓存目录（相对 profile 根目录）。
// 这些目录只缓存网络响应，删除不影响身份数据 —— Cookies、Login Data、Local Storage
// 都不在列表内，所以清理不会导致掉登录。
var profileCacheDirs = []string{
	filepath.Join("Default", "Cache"),
	filepath.Join("Default", "Code Cache"),
	filepath.Join("Default", "GPUCache"),
	filepath.Join("Default", "DawnGraphiteCache"),
	filepath.Join("Default", "DawnWebGPUCache"),
	filepath.Join("Default", "Service Worker", "CacheStorage"),
	filepath.Join("Default", "Service Worker", "ScriptCache"),
	"GrShaderCache",
	"ShaderCache",
}

// clearProfileCache 在浏览器启动前清空磁盘 HTTP 缓存。
//
// 为什么每次启动都要清：profile 是持久化的，Chromium 会把响应写进磁盘缓存，而缓存
// 不区分「这是一次错误响应」。实测 cspt.top 早先某次真实的 403 被写进缓存后，之后每次
// 运行都直接从缓存返回那 403（跨天、跨重启一致），表现为该站永久失败 —— 而同一份
// CookieCloud cookie 用 curl 直连/走代理都是 200，Lightpanda 也拿到已登录首页。
// 站点侧完全看不到请求，日志只显示「导航成功但标题是 403」，极易误判成反爬或 cookie 失效。
// 清掉缓存后立刻恢复「财神 :: 首页」已登录（连测 3 次稳定）。
//
// 代价可忽略：本站场景每个站点每轮只访问一次，磁盘缓存几乎无收益。
func clearProfileCache(dir string) error {
	var errs []string
	for _, rel := range profileCacheDirs {
		p := filepath.Join(dir, rel)
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", rel, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func prepareProfile(dir string) error {
	lock := filepath.Join(dir, "SingletonLock")
	if target, err := os.Readlink(lock); err == nil {
		host, _ := os.Hostname()
		prefix := host + "-"
		if strings.HasPrefix(target, prefix) {
			if pid, e := strconv.Atoi(strings.TrimPrefix(target, prefix)); e == nil && pid > 0 {
				// 容器重启后 PID 会重新分配，光看 PID 存活会误判；必须确认该进程确实是 Chrome。
				if isChromeProcess(pid) {
					return fmt.Errorf("Chrome profile is in use by a live process")
				}
			}
		}
	}
	socket := filepath.Join(dir, "SingletonSocket")
	if c, e := net.DialTimeout("unix", socket, 200*time.Millisecond); e == nil {
		c.Close()
		return fmt.Errorf("Chrome profile socket is active")
	}
	for _, name := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie"} {
		p := filepath.Join(dir, name)
		fi, e := os.Lstat(p)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("refuse removing unexpected profile lock file")
		}
		if e = os.Remove(p); e != nil {
			return e
		}
	}
	// 磁盘 HTTP 缓存会让历史错误响应「复活」，详见 clearProfileCache 的注释。
	// 清理失败不阻断启动 —— 最坏情况只是个别站点读到旧响应，不该因此整轮跑不起来。
	if err := clearProfileCache(dir); err != nil {
		logf("清理浏览器磁盘缓存失败（继续启动）: %v", err)
	}
	return nil
}
