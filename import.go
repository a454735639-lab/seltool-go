package main

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// knownPTSites 是内置的 PT 站域名表。
// CookieCloud 里通常混着几百个普通网站，靠这个表 + 域名标签启发式来挑站点。
var knownPTSites = map[string]bool{
	// 常见中英文 PT
	"m-team.cc": true, "kp.m-team.cc": true, "hdchina.org": true,
	"hdtime.org": true, "hdhome.org": true, "hddolby.com": true,
	"hdfans.org": true, "hdarea.club": true, "hdsky.me": true,
	"hdcity.city": true, "hdbd.us": true, "hdatmos.club": true,
	"ourbits.club": true, "pterclub.net": true, "discfan.net": true,
	"1ptba.com": true, "audiences.me": true, "pttime.org": true,
	"carpt.net": true, "cyanbug.net": true, "springsunday.net": true,
	"zmpt.cc": true, "52pt.site": true, "open.cd": true,
	"hxpt.org": true, "tangpt.top": true, "ubits.club": true,
	"btschool.club": true, "agsvpt.cn": true, "keepfrds.com": true,
	"0ff.cc": true, "hdkyl.in": true, "gamegamept.com": true,
	"htpt.cc": true, "pandapt.net": true, "ptlgs.org": true,
	"xingyungept.org": true, "crabpt.vip": true,
	"gtkpw.xyz": true, "ptchdbits.co": true, "kufei.org": true,
	"hdmayi.com": true, "ptsbao.club": true, "tjupt.org": true,
	"npupt.com": true, "byr.pt": true, "haidan.vip": true,
	"soulvoice.club": true, "joyhd.net": true, "hdzone.org": true,
	"hd4fans.org": true, "pt.sjtu.edu.cn": true, "hdfun.me": true,
	"hdvideo.club": true, "pthome.net": true, "hdupt.com": true,
	"hdroute.org": true, "chdbits.co": true, "ttg.im": true,
	"u2.dmhy.org": true, "dmhy.org": true, "nanyu.xyz": true,
	"free.vip": true, "moecat.best": true,
}

// ptSiteTable 由 main 注入，供导入时查站点中文名。
// 用包级变量是为了让纯函数 BuildImportTargets 不必穿透传参。
var ptSiteTable *PTSiteTable

// siteDisplayName 返回站点显示名：优先中文名（来自可更新的站点表），否则用域名。
func siteDisplayName(host string) string {
	if ptSiteTable != nil {
		if n := ptSiteTable.Lookup(host); n != "" {
			return n
		}
	}
	return host
}

// detectPTSites 从 cookie_data 里挑出 PT 站域名，保留真实 host（含 www.），仅去点前缀并按 host 去重。
// 保留 www 的原因：cookie 往往只对 www 域有效，剥掉后新目标会拿不到登录态。
func detectPTSites(data map[string][]Cookie) []string {
	seen := map[string]bool{}
	out := make([]string, 0, 16)
	for raw := range data {
		d := normalizeDomain(raw) // 只去前导点、小写
		if d == "" {
			continue
		}
		if !isPTSite(d) || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// isPTSite 判定一个域是否为 PT 站，按可靠性从高到低：
//
//  1. 站点名表命中 —— 最可靠。表里是从 MoviePilot 同步来的真实 PT 站名单，
//     且可由面板更新，新站不用改代码就能被识别。
//  2. 内置域名表命中（含去 www 版本比对）—— 兜底，表里是人工整理的常见站。
//  3. 域名含 pt / torrent / hd 标签 —— 启发式，覆盖表里没有的站。
//
// 刻意不用「全量导入」：CookieCloud 里混着淘宝、百度、政务网站等日常站点，
// 全量导入会把它们也加进目标列表，定时任务会去访问无关站点。
func isPTSite(d string) bool {
	if d == "" {
		return false
	}
	bare := strings.TrimPrefix(d, "www.")

	// 1) 站点名表（可更新，最准）
	if ptSiteTable != nil {
		if ptSiteTable.Lookup(d) != "" || ptSiteTable.Lookup(bare) != "" {
			return true
		}
	}

	// 2) 内置域名表
	if knownPTSites[d] || knownPTSites[bare] {
		return true
	}

	// 3) 域名标签启发式
	for _, label := range strings.Split(bare, ".") {
		switch label {
		case "pt", "torrent", "bt":
			return true
		}
	}
	return false
}

// canonicalURLKey 生成用于去重的规范化键：小写 host + 路径，忽略末尾斜杠。
//
// 刻意不剥离 www. 前缀 —— www.hdtime.org 与 hdtime.org 的 Cookie 域不同，
// 合并会导致其中一个站拿不到登录态，必须各自保留。
// 只消除「仅末尾斜杠不同」这类真正的重复。
func canonicalURLKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return strings.ToLower(strings.TrimRight(strings.TrimSpace(raw), "/"))
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	path = strings.TrimRight(path, "/")
	if path == "" {
		path = "/"
	}
	return strings.ToLower(u.Hostname()) + ":" + path
}

// specialSiteDomains 是需要完整保留的多级域名白名单。
// 对齐 MoviePilot 的 _SPECIAL_SITE_DOMAINS：这些站的主域就是三级，
// 按「取最后两段」会算成 dmhy.org / gtkpw.xyz 而误合并到别的站。
var specialSiteDomains = []string{
	"u2.dmhy.org",
	"pt.ecust.pp.ua",
	"pt.gtkpw.xyz",
	"pt.gtk.pw",
}

// registeredDomain 提取用于站点去重的注册域名。
//
// 规则对齐 MoviePilot 的 extract_domain：
//  1. 命中特例白名单 → 原样返回
//  2. 去掉 Cookie 域常见的前导点（.hddolby.com → hddolby.com）
//  3. 标签数 > 3 → 原样保留（子域站点各自独立）
//  4. 否则取最后两段（www.hddolby.com → hddolby.com）
//
// 这样 www 与 apex 会归到同一个注册域名，从根源上杜绝重复目标。
func registeredDomain(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" {
		return ""
	}
	// 去掉 scheme 与路径，只留 host
	if i := strings.Index(v, "://"); i >= 0 {
		v = v[i+3:]
	}
	if i := strings.IndexAny(v, "/?#"); i >= 0 {
		v = v[:i]
	}
	// 去掉端口与 userinfo
	if i := strings.LastIndex(v, "@"); i >= 0 {
		v = v[i+1:]
	}
	if i := strings.LastIndex(v, ":"); i >= 0 && !strings.Contains(v[i:], "]") {
		v = v[:i]
	}
	v = strings.Trim(v, "[]")
	v = strings.TrimPrefix(v, ".")
	if v == "" {
		return ""
	}
	for _, d := range specialSiteDomains {
		if v == d || strings.HasSuffix(v, "."+d) {
			return d
		}
	}
	labels := strings.Split(v, ".")
	if len(labels) > 3 {
		return v
	}
	if len(labels) < 2 {
		return v
	}
	return strings.Join(labels[len(labels)-2:], ".")
}

