package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildAllowedDomains(t *testing.T) {
	got := buildAllowedDomains("kp.m-team.cc")
	want := []string{"kp.m-team.cc", "m-team.cc"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestDomainMatches(t *testing.T) {
	allowed := buildAllowedDomains("kp.m-team.cc")
	cases := map[string]bool{
		"kp.m-team.cc": true,
		"m-team.cc":    true,
		".m-team.cc":   true, // normalizeDomain 会去点
		"evil.com":     false,
		"notm-team.cc": false,
	}
	for domain, want := range cases {
		if got := domainMatches(normalizeDomain(domain), allowed); got != want {
			t.Errorf("domainMatches(%q) = %v, want %v", domain, got, want)
		}
	}
}

func TestFilterForTargetPrefersConfiguredDomain(t *testing.T) {
	data := map[string][]Cookie{
		"m-team.cc":      {{Name: "a", Value: "1", Domain: "m-team.cc"}},
		"kp.m-team.cc":   {{Name: "b", Value: "2", Domain: "kp.m-team.cc"}},
		"other-site.com": {{Name: "c", Value: "3", Domain: "other-site.com"}},
	}
	cc := NewCookieCloud(CookieCloudConfig{}, NewAlertState())

	got := cc.FilterForTarget(data, Target{URL: "https://kp.m-team.cc/"})
	if len(got) != 2 {
		t.Fatalf("按 URL host 过滤应得 2 个 cookie，实际 %d: %+v", len(got), got)
	}

	got = cc.FilterForTarget(data, Target{URL: "https://kp.m-team.cc/", CookieDomain: "other-site.com"})
	if len(got) != 1 || got[0].Name != "c" {
		t.Fatalf("按配置域名过滤应只命中 other-site，实际 %+v", got)
	}
}

func TestToCookieDefaults(t *testing.T) {
	ck, ok := toCookie(map[string]any{
		"name":   "session",
		"value":  "v",
		"secure": true,
	}, "example.com")
	if !ok {
		t.Fatal("应解析成功")
	}
	if ck.Path != "/" || ck.Domain != "example.com" || !ck.Secure {
		t.Fatalf("默认值不正确: %+v", ck)
	}
	if _, ok := toCookie(map[string]any{"name": "x"}, "d"); ok {
		t.Fatal("缺少 value 应解析失败")
	}
}

func TestCookieCloudFetchAndCache(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/cookiecloud/get/testkey" {
			t.Errorf("路径不对: %s", r.URL.Path)
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["password"] != "secret" {
			t.Errorf("password 未正确传递: %v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"cookie_data": map[string]any{
				"example.com": []any{
					map[string]any{"name": "sid", "value": "42", "domain": "example.com", "path": "/"},
				},
			},
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	cc := NewCookieCloud(CookieCloudConfig{
		URL: srv.URL, Key: "testkey", Password: "secret",
		CacheFile: filepath.Join(dir, "cache.json"), AllowCacheFallback: true,
	}, NewAlertState())

	data, err := cc.FetchAllCookies()
	if err != nil {
		t.Fatalf("获取失败: %v", err)
	}
	if len(data["example.com"]) != 1 || data["example.com"][0].Name != "sid" {
		t.Fatalf("解析结果不对: %+v", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "cache.json")); err != nil {
		t.Fatalf("缓存文件未写入: %v", err)
	}

	// 服务不可达时应回退缓存。
	srv.Close()
	data, err = cc.FetchAllCookies()
	if err != nil {
		t.Fatalf("回退缓存失败: %v", err)
	}
	if len(data["example.com"]) != 1 {
		t.Fatalf("回退数据不对: %+v", data)
	}
}

func TestHistoryTrim(t *testing.T) {
	h := NewHistory(filepath.Join(t.TempDir(), "history.json"))
	for i := 0; i < 10; i++ {
		h.Record("https://a.com/", i%2 == 0, "run")
	}
	got := h.Get("https://a.com/")
	if len(got) != maxHistorySize {
		t.Fatalf("历史应保留 %d 条，实际 %d", maxHistorySize, len(got))
	}
	if latest := h.Latest()["https://a.com/"]; latest.Message != "run" {
		t.Fatalf("latest 不正确: %+v", latest)
	}
}

func TestConfigMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.yml")
	content := `
automation:
  schedule:
    cron: "0 30 9 * * *"
  browser:
    page-stay-seconds: 9
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.Automation.Schedule.Cron != "0 30 9 * * *" {
		t.Errorf("cron 未覆盖: %s", cfg.Automation.Schedule.Cron)
	}
	if cfg.Automation.Browser.PageStaySeconds != 9 {
		t.Errorf("page-stay-seconds 未覆盖: %d", cfg.Automation.Browser.PageStaySeconds)
	}
	// 未在文件中出现的键必须保留默认值。
	if cfg.Automation.Schedule.Zone != "Asia/Shanghai" {
		t.Errorf("zone 默认值丢失: %s", cfg.Automation.Schedule.Zone)
	}
	if cfg.Automation.Browser.PageLoadTimeout.Std().Minutes() != 2 {
		t.Errorf("page-load-timeout 默认值丢失: %v", cfg.Automation.Browser.PageLoadTimeout.Std())
	}
	if !cfg.Automation.CookieCloud.AllowCacheFallback {
		t.Error("allow-cache-fallback 默认值丢失")
	}
}

func TestTargetStoreSaveLoad(t *testing.T) {
	file := filepath.Join(t.TempDir(), "targets.json")
	s := NewTargetStore(file)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Save([]Target{{Name: "n", URL: "https://example.com/", WarmupPath: "/favicon.ico"}})
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("保存结果不对: %+v", saved)
	}
	reloaded := NewTargetStore(file)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Get()) != 1 || reloaded.Get()[0].Name != "n" {
		t.Fatalf("重新加载失败: %+v", reloaded.Get())
	}
	if _, err := s.Save([]Target{{URL: "javascript:alert(1)"}}); err == nil {
		t.Fatal("非 http(s) 目标应被拒绝")
	}
}

func TestAccountBcryptAndNoPlaintext(t *testing.T) {
	file := filepath.Join(t.TempDir(), "user.json")
	a := NewAccountStore(file)
	if a.HasUser() {
		t.Fatal("初始不应有用户")
	}
	if err := a.Register("admin", "__REDACTED__"); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || containsAny(string(raw), "__REDACTED__") {
		t.Fatalf("密码不应明文落盘: %s", raw)
	}
	if err := a.Register("other", "__REDACTED__"); err == nil {
		t.Fatal("重复注册应失败")
	}
	if !a.Authenticate("admin", "__REDACTED__") {
		t.Fatal("正确密码应通过")
	}
	if a.Authenticate("admin", "wrong") {
		t.Fatal("错误密码不应通过")
	}
	if a.Authenticate("nobody", "__REDACTED__") {
		t.Fatal("不存在的用户不应通过")
	}
}

func TestEndpointForms(t *testing.T) {
	cases := map[string]string{
		"https://movie-pilot.org":              "https://movie-pilot.org/cookiecloud/get/k1",
		"https://movie-pilot.org/":             "https://movie-pilot.org/cookiecloud/get/k1",
		"https://movie-pilot.org/cookiecloud":  "https://movie-pilot.org/cookiecloud/get/k1",
		"https://movie-pilot.org/cookiecloud/": "https://movie-pilot.org/cookiecloud/get/k1",
		"http://10.0.0.4:18088/apps":       "http://10.0.0.4:18088/apps/cookiecloud/get/k1",
	}
	for in, want := range cases {
		cc := NewCookieCloud(CookieCloudConfig{URL: in, Key: "k1"}, NewAlertState())
		got, err := cc.endpoint()
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got != want {
			t.Errorf("%s => %s, want %s", in, got, want)
		}
	}
}

func TestDetectPTSites(t *testing.T) {
	data := map[string][]Cookie{
		".ubits.club":        {},
		"ubits.club":         {},
		"www.hdtime.org":     {},
		"pt.btschool.club":   {},
		"ptlogin2.qq.com":    {},
		"hd.ghzyj.sh.gov.cn": {},
		".baidu.com":         {},
	}
	got := detectPTSites(data)
	want := map[string]bool{"ubits.club": true, "www.hdtime.org": true, "pt.btschool.club": true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, d := range got {
		if !want[d] {
			t.Errorf("不应识别为 PT: %s", d)
		}
	}
}

func TestParseManualTargets(t *testing.T) {
	got := ParseManualTargets("hdtime.org\nhttps://hddolby.com/\n  \nourbits.club, http://pt.x.com/a")
	if len(got) != 4 {
		t.Fatalf("解析到 %d 条，期望 4: %+v", len(got), got)
	}
	if got[1].URL != "https://hddolby.com/" {
		t.Errorf("URL 未保留原样: %s", got[1].URL)
	}
	if got[3].URL != "http://pt.x.com/a" {
		t.Errorf("http 路径应保留: %s", got[3].URL)
	}
}

func TestMergeTargets(t *testing.T) {
	existing := []Target{{URL: "https://a.com/"}}
	added, skipped := mergeTargets([]Target{{URL: "https://a.com"}, {URL: "https://b.com/"}}, existing)
	if len(added) != 1 || added[0].URL != "https://b.com/" {
		t.Fatalf("added=%+v", added)
	}
	if skipped != 1 {
		t.Fatalf("skipped=%d", skipped)
	}
}
