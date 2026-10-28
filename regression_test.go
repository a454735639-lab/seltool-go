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
