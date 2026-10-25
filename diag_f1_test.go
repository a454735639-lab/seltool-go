package main

import (
	"strings"
	"testing"
)

func TestF1AnnotateLoginFailureCloudflare(t *testing.T) {
	got := annotateLoginFailure("登录状态检查失败（页面含登录表单）", 5, true)
	if !strings.Contains(got, "Cloudflare") || !strings.Contains(got, "人工") {
		t.Fatalf("CF 人机挑战未被单独归类: %q", got)
	}
	// 挑战与 Cookie 无关，不该触发 CookieCloud 刷新（否则每个挑战站白拉一次远端）。
	if isCookieFailure(got) {
		t.Errorf("CF 人机挑战不应触发 Cookie 刷新: %q", got)
	}
	// 原始原因要保留，便于看到是哪条判定先失败的。
	if !strings.Contains(got, "页面含登录表单") {
		t.Errorf("归因丢失原始原因: %q", got)
	}
}

func TestF1AnnotateLoginFailureNoCookies(t *testing.T) {
	in := "登录状态检查失败（页面含登录表单）"
	got := annotateLoginFailure(in, 0, false)
	if !strings.Contains(got, "未解析到任何 CookieCloud Cookie") {
		t.Fatalf("未解析到 Cookie 的情况未被标注: %q", got)
	}
	// 采集侧补上 Cookie 后刷新是有救的，仍应触发一次刷新。
	if !isCookieFailure(got) {
		t.Errorf("「未采集到 Cookie」应仍触发一次刷新: %q", got)
	}
}

func TestF1AnnotateLoginFailurePassthrough(t *testing.T) {
	in := "登录状态检查失败（页面有可见的密码输入框）"
	if got := annotateLoginFailure(in, 7, false); got != in {
		t.Errorf("正常 Cookie 失效不应改写原因: got %q want %q", got, in)
	}
}

func TestF1AnnotateLoginFailurePrefersCloudflare(t *testing.T) {
	// 同时命中「无 Cookie」和「挑战页」时，挑战是更根本的原因（中国 PT 站的
	// cf_clearance 也是 Cookie，缺它不是采集问题而是没人工过挑战）。
	got := annotateLoginFailure("登录状态检查失败（标题命中失败关键字：请稍候）", 0, true)
	if !strings.Contains(got, "Cloudflare") {
		t.Fatalf("挑战页应优先于无 Cookie 归因: %q", got)
	}
}

func TestF1CFChallengeScriptTargetsStrongMarkers(t *testing.T) {
	// 判定只允许看强特征。challenge-platform / turnstile 这类脚本名普通页面
	// （含登录页）也会内联，用它匹配会让「真·登录页」被误报成 CF 挑战。
	for _, bad := range []string{"challenge-platform", "turnstile", "cf-please-wait"} {
		if strings.Contains(cfChallengeScript, bad) {
			t.Errorf("CF 判定不应匹配 %q（正常页面也含该串）", bad)
		}
	}
	for _, want := range []string{"cf-challenge-running", "cf-browser-verification", "just a moment"} {
		if !strings.Contains(cfChallengeScript, want) {
			t.Errorf("CF 判定缺少强特征 %q", want)
		}
	}
}
