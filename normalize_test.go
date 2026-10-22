package main

import "testing"

func TestNormalizeDomain(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"裸域名", "kp.m-team.cc", "kp.m-team.cc"},
		{"大写", "KP.M-Team.CC", "kp.m-team.cc"},
		{"前导点", ".hdtime.org", "hdtime.org"},
		{"带 scheme", "https://kp.m-team.cc", "kp.m-team.cc"},
		{"带 scheme 和路径", "https://kp.m-team.cc/login", "kp.m-team.cc"},
		{"带端口", "kp.m-team.cc:8443", "kp.m-team.cc"},
		{"scheme 加端口加路径", "https://kp.m-team.cc:8443/login?x=1", "kp.m-team.cc"},
		{"协议相对", "//kp.m-team.cc", "kp.m-team.cc"},
		{"带查询串", "hdtime.org/?a=1", "hdtime.org"},
		{"末尾绝对域名点", "hdtime.org.", "hdtime.org"},
		{"带 userinfo", "user:pass@kp.m-team.cc", "kp.m-team.cc"},
		{"带空白", "  https://hdtime.org  ", "hdtime.org"},
		{"两段域名带 scheme", "https://hdtime.org", "hdtime.org"},
		{"空串", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeDomain(tc.in); got != tc.want {
				t.Fatalf("normalizeDomain(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// 归一化后的域名必须能和 CookieCloud 里的裸域名键匹配上。
func TestNormalizeDomainMatchesCloudKeys(t *testing.T) {
	cloudKeys := []string{"kp.m-team.cc", "hdtime.org", "rousi.pro"}
	userInputs := []string{
		"https://kp.m-team.cc",
		"https://hdtime.org/",
		"rousi.pro:8443",
		"https://hdtime.org.",
	}
	for _, in := range userInputs {
		domain := normalizeDomain(in)
		allowed := buildAllowedDomains(domain)
		matched := false
		for _, key := range cloudKeys {
			if domainMatches(normalizeDomain(key), allowed) {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("用户输入 %q 归一成 %q 后匹配不到任何云端键（allowed=%v）", in, domain, allowed)
		}
	}
}
