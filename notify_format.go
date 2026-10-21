package main

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"
)

var linkRe = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)

// 颜色标记：markdown 模式转成企微 <font>，纯文本模式整段剥掉。
const (
	markWarn = "[[w]]"
	markInfo = "[[i]]"
	markDim  = "[[d]]"
	markEnd  = "[[/]]"
)

var (
	rayIDRe    = regexp.MustCompile(`(?i)ray\s*id[:\s]+([0-9a-f]{8,})`)
	certErrRe  = regexp.MustCompile(`(ERR_CERT_[A-Z_]+)`)
	netErrRe   = regexp.MustCompile(`net::(ERR_[A-Z_]+)`)
	httpCodeRe = regexp.MustCompile(`\b(5[0-9]{2})\b`)
	titleRe    = regexp.MustCompile(`标题[：:]\s*([^）)]*)`)
)

// primaryCredentialKey 返回目标维持登录态用的主凭据键名（localStorage 里的 token/auth 等）。
func primaryCredentialKey(t Target) string {
	for _, kv := range t.LocalStorage {
		k := strings.ToLower(kv.Key)
		if strings.Contains(k, "token") || strings.Contains(k, "auth") ||
			strings.Contains(k, "jwt") || strings.Contains(k, "session") {
			return kv.Key
		}
	}
	if len(t.LocalStorage) > 0 {
		return t.LocalStorage[0].Key
	}
	return ""
}

// netDetail 从报错里抽出 Chromium 的网络错误码，没有就返回空串。
func netDetail(raw string) string {
	if g := netErrRe.FindStringSubmatch(raw); len(g) > 1 {
		return g[1]
	}
	return ""
}

