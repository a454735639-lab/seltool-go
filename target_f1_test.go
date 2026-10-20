package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- 任务1: TargetStore YAML 默认目标 / JSON 优先 ---

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

const f1YAMLTargets = `
targets:
  - name: yaml-site
    url: https://yaml.example.com/
    warmupPath: /favicon.ico
`

func TestF1TargetStoreYAMLFallback(t *testing.T) {
	dir := t.TempDir()
	jsonFile := filepath.Join(dir, "targets.json")
	writeFile(t, filepath.Join(dir, "targets.yml"), f1YAMLTargets)

	s := NewTargetStore(jsonFile)
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := s.Get()
	if len(got) != 1 || got[0].Name != "yaml-site" || got[0].URL != "https://yaml.example.com/" {
		t.Fatalf("无 JSON 时应使用 YAML 默认目标, got %+v", got)
	}
	if _, err := os.Stat(jsonFile); !os.IsNotExist(err) {
		t.Fatalf("Load 不应写盘 JSON: %v", err)
	}
}

func TestF1TargetStoreYAMLFallbackViaOption(t *testing.T) {
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "custom-targets.yml")
	writeFile(t, yamlFile, f1YAMLTargets)
	s := NewTargetStore(filepath.Join(dir, "targets.json"), WithYAMLFallback(yamlFile))
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := s.Get(); len(got) != 1 || got[0].Name != "yaml-site" {
		t.Fatalf("显式 YAML 路径未生效: %+v", got)
	}
}

func TestF1TargetStoreInjectedDefaults(t *testing.T) {
	dir := t.TempDir()
	s := NewTargetStore(filepath.Join(dir, "targets.json"),
		WithDefaultTargets([]Target{{Name: "seed", URL: "https://seed.example.com/"}}))
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := s.Get(); len(got) != 1 || got[0].Name != "seed" {
		t.Fatalf("注入的默认目标未生效: %+v", got)
	}
}

func TestF1TargetStoreJSONEmptyArrayWins(t *testing.T) {
	dir := t.TempDir()
	jsonFile := filepath.Join(dir, "targets.json")
	writeFile(t, jsonFile, "[]")
	writeFile(t, filepath.Join(dir, "targets.yml"), f1YAMLTargets)

	s := NewTargetStore(jsonFile)
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := s.Get(); len(got) != 0 {
		t.Fatalf("存在 JSON(空数组) 时应优先且不回填 YAML, got %+v", got)
	}
}