// BuildImportTargets 把域名列表转成目标，跳过已存在的站点。
//
// 去重按 canonicalHost 匹配（忽略 www. 前缀与末尾斜杠），避免同一个站
// 因为「带不带 www」「带不带结尾 /」被重复导入——这正是历史上产生重复目标的原因。
// 同一 host 下路径不同的目标（如 ubits 的签到页）不算重复。
// BuildImportTargets 把域名列表转成目标，跳过已存在的站点。
//
// 去重按「注册域名」判断（对齐 MoviePilot）：www.hddolby.com 与 hddolby.com
// 归为同一个站 hddolby.com，第二次导入直接拒绝，从根源上杜绝重复目标。
// Cookie 注入侧由 buildAllowedDomains 逐级匹配父域，因此合并后不会丢登录态。
func BuildImportTargets(domains []string, existing []Target, _ map[string][]Cookie) (added []Target, skipped int) {
	seenReg := map[string]bool{}
	for _, t := range existing {
		raw := strings.TrimSpace(t.URL)
		if raw == "" {
			continue
		}
		reg := registeredDomain(raw)
		if reg == "" {
			continue
		}
		seenReg[reg] = true
	}

	for _, d := range domains {
		host := normalizeDomain(d)
		if host == "" {
			skipped++
			continue
		}
		reg := registeredDomain(host)
		if reg == "" {
			skipped++
			continue
		}
		if seenReg[reg] {
			skipped++
			continue
		}
		seenReg[reg] = true
		added = append(added, Target{
			Name:         siteDisplayName(host),
			URL:          "https://" + host + "/",
			WarmupPath:   "/favicon.ico",
			LocalStorage: []LocalStorage{},
		})
	}
	return added, skipped
}

// ParseManualTargets 解析手动粘贴的文本：每行一个域名或 URL，也支持逗号/空格分隔。
// 保留 scheme/path/query/fragment；拒绝 userinfo、非法端口、控制字符和空 host。
func ParseManualTargets(input string) []Target {
	fields := strings.FieldsFunc(input, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ' ' || r == '\t'
	})
	seen := map[string]bool{}
	var out []Target
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" || hasControlChar(f) {
			continue
		}
		rawURL := f
		if strings.Contains(rawURL, "://") {
			scheme := strings.ToLower(rawURL[:strings.Index(rawURL, "://")])
			if scheme != "http" && scheme != "https" {
				continue
			}
		} else {
			if !strings.Contains(rawURL, ".") {
				continue
			}
			rawURL = "https://" + rawURL
		}
		u, err := url.Parse(rawURL)
		if err != nil || u.User != nil {
			continue
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			continue
		}
		host := u.Hostname()
		if host == "" || strings.ContainsAny(host, " \t") {
			continue
		}
		if port := u.Port(); port != "" {
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				continue
			}
		} else if strings.HasSuffix(u.Host, ":") {
			continue
		}
		// 只在没有路径时补根路径，避免把 /attendance.php 变成 /attendance.php/
		if u.Path == "" {
			u.Path = "/"
		}
		key := u.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Target{
			// 与自动导入一致：优先用站点表里的中文名，未收录才回落域名
			Name:         siteDisplayName(host),
			URL:          key,
			WarmupPath:   "/favicon.ico",
			LocalStorage: []LocalStorage{},
		})
	}
	return out
}

func hasControlChar(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
