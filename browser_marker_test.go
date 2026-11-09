package main

import (
	"strings"
	"testing"
)

// TestLoggedInMarkerScript 校验 loggedInMarker 注入的脚本本身没写坏。
//
// 该脚本只在真实浏览器里求值，CI 上没有 Chromium，所以这里退一步：
// 确认脚本里保留了关键选择器与可见性判断，避免以后重构时误删。
func TestLoggedInMarkerScript(t *testing.T) {
	if loggedInMarkerScript == "" {
		t.Fatal("loggedInMarkerScript 为空")
	}
	for _, want := range []string{
		".usermenu",             // 好学（hxpt.org）登录后才有
		"usercp.php",            // NexusPHP 个人中心
		"userdetails",           // 好学/猪猪系的个人页
		"logout",                // 通用登出链接
		"getBoundingClientRect", // 必须做可见性判断
		"visibility",            // 隐藏元素不算
	} {
		if !strings.Contains(loggedInMarkerScript, want) {
			t.Errorf("脚本缺少关键片段 %q", want)
		}
	}
}

// TestNeutralTitleWouldPreviouslyTimeout 复现旧逻辑的判定空洞。
//
// 好学的登录态标题是「好学 · PT」，两个关键字表都不命中 —— 这是真实故障的根因
// （空转到 25s deadline 后报「等待页面加载超时」）。用纯函数形式固化下来，
// 防止有人把 loggedInMarker 删掉后回归。
func TestNeutralTitleWouldPreviouslyTimeout(t *testing.T) {
	for _, title := range []string{"好学 · PT", "好学 · FluxTorrent"} {
		if containsAny(title, failTitles...) {
			t.Errorf("%q 不应命中失败关键字", title)
		}
		if containsAny(title, successTitles...) {
			t.Errorf("%q 不应命中成功关键字", title)
		}
	}
}

// TestFailSuccessTitlesDoNotOverlap 保证两个标题关键字表不会互相覆盖。
//
// 两表一旦语义重叠，判定结果就取决于代码里哪个先被求值，属于难查的隐性 bug。
func TestFailSuccessTitlesDoNotOverlap(t *testing.T) {
	for _, f := range failTitles {
		for _, s := range successTitles {
			if strings.Contains(f, s) || strings.Contains(s, f) {
				t.Errorf("失败关键字 %q 与成功关键字 %q 语义重叠", f, s)
			}
		}
	}
}