// classifyFailure 把原始失败信息归类成具体、可操作的原因，并给出细节。
// 判定顺序从「最具体」到「最笼统」，先命中先返回。
//
// 关键：Token 失效和 Cookie 失效的处理动作完全不同
//
//	Cookie → 同步 CookieCloud
//	Token  → 去站点手动抓 localStorage
//
// 所以必须结合目标自身的凭据配置来分类，不能只看报错文本。
func classifyFailure(t Target, message string) (category, detail string) {
	raw := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(message), "执行失败："))
	m := strings.ToLower(raw)

	if m == "" || strings.Contains(m, "目标未生成") || strings.Contains(m, "没有运行记录") {
		return "本次未执行", ""
	}

	if strings.Contains(m, "cloudflare") || strings.Contains(m, "ray id") ||
		strings.Contains(m, "just a moment") || strings.Contains(m, "checking your browser") ||
		strings.Contains(m, "challenge") || strings.Contains(m, "人机") ||
		strings.Contains(m, "请稍候") || strings.Contains(m, "cf-mitigated") {
		d := ""
		if strings.Contains(m, "请稍候") {
			d = "需要人机验证"
		} else if strings.Contains(m, "just a moment") {
			d = "需要人机验证"
		}
		return "人机验证拦截", d
	}

	if strings.Contains(m, "err_cert") || strings.Contains(m, "x509") ||
		strings.Contains(m, "certificate") || strings.Contains(m, "证书") {
		d := "证书无效"
		if strings.Contains(m, "date_invalid") || strings.Contains(m, "expired") {
			d = "证书已过期"
		}
		return "网站证书失效", d
	}

	if g := netErrRe.FindStringSubmatch(raw); len(g) > 1 {
		code := g[1]
		switch {
		case strings.Contains(code, "NAME_NOT_RESOLVED"), strings.Contains(code, "NAME_RESOLUTION"):
			return "域名解析失败", "站点可能换域名了"
		case strings.Contains(code, "CONNECTION_CLOSED"), strings.Contains(code, "CONNECTION_RESET"),
			strings.Contains(code, "CONNECTION_REFUSED"), strings.Contains(code, "ADDRESS_UNREACHABLE"):
			return "站点拒绝连接", "站点可能挂了或被墙"
		case strings.Contains(code, "TIMED_OUT"):
			return "连接超时", "响应太慢"
		case strings.Contains(code, "INTERNET_DISCONNECTED"):
			return "本机网络断开", "检查一下本机网络"
		case strings.Contains(code, "TOO_MANY_REDIRECTS"):
			return "跳转次数过多", "站点配置有问题"
		case strings.Contains(code, "HTTP2_PROTOCOL_ERROR"):
			return "网站协议异常", "站点服务异常"
		default:
			return "站点连不上", ""
		}
	}

	if strings.Contains(m, "no such host") || strings.Contains(m, "nxdomain") ||
		strings.Contains(m, "dns") || strings.Contains(m, "域名") {
		return "域名解析失败", "站点可能换域名了"
	}

	if strings.Contains(m, "chrome") || strings.Contains(m, "chromedp") || strings.Contains(m, "浏览器") {
		return "浏览器异常", "浏览器启动或执行失败"
	}

	if g := httpCodeRe.FindStringSubmatch(raw); len(g) > 1 {
		return "站点故障（" + g[1] + "）", "站点服务器报错"
	}

	// 403 是站点明确拒绝（IP / UA 被判成异常），重试没用，单独归类
	if strings.Contains(m, "403") && strings.Contains(m, "forbidden") {
		return "访问被拒绝（403）", "站点判定为异常访问"
	}

	if strings.Contains(m, "等待页面加载超时") {
		return "页面打不开", "页面白屏或加载不动"
	}

	loginish := strings.Contains(m, "登录态") || strings.Contains(m, "未登录") ||
		strings.Contains(m, "需要登录") || strings.Contains(m, "登录状态") ||
		strings.Contains(m, "登录检查") || strings.Contains(m, "登录页") ||
		strings.Contains(m, "含登录表单") || strings.Contains(m, "登录/注册") ||
		strings.Contains(m, "密码输入框") || strings.Contains(m, "login")
	if loginish {
		if primaryCredentialKey(t) != "" {
			return "登录过期（Token）", ""
		}
		return "登录过期（Cookie）", ""
	}

	if strings.Contains(m, "cookie") && (strings.Contains(m, "未找到") ||
		strings.Contains(m, "缺失") || strings.Contains(m, "没有")) {
		return "登录过期（Cookie）", "CookieCloud 里没有这个站的记录"
	}

	if strings.Contains(m, "timeout") || strings.Contains(m, "超时") {
		return "连接超时", ""
	}

	if strings.Contains(m, "refused") || strings.Contains(m, "reset") ||
		strings.Contains(m, "无法连接") || strings.Contains(m, "连接失败") {
		return "站点拒绝连接", ""
	}

	if strings.Contains(m, "预热") {
		return "站点连不上", ""
	}

	if strings.Contains(m, "page load") || strings.Contains(m, "navigation") || strings.Contains(m, "导航") {
		return "页面打不开", ""
	}

	return "", ""
}

// needsRelogin 判断这条失败是否需要人工重新登录。
func needsRelogin(category string) bool {
	switch category {
	case "登录过期（Cookie）", "登录过期（Token）", "登录态失效", "需重新登录":
		return true
	}
	return false
}

// failureStreak 从最近一条往前数连续失败次数，并返回最近一次成功时间。
func failureStreak(records []RunRecord) (int, string) {
	streak := 0
	lastSuccess := ""
	for _, r := range records {
		if r.Success {
			lastSuccess = r.ExecutedAt
			break
		}
		streak++
	}
	return streak, lastSuccess
}

func shortTime(rfc string) string {
	if rfc == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, rfc); err == nil {
		return t.Format("01-02 15:04")
	}
	if len(rfc) >= 16 {
		return strings.Replace(rfc[5:16], "T", " ", 1)
	}
	return rfc
}

func humanDuration(d time.Duration) string {
	if d < time.Second {
		return "不到 1 秒"
	}
	s := int(d.Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%d 秒", s)
	case s < 3600:
		return fmt.Sprintf("%d 分 %d 秒", s/60, s%60)
	}
	return fmt.Sprintf("%d 小时 %d 分", s/3600, s%3600/60)
}

func mdLinkText(s string) string {
	s = strings.TrimSpace(s)
	r := strings.NewReplacer("[", "(", "]", ")", "|", "/", "\n", " ")
	return r.Replace(s)
}

// ══════════════════════════════════════════════════════════
// 新版排版：按「失败原因」分组，同类站点聚在一起
// ══════════════════════════════════════════════════════════

