package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	f1Key      = "KEY-should-never-appear"
	f1Password = "PASSWORD-should-never-appear"
	f1Secret   = "COOKIEVALUE-should-never-appear"
)

// newF1CookieCloud 起一个返回固定状态码/正文的 mock 服务，并返回受控的 CookieCloud。
func newF1CookieCloud(t *testing.T, status int, body string, cacheFile string) (*CookieCloud, *httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	cc := NewCookieCloud(CookieCloudConfig{
		URL: srv.URL, Key: f1Key, Password: f1Password,
		CacheFile: cacheFile, AllowCacheFallback: true,
	}, NewAlertState())
	return cc, srv, &hits
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	oldOut, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldOut)
		log.SetFlags(oldFlags)
	})
	return &buf
}

func assertNoSecret(t *testing.T, where, text string) {
	t.Helper()
	for _, secret := range []string{f1Key, f1Password, f1Secret} {
		if strings.Contains(text, secret) {
			t.Fatalf("%s 泄漏敏感内容 %q: %s", where, secret, text)
		}
	}
	if strings.Contains(text, "/cookiecloud/get/") {
		t.Fatalf("%s 泄漏含 key 的完整路径: %s", where, text)
	}
}

// Probe 必须只打远端：不读缓存、不写缓存、不产生启动告警。
func TestF1ProbeIgnoresCacheAndAlerts(t *testing.T) {
	dir := t.TempDir()
	cacheFile := filepath.Join(dir, "cache.json")
	cached := `{"cookie_data":{"cached.example.com":[{"name":"sid","value":"` + f1Secret + `"}]}}`
	if err := os.WriteFile(cacheFile, []byte(cached), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(cacheFile)
	if err != nil {
		t.Fatal(err)
	}

	logs := captureLog(t)
	cc, _, _ := newF1CookieCloud(t, http.StatusServiceUnavailable, "boom", cacheFile)

	domains, cookies, reachable, err := cc.Probe()
	if err == nil {
		t.Fatal("503 时 Probe 必须报错，不能回退缓存冒充成功")
	}
	if !reachable {
		t.Fatal("503 说明服务端可达，reachable 应为 true")
	}
	if domains != 0 || cookies != 0 {
		t.Fatalf("失败时不应有计数: %d/%d", domains, cookies)
	}

	after, statErr := os.Stat(cacheFile)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatal("Probe 不应改写缓存文件")
	}
	if got, readErr := os.ReadFile(cacheFile); readErr != nil || string(got) != cached {
		t.Fatalf("缓存内容被改动: %v", readErr)
	}
	if w := cc.alerts.StartupWarningText(); w != "" {
		t.Fatalf("Probe 不应产生启动告警: %s", w)
	}
	assertNoSecret(t, "Probe 日志", logs.String())
	assertNoSecret(t, "Probe 错误", err.Error())
}

func TestF1ProbeDoesNotWriteCacheOnSuccess(t *testing.T) {
	dir := t.TempDir()
	cacheFile := filepath.Join(dir, "cache.json")
	body := `{"cookie_data":{"a.example.com":[{"name":"sid","value":"` + f1Secret + `"}]}}`
	cc, _, _ := newF1CookieCloud(t, http.StatusOK, body, cacheFile)

	domains, cookies, reachable, err := cc.Probe()
	if err != nil || !reachable {
		t.Fatalf("合法响应应成功: reachable=%v err=%v", reachable, err)
	}
	if domains != 1 || cookies != 1 {
		t.Fatalf("计数不对: %d/%d", domains, cookies)
	}
	if _, err := os.Stat(cacheFile); !os.IsNotExist(err) {
		t.Fatalf("Probe 不应写缓存: %v", err)
	}
}

func TestF1ProbeErrorPaths(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		reachable  bool
		wantSubstr string
	}{
		{"503", http.StatusServiceUnavailable, "maintenance", true, "503"},
		{"404", http.StatusNotFound, `{"error":"not found"}`, true, "404"},
		{"错误密码500", http.StatusInternalServerError, `{"error":"bad password"}`, true, "500"},
		{"200非法JSON", http.StatusOK, "<html>login</html>", true, "JSON"},
		{"200无cookie_data", http.StatusOK, `{"other":1}`, true, "cookie_data"},
		{"200cookie_data为null", http.StatusOK, `{"cookie_data":null}`, true, "cookie_data"},
		{"200cookie_data非对象", http.StatusOK, `{"cookie_data":[1,2]}`, true, "cookie_data"},
		{"200空正文", http.StatusOK, "", true, "空"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			cc, _, _ := newF1CookieCloud(t, tc.status, tc.body, filepath.Join(t.TempDir(), "cache.json"))
			domains, cookies, reachable, err := cc.Probe()
			if err == nil {
				t.Fatalf("%s 必须报错，不能当作连接成功", tc.name)
			}
			if reachable != tc.reachable {
				t.Fatalf("reachable=%v want %v (err=%v)", reachable, tc.reachable, err)
			}
			if domains != 0 || cookies != 0 {
				t.Fatalf("失败时不应有计数: %d/%d", domains, cookies)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("错误信息应包含 %q，实际 %q", tc.wantSubstr, err.Error())
			}
			assertNoSecret(t, "错误信息", err.Error())
			assertNoSecret(t, "日志", logs.String())
		})
	}
}

