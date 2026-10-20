package main

// 运行时自检。
//
// 目的：把「目标、图标、数据文件、调度」四类状态一次报出来，
// 出问题时不用逐个接口查。
//
// 起因：图标「时好时坏」排查了七八轮，根因是前后端域名规则不一致 +
// 按内容去重漏 key。这类问题只要有一个自检接口，一眼就能看出来。

import (
	"net/http"
	"os"
	"strings"
	"time"
)

// selfCheckResponse 是 /api/settings/selfcheck 的响应。
type selfCheckResponse struct {
	OK       bool              `json:"ok"`
	Targets  int               `json:"targets"`
	Icons    selfCheckIcons    `json:"icons"`
	Sites    selfCheckSites    `json:"sites"`
	Files    map[string]string `json:"files"`
	Runtime  selfCheckRuntime  `json:"runtime"`
	Problems []string          `json:"problems"`
}

type selfCheckIcons struct {
	Total   int      `json:"total"`
	Covered int      `json:"covered"`
	Missing []string `json:"missing"`
	Bytes   int      `json:"bytes"`
}

type selfCheckSites struct {
	Total   int    `json:"total"`
	Version string `json:"version"`
}

type selfCheckRuntime struct {
	AnonMB    int64  `json:"anonMB"`
	FileMB    int64  `json:"fileMB"`
	Running   bool   `json:"running"`
	Cron      string `json:"cron"`
	CronNext  string `json:"cronNext"`
	UptimeMin int64  `json:"uptimeMin"`
}

// iconCandidates 返回一个 host 可能对应的所有图标键，与前端 iconFor 保持同一顺序。
//
// 前后端各有一份域名处理逻辑，历史上就是这里出的问题：
// 后端按目标 URL 的 host 返回图标，前端按去掉 www 的形式查找，两边对不上。
// 自检用同一套候选顺序，能提前发现不一致。
func iconCandidates(host string) []string {
	h := normalizeIconKey(host)
	if h == "" {
		return nil
	}
	out := []string{h}
	if bare := strings.TrimPrefix(h, "www."); bare != h {
		out = append(out, bare)
	}
	if reg := registeredDomain(h); reg != "" && reg != h {
		out = append(out, reg)
	}
	// 前端还会尝试给注册域名补 www 前缀
	if reg := registeredDomain(h); reg != "" {
		if w := "www." + reg; w != h {
			out = append(out, w)
		}
	}
	return out
}

// lookupIcon 按候选顺序查图标，与前端行为一致。
func lookupIcon(table *PTIconTable, host string) string {
	if table == nil {
		return ""
	}
	for _, k := range iconCandidates(host) {
		if v := table.Lookup(k); v != "" {
			return v
		}
	}
	return ""
}

func (s *Server) handleSelfCheck(w http.ResponseWriter, r *http.Request) {
	resp := selfCheckResponse{OK: true, Files: map[string]string{}}

	// ── 目标与图标覆盖 ──
	var targets []Target
	if s.targets != nil {
		targets = s.targets.Get()
	}
	resp.Targets = len(targets)

	if s.ptIcons != nil {
		resp.Icons.Total = s.ptIcons.Count()
		for _, v := range s.ptIcons.All() {
			resp.Icons.Bytes += len(v)
		}
	}
	for _, t := range targets {
		host := hostOf(t.URL)
		if lookupIcon(s.ptIcons, host) == "" {
			name := t.DisplayName()
			if name == "" {
				name = host
			}
			resp.Icons.Missing = append(resp.Icons.Missing, name)
		}
	}
	resp.Icons.Covered = len(targets) - len(resp.Icons.Missing)

	// ── 站点名表 ──
	if s.ptSites != nil {
		version, _, count := s.ptSites.Snapshot()
		resp.Sites.Total = count
		resp.Sites.Version = version
	}

	// ── 数据文件 ──
	for _, f := range []string{
		"config/targets.json",
		"config/cookiecloud-settings.json",
		"config/notify-settings.json",
		"config/pt-sites.json",
		"config/pt-icons.json",
	} {
		if _, err := os.Stat(f); err == nil {
			resp.Files[f] = "ok"
		} else if os.IsNotExist(err) {
			resp.Files[f] = "absent"
		} else {
			resp.Files[f] = "error"
		}
	}

	// ── 运行时 ──
	resp.Runtime.AnonMB, resp.Runtime.FileMB = readCgroupMemory()
	if s.automation != nil {
		resp.Runtime.Running = s.automation.IsRunning()
	}
	if s.scheduler != nil {
		snap := s.scheduler.Snapshot()
		resp.Runtime.Cron = snap.Cron
		if snap.NextRun != "" {
			resp.Runtime.CronNext = snap.NextRun
		}
	}

	// ── 问题汇总 ──
	if resp.Icons.Missing != nil {
		resp.Problems = append(resp.Problems,
			"有目标没有图标："+strings.Join(resp.Icons.Missing, "、"))
	}
	if resp.Icons.Total == 0 {
		resp.Problems = append(resp.Problems, "图标表为空")
	}
	if resp.Targets == 0 {
		resp.Problems = append(resp.Problems, "没有配置任何目标")
	}
	for f, st := range resp.Files {
		if st == "error" {
			resp.Problems = append(resp.Problems, "文件读取异常："+f)
		}
	}
	resp.OK = len(resp.Problems) == 0

	writeJSON(w, http.StatusOK, resp)
}

// readCgroupMemory 读 cgroup v2 的 anon/file 内存，单位 MB。
// 读不到就返回 0，不影响自检结果。
func readCgroupMemory() (anonMB, fileMB int64) {
	data, err := os.ReadFile("/sys/fs/cgroup/memory.stat")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		var n int64
		for _, c := range fields[1] {
			if c < '0' || c > '9' {
				n = 0
				break
			}
			n = n*10 + int64(c-'0')
		}
		switch fields[0] {
		case "anon":
			anonMB = n / 1048576
		case "file":
			fileMB = n / 1048576
		}
	}
	return anonMB, fileMB
}

// 避免未使用告警（time 在后续扩展里会用到）
var _ = time.Now
