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
	"syscall"
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
	f, err := os.OpenFile(filepath.Join(dir, ".seltool-owner.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("browser profile already owned")
	}
	var once sync.Once
	release := func() { once.Do(func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }) }
	if err = prepareProfile(dir); err != nil {
		release()
		return nil, err
	}
	return release, nil
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
	return nil
}