// groupedFailure 是同一原因下的一组失败目标。
type groupedFailure struct {
	category   string
	actionable bool
	items      []failureItem
}

type failureItem struct {
	name    string
	url     string
	streak  int
	lastOK  string
	rawNote string
}

// 分组优先级：需要人动手的排最前，其次是站点侧故障，最后是环境问题。
func categoryRank(cat string) int {
	switch {
	case needsRelogin(cat):
		return 0
	case cat == "人机验证拦截", cat == "网站证书失效":
		return 1
	case strings.HasPrefix(cat, "站点故障"), cat == "域名解析失败":
		return 2
	case cat == "连接超时", cat == "站点拒绝连接", cat == "站点连不上", cat == "页面打不开":
		return 3
	default:
		return 4
	}
}

// hostResolves 判断域名当前是否有解析记录。
// 有些站关停后 DNS 记录被删，但 Chromium 只会报「连接被关闭」，
// 不查一次 DNS 就会把「域名没了」误报成「站点挂了或被墙」。
func hostResolves(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	return err == nil && len(addrs) > 0
}

// buildFailureGroups 把失败目标按原因归类聚合。
func buildFailureGroups(failed []Target, latest, previous map[string]RunRecord, recordsOf func(string) []RunRecord) []groupedFailure {
	byCat := map[string]*groupedFailure{}
	for _, t := range failed {
		record, ok := latest[t.URL]
		reason := "本次任务未生成运行记录"
		if ok {
			reason = record.Message
		}
		cat, detail := classifyFailure(t, reason)
		// 连接类失败再确认一次 DNS：域名没了和站点挂了，处理方式完全不同
		if cat == "站点拒绝连接" || cat == "站点连不上" || cat == "连接超时" {
			if host := hostOf(t.URL); !hostResolves(host) {
				cat = "域名解析失败"
				detail = "站点可能换域名了"
			}
		}
		if cat == "" {
			cat = truncateRunes(strings.TrimPrefix(strings.TrimSpace(reason), "执行失败："), 14)
		}
		g, exists := byCat[cat]
		if !exists {
			g = &groupedFailure{category: cat, actionable: needsRelogin(cat)}
			byCat[cat] = g
		}
		streak, lastOK := failureStreak(recordsOf(t.URL))
		g.items = append(g.items, failureItem{
			name:    t.DisplayName(),
			url:     "", // 通知里只写站点名，不贴网址
			streak:  streak,
			lastOK:  lastOK,
			rawNote: detail,
		})
	}

	groups := make([]groupedFailure, 0, len(byCat))
	for _, g := range byCat {
		sort.SliceStable(g.items, func(i, j int) bool { return g.items[i].name < g.items[j].name })
		groups = append(groups, *g)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		ri, rj := categoryRank(groups[i].category), categoryRank(groups[j].category)
		if ri != rj {
			return ri < rj
		}
		if len(groups[i].items) != len(groups[j].items) {
			return len(groups[i].items) > len(groups[j].items)
		}
		return groups[i].category < groups[j].category
	})
	return groups
}

// streakSuffix 生成 " · 连续 7 次" / " · 首次失败" 之类的后缀。
func streakSuffix(streak int, lastOK string) string {
	if streak <= 1 {
		return " · 首次失败"
	}
	s := fmt.Sprintf(" · 连续 %d 次", streak)
	if lastOK != "" {
		s += "（上次成功 " + shortTime(lastOK) + "）"
	}
	return s
}

