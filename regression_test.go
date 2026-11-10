package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// 同域不同路径批量导入时，第二个 URL 曾经用「新列表下标」去索引旧列表，
// 直接 panic（index out of range）。这里锁住修复后的行为：同主机只保留一条。
func TestMergeTargetsOverwriteSameHostKeepsOne(t *testing.T) {
	existing := []Target{{URL: "https://example.com/"}}
	incoming := []Target{
		{URL: "https://hdtime.org/", Name: "A"},
		{URL: "https://hdtime.org/attendance.php", Name: "B"},
	}

	added, updated, skipped := mergeTargetsOverwrite(incoming, existing, true)
	if len(added) != 1 {
		t.Fatalf("同一主机应只新增一条，实际 %d 条: %+v", len(added), added)
	}
	if updated != 1 {
		t.Fatalf("勾选覆盖时应把新名称写到待新增的那条上，updated=%d", updated)
	}
	if skipped != 0 {
		t.Fatalf("命中待新增条目不算跳过，skipped=%d", skipped)
	}
	if added[0].Name != "B" {
		t.Fatalf("后一条的名称应覆盖前一条，实际 %q", added[0].Name)
	}

	// 不勾选覆盖时同样不能 panic，且应计为跳过。
	added2, updated2, skipped2 := mergeTargetsOverwrite(incoming, existing, false)
	if len(added2) != 1 || updated2 != 0 || skipped2 != 1 {
		t.Fatalf("未勾选覆盖时结果不对: added=%d updated=%d skipped=%d", len(added2), updated2, skipped2)
	}
}

// 上限校验曾经对每个目标单独调用，切片长度恒为 1，500 个的上限永远不生效。
func TestSaveRejectsOverLimitTargets(t *testing.T) {
	store := NewTargetStore(filepath.Join(t.TempDir(), "targets.json"))
	targets := make([]Target, maxTargets+1)
	for i := range targets {
		targets[i] = Target{URL: fmt.Sprintf("https://s%d.example%d.com/", i, i)}
	}
	if _, err := store.Save(targets); err == nil {
		t.Fatal("超过上限时应拒绝保存")
	} else if !strings.Contains(err.Error(), "上限") {
		t.Fatalf("错误信息应说明超出上限，实际: %v", err)
	}
}

// 报错序号要指向整张表里的真实位置，而不是恒为「第 1 个」。
func TestSaveReportsRealTargetIndex(t *testing.T) {
	store := NewTargetStore(filepath.Join(t.TempDir(), "targets.json"))
	targets := make([]Target, 0, 5)
	for i := 0; i < 4; i++ {
		targets = append(targets, Target{URL: fmt.Sprintf("https://ok%d.example%d.com/", i, i)})
	}
	targets = append(targets, Target{URL: "ftp://bad.example.com/"})

	_, err := store.Save(targets)
	if err == nil {
		t.Fatal("非法协议应被拒绝")
	}
	if !strings.Contains(err.Error(), "第 5 个") {
		t.Fatalf("错误信息应指向第 5 个目标，实际: %v", err)
	}
}