// 404 曾经被当成「连接成功、只是没数据」，这里明确它必须报错。
func TestF1Probe404IsNotSuccess(t *testing.T) {
	cc, _, _ := newF1CookieCloud(t, http.StatusNotFound, `{}`, filepath.Join(t.TempDir(), "cache.json"))
	_, _, reachable, err := cc.Probe()
	if err == nil {
		t.Fatal("404 不能解释为连接成功无数据")
	}
	if !reachable {
		t.Fatal("404 仍说明服务端可达")
	}
	var se *HTTPStatusError
	if !errors.As(err, &se) || se.Code != 404 {
		t.Fatalf("应返回 HTTPStatusError(404): %v", err)
	}
	if se.Body != "" {
		t.Fatalf("HTTPStatusError 不应保存响应正文: %q", se.Body)
	}
}

func TestF1ProbeEmptyCookieDataSucceeds(t *testing.T) {
	cc, _, _ := newF1CookieCloud(t, http.StatusOK, `{"cookie_data":{}}`, filepath.Join(t.TempDir(), "cache.json"))
	domains, cookies, reachable, err := cc.Probe()
	if err != nil || !reachable {
		t.Fatalf("空 cookie_data 对象应成功: %v", err)
	}
	if domains != 0 || cookies != 0 {
		t.Fatalf("空数据计数应为 0: %d/%d", domains, cookies)
	}
}

func TestF1ProbeTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"cookie_data":{}}`))
	}))
	defer srv.Close()

	logs := captureLog(t)
	cc := NewCookieCloud(CookieCloudConfig{
		URL: srv.URL, Key: f1Key, Password: f1Password,
		CacheFile: filepath.Join(t.TempDir(), "cache.json"), AllowCacheFallback: true,
	}, NewAlertState())
	cc.client = &http.Client{Timeout: 50 * time.Millisecond}

	_, _, reachable, err := cc.Probe()
	if err == nil {
		t.Fatal("超时必须报错")
	}
	if reachable {
		t.Fatal("超时说明没拿到响应，reachable 应为 false")
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Fatalf("错误信息应说明超时: %v", err)
	}
	assertNoSecret(t, "超时错误", err.Error())
	assertNoSecret(t, "超时日志", logs.String())
}

// 运行任务（FetchAllCookies）仍按 allowCacheFallback 回退。
func TestF1FetchAllCookiesStillFallsBack(t *testing.T) {
	dir := t.TempDir()
	cacheFile := filepath.Join(dir, "cache.json")
	cached := `{"cookie_data":{"cached.example.com":[{"name":"sid","value":"v1"}]}}`
	if err := os.WriteFile(cacheFile, []byte(cached), 0o600); err != nil {
		t.Fatal(err)
	}

	cc, _, _ := newF1CookieCloud(t, http.StatusServiceUnavailable, "down", cacheFile)
	data, err := cc.FetchAllCookies()
	if err != nil {
		t.Fatalf("allow-cache-fallback=true 应回退缓存: %v", err)
	}
	if len(data["cached.example.com"]) != 1 {
		t.Fatalf("回退数据不对: %+v", data)
	}

	cfg := cc.Config()
	cfg.AllowCacheFallback = false
	cc.Update(cfg)
	if _, err := cc.FetchAllCookies(); err == nil {
		t.Fatal("allow-cache-fallback=false 时必须报错")
	}
}

func TestF1FetchAllCookiesRejectsBadPayloadWithoutCache(t *testing.T) {
	logs := captureLog(t)
	cc, _, _ := newF1CookieCloud(t, http.StatusOK, `{"nope":true}`, filepath.Join(t.TempDir(), "cache.json"))
	// allow-cache-fallback=true 且无缓存时按设计继续（空 Cookie），所以这里关掉回退再验证报错。
	cfg := cc.Config()
	cfg.AllowCacheFallback = false
	cc.Update(cfg)
	if _, err := cc.FetchAllCookies(); err == nil {
		t.Fatal("缺 cookie_data 且不允许回退时运行任务必须报错")
	}
	assertNoSecret(t, "日志", logs.String())
}

func TestF1NoSecretInSuccessLogs(t *testing.T) {
	logs := captureLog(t)
	body := `{"cookie_data":{"a.example.com":[{"name":"sid","value":"` + f1Secret + `"}]}}`
	cc, _, _ := newF1CookieCloud(t, http.StatusOK, body, filepath.Join(t.TempDir(), "cache.json"))
	if _, err := cc.FetchAllCookies(); err != nil {
		t.Fatalf("获取失败: %v", err)
	}
	assertNoSecret(t, "成功日志", logs.String())
}

// ProbeWith：用请求里的新配置测试，不落盘、不影响现有实例。
func TestF1ProbeWithRequestConfig(t *testing.T) {
	okBody := `{"cookie_data":{"a.example.com":[{"name":"sid","value":"` + f1Secret + `"}]}}`
	good, _, _ := newF1CookieCloud(t, http.StatusOK, okBody, filepath.Join(t.TempDir(), "cache.json"))
	bad, _, _ := newF1CookieCloud(t, http.StatusInternalServerError, "bad", filepath.Join(t.TempDir(), "cache.json"))

	original := bad.Config()
	domains, cookies, reachable, err := bad.ProbeWith(good.Config())
	if err != nil || !reachable {
		t.Fatalf("应按传入配置访问远端: %v", err)
	}
	if domains != 1 || cookies != 1 {
		t.Fatalf("计数不对: %d/%d", domains, cookies)
	}
	if bad.Config() != original {
		t.Fatal("ProbeWith 不应修改实例自身配置")
	}
	// 传入坏配置时必须失败，不能沿用实例自身的好配置。
	if _, _, _, err := bad.ProbeWith(bad.Config()); err == nil {
		t.Fatal("传入 500 配置时应报错")
	}
}

func TestF1HostOnlyParsing(t *testing.T) {
	cases := []struct {
		name string
		node map[string]any
		want bool
	}{
		{"显式true", map[string]any{"name": "a", "value": "1", "domain": "x.com", "hostOnly": true}, true},
		{"显式false", map[string]any{"name": "a", "value": "1", "domain": "x.com", "hostOnly": false}, false},
		{"缺省", map[string]any{"name": "a", "value": "1", "domain": "x.com"}, false},
		{"字符串不误判", map[string]any{"name": "a", "value": "1", "domain": "x.com", "hostOnly": "true"}, false},
		{"无domain不带点", map[string]any{"name": "a", "value": "1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ck, ok := toCookie(tc.node, "fallback.com")
			if !ok {
				t.Fatal("应解析成功")
			}
			if ck.HostOnly != tc.want {
				t.Fatalf("HostOnly=%v want %v", ck.HostOnly, tc.want)
			}
		})
	}

	// JSON 字段名必须是 hostOnly，且缺省序列化为 false。
	raw, err := json.Marshal(Cookie{Name: "a", Value: "1", Domain: "x.com", HostOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"hostOnly":true`) {
		t.Fatalf("JSON 字段名不对: %s", raw)
	}
}