// buildTaskMessage 生成任务结束通知正文（含颜色标记，纯文本模式由上层剥离）。
func buildTaskMessage(notificationName string, targets []Target, failed []Target, latest, previous map[string]RunRecord, taskFailure error, elapsed time.Duration, recordsOf func(string) []RunRecord) string {
	var b strings.Builder
	now := time.Now().Format("01-02 15:04")
	successCount := len(targets) - len(failed)

	// ── 第 1 段：概览行（一行看完）──
	if len(failed) == 0 && taskFailure == nil {
		fmt.Fprintf(&b, "%s · 全部 %d 个目标成功 · 用时 %s", now, len(targets), humanDuration(elapsed))
	} else {
		fmt.Fprintf(&b, "%s · %d 个目标 · %s%d 成功%s · %s%d 失败%s · 用时 %s",
			now, len(targets),
			markInfo, successCount, markEnd,
			markWarn, len(failed), markEnd,
			humanDuration(elapsed))
	}

	// ── 第 2 段：任务级异常（如有）──
	if taskFailure != nil {
		fmt.Fprintf(&b, "\n%s⚠ 任务异常：%s%s", markWarn,
			truncateRunes(buildFailureMessage(taskFailure), 160), markEnd)
	}

	// ── 第 3 段：失败分组 ──
	var cookieItems, tokenItems []string
	if len(failed) > 0 {
		groups := buildFailureGroups(failed, latest, previous, recordsOf)
		fmt.Fprintf(&b, "\n\n%s失败 %d 个，按原因分组%s", markDim, len(failed), markEnd)

		for _, g := range groups {
			// 组标题：原因 + 计数 +（需重登标记）
			head := fmt.Sprintf("%s ×%d", g.category, len(g.items))
			if g.actionable {
				head += " " + markWarn + "[需重登]" + markEnd
				for _, it := range g.items {
					if g.category == "登录过期（Token）" {
						tokenItems = append(tokenItems, it.name)
					} else {
						cookieItems = append(cookieItems, it.name)
					}
				}
			}
			fmt.Fprintf(&b, "\n\n**%s**", head)

			for _, it := range g.items {
				line := "· " + it.name
				if strings.HasPrefix(it.url, "http") {
					line = fmt.Sprintf("· [%s](%s)", it.name, it.url)
				}
				line += streakSuffix(it.streak, it.lastOK)
				fmt.Fprintf(&b, "\n%s", line)

				// 细节行：Token 失效会显示「凭据 token」，
				// CF 盾显示 Ray ID，证书显示 ERR_CERT_*，网络类显示 ERR_* 码。
				if it.rawNote != "" && it.rawNote != g.category {
					fmt.Fprintf(&b, "\n  %s%s%s", markDim, truncateRunes(it.rawNote, 52), markEnd)
				}
			}
		}
	}

	// ── 成功站点不单独列出 ──
	// 成功数量已在概览行体现（"44 成功"），逐个列名字属于噪音。
	// 只有全部成功时才给一行确认，避免"什么都没收到"的疑虑。

	// ── 第 5 段：行动项 ──
	if len(cookieItems)+len(tokenItems) > 0 {
		fmt.Fprintf(&b, "\n\n%s▸ 需处理%s", markWarn, markEnd)
		if len(cookieItems) > 0 {
			fmt.Fprintf(&b, "\n  Cookie 过期：%s\n    → 到 CookieCloud 重新同步一次", strings.Join(cookieItems, "、"))
		}
		if len(tokenItems) > 0 {
			fmt.Fprintf(&b, "\n  Token 过期：%s\n    → 到站点重新抓取登录凭据", strings.Join(tokenItems, "、"))
		}
	}
	return b.String()
}

func truncateRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

func stripMarkers(s string) string {
	r := strings.NewReplacer(markWarn, "", markInfo, "", markDim, "", markEnd, "")
	return r.Replace(s)
}

// toPlainText 在 stripMarkers 基础上再剥掉 markdown 语法，供 text 模式与企业微信应用消息使用。
func toPlainText(s string) string {
	s = stripMarkers(s)
	s = linkRe.ReplaceAllString(s, "$1 ($2)")
	s = strings.ReplaceAll(s, "**", "")
	out := make([]string, 0, 32)
	for _, line := range strings.Split(s, "\n") {
		// 引用前缀在纯文本里是噪音，直接去掉
		out = append(out, strings.TrimPrefix(line, "> "))
	}
	return strings.Join(out, "\n")
}

// renderMarkdownMarkers 把颜色标记转成企微支持的 font 标签。
func renderMarkdownMarkers(s string) string {
	r := strings.NewReplacer(
		markWarn, `<font color="warning">`,
		markInfo, `<font color="info">`,
		markDim, `<font color="comment">`,
		markEnd, `</font>`,
	)
	return r.Replace(s)
}