// 单行超过字节上限时必须硬切，否则切出的块超出企微 2048 字节限制，整条通知被拒。
func TestSplitByBytesHardCutsLongLine(t *testing.T) {
	long := strings.Repeat("错", 900) // 2700 字节，单行
	chunks := splitByBytes(long, 2000)
	if len(chunks) < 2 {
		t.Fatalf("超长单行应被切成多块，实际 %d 块", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 2000 {
			t.Fatalf("第 %d 块 %d 字节，超过上限", i+1, len(c))
		}
	}
	if got := strings.Join(chunks, ""); got != long {
		t.Fatalf("切分后内容应可无损还原，实际长度 %d / %d", len(got), len(long))
	}
}

// 常规多行输入仍应按行切分，不要把正常换行也当成硬切点。
func TestSplitByBytesKeepsLines(t *testing.T) {
	input := "第一行\n第二行\n第三行"
	chunks := splitByBytes(input, 2000)
	if len(chunks) != 1 || chunks[0] != input {
		t.Fatalf("未超限时不应切分: %#v", chunks)
	}
}

// 同批导入 apex 与 www 时，两者曾双双入库：合并阶段只按 host 判重，
// 而保存校验（findDuplicateTarget）按注册域名判重，于是整批被拒，
// 用户看到「高校 与 高校 是同一站点（hdupt.com）」，却分不清该取消哪个。
func TestMergeTargetsOverwriteApexAndWWWKeepsOne(t *testing.T) {
	incoming := []Target{
		{URL: "https://hdupt.com/", Name: "高校"},
		{URL: "https://www.hdupt.com/", Name: "高校"},
	}
	added, updated, skipped := mergeTargetsOverwrite(incoming, nil, false)
	if len(added) != 1 || skipped != 1 || updated != 0 {
		t.Fatalf("apex 与 www 应只新增一条: added=%d updated=%d skipped=%d %+v", len(added), updated, skipped, added)
	}
	if dup := findDuplicateTarget(added, nil); dup != "" {
		t.Fatalf("合并结果仍过不了保存校验: %s", dup)
	}
}

// 同一个漏洞在 CookieCloud 定时自动导入的路径上（mergeTargets）。
func TestMergeTargetsApexAndWWWKeepsOne(t *testing.T) {
	incoming := []Target{
		{URL: "https://hdupt.com/"},
		{URL: "https://www.hdupt.com/"},
	}
	added, skipped := mergeTargets(incoming, nil)
	if len(added) != 1 || skipped != 1 {
		t.Fatalf("apex 与 www 应只新增一条: added=%d skipped=%d %+v", len(added), skipped, added)
	}
	if dup := findDuplicateTarget(added, nil); dup != "" {
		t.Fatalf("合并结果仍过不了保存校验: %s", dup)
	}
}

// 已有 apex 目标时，再导入用户手动改过的 www 版本应被跳过，不能新增。
func TestMergeTargetsSkipsWWWWhenApexExists(t *testing.T) {
	existing := []Target{{URL: "https://hdupt.com/"}}
	added, skipped := mergeTargets([]Target{{URL: "https://www.hdupt.com/"}}, existing)
	if len(added) != 0 || skipped != 1 {
		t.Fatalf("已有 apex 时应跳过 www: added=%d skipped=%d", len(added), skipped)
	}
}

// 候选列表里 apex 与 www 会显示成同一个名字（都剥掉 www 查站点名表），
// 必须合并成一条，否则用户勾选后又会触发「同一站点」的保存报错。
func TestDedupeCandidatesMergesApexAndWWW(t *testing.T) {
	list := []siteCandidate{
		{Domain: "example.com", Name: "示例", Source: "cookie"},
		{Domain: "www.example.com", Name: "示例", Source: "cookie"},
	}
	got := dedupeCandidates(list)
	if len(got) != 1 {
		t.Fatalf("同一注册域名应合并成一条，实际 %d 条: %+v", len(got), got)
	}
	// 凭据相同时优先 apex：cookie 与 localStorage 都按父域匹配，
	// apex 目标能拿到 www 名下的凭据，反过来拿不到。
	if got[0].Domain != "example.com" {
		t.Fatalf("应保留 apex，实际 %q", got[0].Domain)
	}
}

// 只有 www 那条带凭据时要保留 www，否则合并后登录态会丢。
func TestDedupeCandidatesKeepsCredentialHost(t *testing.T) {
	list := []siteCandidate{
		{Domain: "example.com", Name: "示例"},
		{Domain: "www.example.com", Name: "示例", Source: "cookie"},
	}
	got := dedupeCandidates(list)
	if len(got) != 1 || got[0].Domain != "www.example.com" || got[0].Source != "cookie" {
		t.Fatalf("应保留带 cookie 的 www: %+v", got)
	}
}

// 一条只有 cookie、一条只有 localStorage 时，合并后应标成 both 并保留 Exists。
func TestDedupeCandidatesMergesSourceAndExists(t *testing.T) {
	list := []siteCandidate{
		{Domain: "example.com", Name: "示例", Source: "cookie"},
		{Domain: "www.example.com", Name: "示例", Source: "localStorage", Exists: true},
	}
	got := dedupeCandidates(list)
	if len(got) != 1 {
		t.Fatalf("应合并成一条，实际 %d 条", len(got))
	}
	if got[0].Source != "both" {
		t.Fatalf("两种凭据都有时应标 both，实际 %q", got[0].Source)
	}
	if !got[0].Exists {
		t.Fatal("任一条已在目标列表里，合并后应保留 Exists")
	}
}

// 不同注册域名的候选不能被误合并。
func TestDedupeCandidatesKeepsDistinctSites(t *testing.T) {
	list := []siteCandidate{
		{Domain: "example.com", Name: "示例"},
		{Domain: "hdupt.com", Name: "高校"},
		{Domain: "kp.m-team.cc", Name: "馒头"},
	}
	got := dedupeCandidates(list)
	if len(got) != 3 {
		t.Fatalf("不同站点不应合并，实际 %d 条: %+v", len(got), got)
	}
}