// ---- settings ----

func TestF1SettingsEmptyURLDisablesAndSurvivesRestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "cookiecloud-settings.json")
	s := NewSettingsStore(file)
	if _, ok, err := s.Load(); err != nil || ok {
		t.Fatalf("文件不存在时应返回 ok=false: ok=%v err=%v", ok, err)
	}

	if _, err := s.Save(CookieCloudSettings{URL: "https://cc.example.com", Key: "k", Password: "p"}); err != nil {
		t.Fatal(err)
	}
	// 用户清空地址 = 禁用；空密码沿用已有值。
	if _, err := s.Save(CookieCloudSettings{URL: "", Key: "", Password: ""}); err != nil {
		t.Fatal(err)
	}

	reloaded := NewSettingsStore(file)
	saved, ok, err := reloaded.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("设置文件存在时 Load 必须返回 ok=true，否则重启会回退到 yaml/env 重新启用")
	}
	if saved.URL != "" {
		t.Fatalf("空地址应保持禁用，实际 %q", saved.URL)
	}
	if saved.Password != "p" {
		t.Fatalf("空密码应沿用已有值，实际 %q", saved.Password)
	}
}

func TestF1SettingsSaveValidation(t *testing.T) {
	s := NewSettingsStore(filepath.Join(t.TempDir(), "s.json"))
	if _, err := s.Save(CookieCloudSettings{URL: "ftp://x"}); err == nil {
		t.Fatal("非 http(s) 地址应被拒绝")
	}
	if _, err := s.Save(CookieCloudSettings{URL: "https://cc.example.com/"}); err != nil {
		t.Fatal(err)
	}
	if got := s.Current().URL; got != "https://cc.example.com" {
		t.Fatalf("尾部斜杠应被裁剪: %q", got)
	}
}

func TestF1SettingsReloadAppliesNewValues(t *testing.T) {
	file := filepath.Join(t.TempDir(), "s.json")
	if err := writeJSONAtomic(file, CookieCloudSettings{URL: "https://a.example.com", Key: "k1", Password: "p1"}, 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewSettingsStore(file)
	got, ok, err := s.Load()
	if err != nil || !ok {
		t.Fatalf("加载失败: ok=%v err=%v", ok, err)
	}
	if got.URL != "https://a.example.com" || got.Key != "k1" || got.Password != "p1" {
		t.Fatalf("加载内容不对: %+v", got)
	}
}