func TestF1TargetStoreLoadKeepsUserData(t *testing.T) {
	dir := t.TempDir()
	jsonFile := filepath.Join(dir, "targets.json")
	writeFile(t, filepath.Join(dir, "targets.yml"), f1YAMLTargets)

	s := NewTargetStore(jsonFile)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save([]Target{{Name: "user", URL: "https://user.example.com/"}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// 文件丢失后再次 Load 不应清空内存中的用户数据。
	if err := os.Remove(jsonFile); err != nil {
		t.Fatal(err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := s.Get(); len(got) != 1 || got[0].Name != "user" {
		t.Fatalf("Load 清空了用户数据: %+v", got)
	}
}

func TestF1TargetStoreConfigure(t *testing.T) {
	dir := t.TempDir()
	tplFile := filepath.Join(dir, "templates.yml")
	writeFile(t, tplFile, f1TemplateYAML)
	tpl := NewTemplateStore(tplFile)
	if err := tpl.Load(); err != nil {
		t.Fatal(err)
	}
	s := NewTargetStore(filepath.Join(dir, "targets.json"))
	s.Configure([]Target{{Name: "seed", URL: "https://seed.example.com/"}}, tpl)
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := s.Get(); len(got) != 1 || got[0].Name != "seed" {
		t.Fatalf("Configure 注入的默认目标未生效: %+v", got)
	}
	if _, err := s.Save([]Target{{TemplateID: "ghost", Name: "g", URL: "https://g.example.com/",
		WarmupPath: "/favicon.ico"}}); err == nil {
		t.Fatal("Configure 注入的模板表未参与校验")
	}
}

// 旧用户 JSON 里的模板目标即使字段与模板不符，Load 也必须成功（只在 Save 时拦截）。
func TestF1LoadDoesNotRejectLegacyData(t *testing.T) {
	dir := t.TempDir()
	jsonFile := filepath.Join(dir, "targets.json")
	writeFile(t, jsonFile, `[{"templateId":"rousi","name":"旧名字","url":"https://rousi.pro/x","warmupPath":"/favicon.ico","localStorage":[{"key":"","value":""}]}]`)
	s, _ := newF1Store(t, jsonFile)
	if err := s.Load(); err != nil {
		t.Fatalf("Load 不应拒绝历史数据: %v", err)
	}
	if got := s.Get(); len(got) != 1 || got[0].Name != "旧名字" {
		t.Fatalf("历史数据未保留: %+v", got)
	}
}

// --- 任务1: 保存时模板校验 ---

const f1TemplateYAML = `
templates:
  - id: rousi
    name: rousi.pro
    url: https://rousi.pro/
    warmupPath: /favicon.ico
    localStorageKeys:
      - token
  - id: plain
    name: plain.example.com
    url: https://plain.example.com/
    warmupPath: /user/login
    localStorageKeys: []
`

func newF1Store(t *testing.T, file string) (*TargetStore, *TemplateStore) {
	t.Helper()
	dir := t.TempDir()
	tplFile := filepath.Join(dir, "templates.yml")
	writeFile(t, tplFile, f1TemplateYAML)
	tpl := NewTemplateStore(tplFile)
	if err := tpl.Load(); err != nil {
		t.Fatalf("模板加载失败: %v", err)
	}
	return NewTargetStore(file, WithTemplates(tpl)), tpl
}

func TestF1SaveRejectsUnknownTemplate(t *testing.T) {
	s, _ := newF1Store(t, filepath.Join(t.TempDir(), "targets.json"))
	_, err := s.Save([]Target{{TemplateID: "nope", Name: "x", URL: "https://x.example.com/", WarmupPath: "/favicon.ico"}})
	if err == nil || !strings.Contains(err.Error(), "模板") {
		t.Fatalf("应拒绝不存在的模板, got %v", err)
	}
}

func TestF1SaveTemplateFieldsFixed(t *testing.T) {
	base := func() Target {
		return Target{
			TemplateID: "rousi", Name: "rousi.pro", URL: "https://rousi.pro/",
			WarmupPath:   "/favicon.ico",
			LocalStorage: []LocalStorage{{Key: "token", Value: "abc"}},
		}
	}
	cases := map[string]func(*Target){
		"name":       func(tg *Target) { tg.Name = "改名" },
		"url":        func(tg *Target) { tg.URL = "https://rousi.pro/login" },
		"warmupPath": func(tg *Target) { tg.WarmupPath = "/other" },
	}
	for name, mutate := range cases {
		s, _ := newF1Store(t, filepath.Join(t.TempDir(), "targets.json"))
		tg := base()
		mutate(&tg)
		if _, err := s.Save([]Target{tg}); err == nil {
			t.Errorf("%s 被修改后应拒绝保存", name)
		}
	}

	s, _ := newF1Store(t, filepath.Join(t.TempDir(), "targets.json"))
	if _, err := s.Save([]Target{base()}); err != nil {
		t.Fatalf("合法模板目标应保存成功: %v", err)
	}
}

func TestF1SaveTemplateLocalStorageKeysExact(t *testing.T) {
	base := func() Target {
		return Target{
			TemplateID: "rousi", Name: "rousi.pro", URL: "https://rousi.pro/",
			WarmupPath:   "/favicon.ico",
			LocalStorage: []LocalStorage{{Key: "token", Value: "abc"}},
		}
	}
	bad := map[string][]LocalStorage{
		"extra":   {{Key: "token", Value: "abc"}, {Key: "other", Value: "x"}},
		"missing": {},
		"renamed": {{Key: "tokens", Value: "abc"}},
		"empty":   {{Key: "token", Value: "   "}},
	}
	for name, ls := range bad {
		s, _ := newF1Store(t, filepath.Join(t.TempDir(), "targets.json"))
		tg := base()
		tg.LocalStorage = ls
		if _, err := s.Save([]Target{tg}); err == nil {
			t.Errorf("localStorage %s 应拒绝保存", name)
		}
	}

	// 空模板（localStorageKeys: []）必须真的为空。
	s, _ := newF1Store(t, filepath.Join(t.TempDir(), "targets.json"))
	if _, err := s.Save([]Target{{TemplateID: "plain", Name: "plain.example.com",
		URL: "https://plain.example.com/", WarmupPath: "/user/login",
		LocalStorage: []LocalStorage{{Key: "a", Value: "b"}}}}); err == nil {
		t.Error("空模板目标不应允许 localStorage 键")
	}
}

func TestF1SaveNormalTargetEmptyLocalStorageKey(t *testing.T) {
	s := NewTargetStore(filepath.Join(t.TempDir(), "targets.json"))
	_, err := s.Save([]Target{{Name: "n", URL: "https://example.com/", WarmupPath: "/favicon.ico",
		LocalStorage: []LocalStorage{{Key: "  ", Value: "v"}}}})
	if err == nil {
		t.Fatal("普通目标 localStorage 空 key 应拒绝")
	}
	if _, err := s.Save([]Target{{Name: "n", URL: "https://example.com/", WarmupPath: "/favicon.ico",
		LocalStorage: []LocalStorage{{Key: "k", Value: "v"}}}}); err != nil {
		t.Fatalf("合法普通目标应保存: %v", err)
	}
}

// --- 任务2: 导入保留 www 与真实 host ---

func TestF1DetectPTSitesKeepsWWW(t *testing.T) {
	data := map[string][]Cookie{
		".ubits.club":        {},
		"ubits.club":         {},
		"www.hdtime.org":     {},
		".www.hdtime.org":    {},
		"www.m-team.cc":      {},
		"pt.btschool.club":   {},
		"ptlogin2.qq.com":    {},
		"hd.ghzyj.sh.gov.cn": {},
		".baidu.com":         {},
	}
	got := detectPTSites(data)
	want := map[string]bool{
		"ubits.club":       true,
		"www.hdtime.org":   true,
		"www.m-team.cc":    true,
		"pt.btschool.club": true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want keys %v", got, want)
	}
	for _, d := range got {
		if !want[d] {
			t.Errorf("域名不应被改写或误判: %s (got %v)", d, got)
		}
	}
}

func TestF1ImportKeepsWWWAndApexSeparate(t *testing.T) {
	// 行为已改为 MoviePilot 模式：www 与 apex 归为同一注册域名，
	// 只保留一条，从根源上杜绝重复目标。Cookie 注入侧由 buildAllowedDomains
	// 逐级匹配父域，合并后仍能取到两个域的 Cookie。
	domains := []string{"www.hdtime.org", "hdtime.org", "www.hdtime.org"}
	added, skipped := BuildImportTargets(domains, nil, nil)
	if len(added) != 1 {
		t.Fatalf("同注册域名只应保留一条, got %d: %+v", len(added), added)
	}
	if skipped != 2 {
		t.Errorf("应跳过 2 条重复, got %d", skipped)
	}
	if added[0].URL != "https://www.hdtime.org/" {
		t.Errorf("应保留首次出现的写法, got %s", added[0].URL)
	}
}

func TestF1IsPTSiteMatchesWithoutWWW(t *testing.T) {
	if !isPTSite("www.m-team.cc") {
		t.Error("www 前缀应仍能命中内置表")
	}
	if !isPTSite("kp.m-team.cc") {
		t.Error("子域应仍能命中内置表")
	}
	if isPTSite("www.baidu.com") {
		t.Error("非 PT 站不应命中")
	}
}

// --- 任务2: 手动输入严格解析 ---

func TestF1ParseManualTargetsKeepsComponents(t *testing.T) {
	got := ParseManualTargets("https://hdtime.org/a/b?x=1&y=2#frag")
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	if got[0].URL != "https://hdtime.org/a/b?x=1&y=2#frag" {
		t.Fatalf("scheme/path/query/fragment 应保留: %s", got[0].URL)
	}
	// 名字改为站点表里的中文名（见 siteDisplayName）；URL 组件仍须原样保留。
	// 未注入站点表时回落 host。
	want := siteDisplayName("hdtime.org")
	if got[0].Name != want {
		t.Fatalf("name 应为站点显示名 %q, got %q", want, got[0].Name)
	}
}

func TestF1ParseManualTargetsRejects(t *testing.T) {
	bad := []string{
		"https://user:pass@hdtime.org/",
		"https://hdtime.org:99999/",
		"https://hdtime.org:abc/",
		"https://hd\x00time.org/",
		"https:///nohost",
		"javascript:alert(1)",
		"ftp://hdtime.org/",
	}
	for _, in := range bad {
		if got := ParseManualTargets(in); len(got) != 0 {
			t.Errorf("%q 应被拒绝, got %+v", in, got)
		}
	}
}

// TestF1ImportDedupesTrailingSlash 验证「同一 URL 仅末尾斜杠不同」不会重复导入，
// 而 www 与 apex 仍各自保留。
func TestF1ImportDedupesTrailingSlash(t *testing.T) {
	existing := []Target{{URL: "https://www.hddolby.com"}}
	added, skipped := BuildImportTargets([]string{"www.hddolby.com"}, existing, nil)
	if len(added) != 0 || skipped != 1 {
		t.Fatalf("末尾斜杠差异应判为已存在, got added=%d skipped=%d", len(added), skipped)
	}

	// 已有 apex，再导入 www 应被拒（归为同一注册域名）
	existing2 := []Target{{URL: "https://hdtime.org/"}}
	added2, skipped2 := BuildImportTargets([]string{"www.hdtime.org"}, existing2, nil)
	if len(added2) != 0 || skipped2 != 1 {
		t.Fatalf("同注册域名应判为已存在, got added=%d skipped=%d", len(added2), skipped2)
	}
}

// TestF1SiteDisplayNamePrefersChinese 验证中文名优先，未收录的站点回落到域名。
// 站点表由 PTSiteTable 提供（内置 assets/pt-sites.json，可被面板覆盖），
// 因此测试前需要注入表实例。
func TestF1SiteDisplayNamePrefersChinese(t *testing.T) {
	prev := ptSiteTable
	ptSiteTable = NewPTSiteTable(filepath.Join(t.TempDir(), "pt-sites.json"))
	defer func() { ptSiteTable = prev }()

	if got := siteDisplayName("www.hddolby.com"); got != "高清杜比" {
		t.Errorf("应取站点表里的中文名, got %s", got)
	}
	if got := siteDisplayName("unknown-site.example"); got != "unknown-site.example" {
		t.Errorf("未收录应回落域名, got %s", got)
	}
}

// TestF1PTSiteTableUpdate 验证面板提交的新表能生效并可回退内置表。
func TestF1PTSiteTableUpdate(t *testing.T) {
	tbl := NewPTSiteTable(filepath.Join(t.TempDir(), "pt-sites.json"))

	// 内置表应能查到已知站点
	if got := tbl.Lookup("www.hddolby.com"); got != "高清杜比" {
		t.Errorf("内置表应含 hddolby, got %q", got)
	}

	// 提交自定义表（直接映射格式）
	ver, n, err := tbl.Replace([]byte(`{"newsite.example":"新站"}`))
	if err != nil {
		t.Fatalf("替换失败: %v", err)
	}
	if n != 1 || ver != "自定义" {
		t.Errorf("替换结果异常: ver=%s n=%d", ver, n)
	}
	if got := tbl.Lookup("newsite.example"); got != "新站" {
		t.Errorf("自定义表应生效, got %q", got)
	}
	if got := tbl.Lookup("www.hddolby.com"); got != "" {
		t.Errorf("替换后不应再有旧条目, got %q", got)
	}

	// 回退内置
	if err := tbl.Reset(); err != nil {
		t.Fatalf("重置失败: %v", err)
	}
	if got := tbl.Lookup("www.hddolby.com"); got != "高清杜比" {
		t.Errorf("重置后应回退内置表, got %q", got)
	}

	// 非法输入应被拒
	if _, _, err := tbl.Replace([]byte(`not-json`)); err == nil {
		t.Error("非法 JSON 应报错")
	}
	if _, _, err := tbl.Replace([]byte(`{}`)); err == nil {
		t.Error("空表应报错")
	}
}

// TestF1RegisteredDomain 验证注册域名提取规则（对齐 MoviePilot extract_domain）。
func TestF1RegisteredDomain(t *testing.T) {
	cases := map[string]string{
		"www.hddolby.com":         "hddolby.com",
		"hddolby.com":             "hddolby.com",
		"https://www.hdtime.org/": "hdtime.org",
		".hddolby.com":            "hddolby.com",
		"kp.m-team.cc":            "m-team.cc",
		"u2.dmhy.org":             "u2.dmhy.org",    // 特例白名单
		"pt.gtkpw.xyz":            "pt.gtkpw.xyz",   // 特例白名单
		"pt.ecust.pp.ua":          "pt.ecust.pp.ua", // 特例白名单
		"www.pttime.org":          "pttime.org",
		"gamegamept.com":          "gamegamept.com",
		"a.b.c.d.example.com":     "a.b.c.d.example.com", // 超过三段原样保留
	}
	for in, want := range cases {
		if got := registeredDomain(in); got != want {
			t.Errorf("registeredDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestF1ImportDedupesByRegisteredDomain 验证 www 与 apex 归为同一站，不会重复导入。
func TestF1ImportDedupesByRegisteredDomain(t *testing.T) {
	// 已有 www 形式，再导入 apex 应被拒
	existing := []Target{{URL: "https://www.hddolby.com/"}}
	added, skipped := BuildImportTargets([]string{"hddolby.com"}, existing, nil)
	if len(added) != 0 || skipped != 1 {
		t.Fatalf("同注册域名应判为已存在, got added=%d skipped=%d", len(added), skipped)
	}

	// 一次导入 www 与 apex，只应加入一个
	added2, _ := BuildImportTargets([]string{"www.hdtime.org", "hdtime.org"}, nil, nil)
	if len(added2) != 1 {
		t.Fatalf("同注册域名只应加入一个, got %d: %+v", len(added2), added2)
	}

	// 不同站各自保留
	added3, _ := BuildImportTargets([]string{"hddolby.com", "hdtime.org", "pttime.org"}, nil, nil)
	if len(added3) != 3 {
		t.Fatalf("不同站应各自保留, got %d", len(added3))
	}

	// 特例白名单站点不应被误合并
	added4, _ := BuildImportTargets([]string{"u2.dmhy.org", "dmhy.org"}, nil, nil)
	if len(added4) != 2 {
		t.Fatalf("特例站与父域应各自保留, got %d: %+v", len(added4), added4)
	}
}
