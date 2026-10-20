package main

import (
	"fmt"
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

// failureCategory 把原始失败信息归类成一眼能懂的原因；无法归类时返回空串。
func failureCategory(message string) string {
	m := strings.ToLower(strings.TrimSpace(message))
	switch {
	case m == "":
		return "无运行记录"
	case strings.Contains(m, "chrome"), strings.Contains(m, "浏览器"), strings.Contains(m, "chromedp"):
		return "浏览器异常"
	case strings.Contains(m, "cookie") && (strings.Contains(m, "未找到") || strings.Contains(m, "缺失") || strings.Contains(m, "没有")):
		return "Cookie 缺失"
	case strings.Contains(m, "登录态"), strings.Contains(m, "未登录"), strings.Contains(m, "需要登录"), strings.Contains(m, "login"):
		return "登录态失效"
	case strings.Contains(m, "证书"), strings.Contains(m, "certificate"), strings.Contains(m, "x509"), strings.Contains(m, "tls"):
		return "证书问题"
	case strings.Contains(m, "cloudflare"), strings.Contains(m, "challenge"), strings.Contains(m, "人机"), strings.Contains(m, "拦截"):
		return "被站点拦截"
	case strings.Contains(m, "502"):
		return "站点故障 502"
	case strings.Contains(m, "503"):
		return "站点故障 503"
	case strings.Contains(m, "504"):
		return "站点故障 504"
	case strings.Contains(m, "500"):
		return "站点故障 500"
	case strings.Contains(m, "域名"), strings.Contains(m, "no such host"), strings.Contains(m, "dns"):
		return "域名解析失败"
	case strings.Contains(m, "timeout"), strings.Contains(m, "超时"):
		return "连接超时"
	case strings.Contains(m, "refused"), strings.Contains(m, "reset"), strings.Contains(m, "无法连接"), strings.Contains(m, "连接失败"):
		return "无法连接"
	case strings.Contains(m, "预热"):
		return "预热地址失败"
	case strings.Contains(m, "登录状态") || strings.Contains(m, "登录检查"):
		return "需重新登录"
	case strings.Contains(m, "登录页") || strings.Contains(m, "含登录表单"):
		return "需重新登录"
	case strings.Contains(m, "page load"), strings.Contains(m, "navigation"), strings.Contains(m, "导航"):
		return "页面加载失败"
	case strings.Contains(m, "目标未生成"), strings.Contains(m, "没有运行记录"):
		return "本次未执行"
	}
	return ""
}

// needsRelogin 判断这条失败是否需要人工重新登录。
func needsRelogin(category string) bool {
	return category == "Cookie 缺失" || category == "登录态失效" || category == "需重新登录"
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
	case strings.HasPrefix(cat, "站点故障"), cat == "被站点拦截", cat == "证书问题":
		return 1
	case cat == "域名解析失败", cat == "预热地址失败", cat == "页面加载失败":
		return 2
	default:
		return 3
	}
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
		cat := failureCategory(reason)
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
			name:    mdLinkText(t.DisplayName()),
			url:     strings.TrimSpace(t.URL),
			streak:  streak,
			lastOK:  lastOK,
			rawNote: truncateRunes(strings.TrimPrefix(strings.TrimSpace(reason), "执行失败："), 60),
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
	var actionable []string
	if len(failed) > 0 {
		groups := buildFailureGroups(failed, latest, previous, recordsOf)
		fmt.Fprintf(&b, "\n\n%s失败 %d 个，按原因分组%s", markDim, len(failed), markEnd)

		for _, g := range groups {
			// 组标题：原因 + 计数 +（需重登标记）
			head := fmt.Sprintf("%s ×%d", g.category, len(g.items))
			if g.actionable {
				head += " " + markWarn + "[需重登]" + markEnd
				for _, it := range g.items {
					actionable = append(actionable, it.name)
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

				// 只对「非重登类」补一行原始报错——重登类原因已足够明确
				if !g.actionable && it.rawNote != "" && it.rawNote != g.category {
					fmt.Fprintf(&b, "\n  %s%s%s", markDim, truncateRunes(it.rawNote, 52), markEnd)
				}
			}
		}
	}

	// ── 成功站点不单独列出 ──
	// 成功数量已在概览行体现（"44 成功"），逐个列名字属于噪音。
	// 只有全部成功时才给一行确认，避免"什么都没收到"的疑虑。

	// ── 第 5 段：行动项 ──
	if len(actionable) > 0 {
		fmt.Fprintf(&b, "\n\n%s▸ 需处理：%s 需要重新登录并同步 Cookie%s",
			markWarn, strings.Join(actionable, "、"), markEnd)
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
