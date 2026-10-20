package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// cronParser 与 scheduler 的 WithSeconds() 保持一致：强制六段（秒 分 时 日 月 周）。
// cron.ParseStandard 只认五段，直接用它会拒绝本项目既有的 "0 0 10 * * *" 配置。
var cronParser = cron.NewParser(
	cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// ScheduleSettings 是 Web 面板可编辑的定时配置；存独立文件，保存即热重载。
type ScheduleSettings struct {
	Enabled bool   `json:"enabled"`
	Cron    string `json:"cron"`
	Zone    string `json:"zone"`
	// 展示用的可读描述，由后端根据 cron 生成，前端只读
	Describe string `json:"describe,omitempty"`
	// 下次触发时间（RFC3339），供面板显示
	NextRun string `json:"nextRun,omitempty"`
}

// 常用预设，前端下拉直接取用
type SchedulePreset struct {
	Label string `json:"label"`
	Cron  string `json:"cron"`
}

func schedulePresets() []SchedulePreset {
	return []SchedulePreset{
		{"每天 08:00", "0 0 8 * * *"},
		{"每天 10:00", "0 0 10 * * *"},
		{"每天 12:00", "0 0 12 * * *"},
		{"每天 20:00", "0 0 20 * * *"},
		{"每天 0 点", "0 0 0 * * *"},
		{"每 6 小时", "0 0 */6 * * *"},
		{"每 12 小时", "0 0 */12 * * *"},
		{"每周一 08:00", "0 0 8 * * 1"},
		{"每小时", "0 0 * * * *"},
	}
}

type ScheduleStore struct {
	mu   sync.RWMutex
	file string
	cur  ScheduleSettings
}

func NewScheduleStore(file string) *ScheduleStore {
	return &ScheduleStore{file: absPath(file)}
}

func (s *ScheduleStore) Load() (ScheduleSettings, bool, error) {
	data, err := os.ReadFile(s.file)
	if err != nil {
		if os.IsNotExist(err) {
			return ScheduleSettings{}, false, nil
		}
		return ScheduleSettings{}, false, err
	}
	var saved ScheduleSettings
	if err := json.Unmarshal(data, &saved); err != nil {
		return ScheduleSettings{}, false, err
	}
	s.mu.Lock()
	s.cur = saved
	s.mu.Unlock()
	return saved, true, nil
}

func (s *ScheduleStore) Current() ScheduleSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// Save 校验并落盘。cron 非法、时区无效直接拒绝，避免存进去一个起不来的配置。
func (s *ScheduleStore) Save(in ScheduleSettings) (ScheduleSettings, error) {
	in.Cron = strings.TrimSpace(in.Cron)
	in.Zone = strings.TrimSpace(in.Zone)
	if in.Zone == "" {
		in.Zone = "Asia/Shanghai"
	}
	if _, err := time.LoadLocation(in.Zone); err != nil {
		return ScheduleSettings{}, fmt.Errorf("时区无效: %s", in.Zone)
	}
	if in.Cron == "" {
		in.Cron = "0 0 8 * * *"
	}
	if _, err := cronParser.Parse(normalizeCron(in.Cron)); err != nil {
		return ScheduleSettings{}, fmt.Errorf("cron 表达式无效: %v", err)
	}
	in.Describe = describeCron(in.Cron)
	in.NextRun = ""

	s.mu.Lock()
	s.cur = in
	s.mu.Unlock()

	if err := writeJSONAtomic(s.file, in, 0o600); err != nil {
		return ScheduleSettings{}, err
	}
	return in, nil
}

// normalizeCron 兼容 5 段（分 时 日 月 周）写法，自动补秒位。
func normalizeCron(expr string) string {
	fields := strings.Fields(expr)
	if len(fields) == 5 {
		return "0 " + expr
	}
	return expr
}

// describeCron 把 cron 转成人话，覆盖常见形态；无法识别时原样返回。
func describeCron(expr string) string {
	f := strings.Fields(normalizeCron(expr))
	if len(f) != 6 {
		return expr
	}
	sec, min, hour, dom, mon, dow := f[0], f[1], f[2], f[3], f[4], f[5]

	// 秒位必须固定，否则不是「定时」而是「轮询」
	if sec != "0" {
		return "每 " + sec + " 秒"
	}
	weekday := map[string]string{
		"0": "周日", "1": "周一", "2": "周二", "3": "周三",
		"4": "周四", "5": "周五", "6": "周六", "7": "周日",
	}
	pad2 := func(v string) string {
		if len(v) == 1 {
			return "0" + v
		}
		return v
	}
	hhmm := func(h, m string) string { return pad2(h) + ":" + pad2(m) }

	switch {
	case mon == "*" && dom == "*" && dow == "*":
		if min == "0" && hour == "*" {
			return "每小时整点"
		}
		// "*/6" 这类本身就是「每 6 小时」，不能再加「每天」
		if strings.HasPrefix(hour, "*/") {
			return "每 " + strings.TrimPrefix(hour, "*/") + " 小时"
		}
		if hour == "*" {
			return "每小时第 " + pad2(min) + " 分"
		}
		return "每天 " + hhmm(hour, min)
	case dow != "*" && dom == "*" && mon == "*":
		name := weekday[dow]
		if name == "" {
			name = "周 " + dow
		}
		return "每" + name + " " + hhmm(hour, min)
	case dom != "*" && mon == "*" && dow == "*":
		return "每月 " + dom + " 日 " + hhmm(hour, min)
	}
	return expr
}

// Scheduler 包住 cron 实例，支持运行时替换表达式而不重启进程。
type Scheduler struct {
	mu      sync.Mutex
	cron    *cron.Cron
	entryID cron.EntryID
	job     func()
	zone    string
	enabled bool
	cronStr string
	lastErr string

	// MoviePilot 自动同步（独立于目标执行任务）
	mpCron    *cron.Cron
	mpEntryID cron.EntryID
	mpJob     func(MoviePilotSyncConfig)
	mpCfg     MoviePilotSyncConfig
}

func NewScheduler(job func()) *Scheduler {
	return &Scheduler{job: job}
}

// Apply 用给定配置重建调度。返回错误时保持原调度不变。
func (s *Scheduler) Apply(set ScheduleSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	loc, err := time.LoadLocation(set.Zone)
	if err != nil {
		s.lastErr = err.Error()
		return fmt.Errorf("时区无效: %s", set.Zone)
	}
	expr := normalizeCron(set.Cron)
	if _, err := cronParser.Parse(expr); err != nil {
		s.lastErr = err.Error()
		return fmt.Errorf("cron 表达式无效: %v", err)
	}

	// 先停旧的，再起新的；失败时回滚到旧表达式
	oldCron, oldZone, oldEnabled := s.cronStr, s.zone, s.enabled
	oldScheduler := s.cron
	oldEntry := s.entryID

	if oldScheduler != nil {
		oldScheduler.Stop()
	}

	if !set.Enabled {
		s.cron = nil
		s.entryID = 0
		s.enabled = false
		s.cronStr = expr
		s.zone = set.Zone
		s.lastErr = ""
		logf("定时任务已停用")
		return nil
	}

	c := cron.New(cron.WithSeconds(), cron.WithLocation(loc))
	id, err := c.AddFunc(expr, s.job)
	if err != nil {
		// 回滚
		s.cronStr, s.zone, s.enabled = oldCron, oldZone, oldEnabled
		s.cron = oldScheduler
		s.entryID = oldEntry
		s.lastErr = err.Error()
		return fmt.Errorf("cron 表达式无效: %v", err)
	}
	c.Start()
	s.cron = c
	s.entryID = id
	s.enabled = true
	s.cronStr = expr
	s.zone = set.Zone
	s.lastErr = ""
	logf("定时任务已更新: %s (%s)", expr, set.Zone)
	return nil
}

// nextRunLocked 是 NextRun 的无锁实现。
// sync.Mutex 不可重入：Snapshot 已持锁，若直接调 NextRun 会死锁并挂起整个请求。
func (s *Scheduler) nextRunLocked() time.Time {
	if s.cron == nil || !s.enabled {
		return time.Time{}
	}
	entries := s.cron.Entries()
	for _, e := range entries {
		if e.ID == s.entryID {
			return e.Next
		}
	}
	if len(entries) > 0 {
		return entries[0].Next
	}
	return time.Time{}
}

// NextRun 返回下次触发时间，未启用或未调度时返回零值。
func (s *Scheduler) NextRun() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextRunLocked()
}

// Snapshot 供面板展示当前状态。
func (s *Scheduler) Snapshot() ScheduleSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := ScheduleSettings{
		Enabled:  s.enabled,
		Cron:     s.cronStr,
		Zone:     s.zone,
		Describe: describeCron(s.cronStr),
	}
	if next := s.nextRunLocked(); !next.IsZero() {
		out.NextRun = next.Format(time.RFC3339)
	}
	return out
}

func (s *Scheduler) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron != nil {
		s.cron.Stop()
	}
}

// SetMoviePilot 配置 MoviePilot 站点自动同步；传空 URL 或 Enabled=false 则停用。
func (s *Scheduler) SetMoviePilot(cfg MoviePilotSyncConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.mpCron != nil {
		s.mpCron.Stop()
		s.mpCron = nil
		s.mpEntryID = 0
	}
	s.mpCfg = cfg

	if !cfg.Enabled || strings.TrimSpace(cfg.URL) == "" || s.mpJob == nil {
		logf("MoviePilot 自动同步未启用")
		return
	}
	loc, err := time.LoadLocation(s.zone)
	if err != nil {
		loc = time.Local
	}
	expr := normalizeCron(cfg.Cron)
	if _, err := cronParser.Parse(expr); err != nil {
		logf("MoviePilot 同步表达式无效，已跳过: %v", err)
		return
	}
	c := cron.New(cron.WithSeconds(), cron.WithLocation(loc))
	cfgCopy := cfg
	id, err := c.AddFunc(expr, func() { s.mpJob(cfgCopy) })
	if err != nil {
		logf("MoviePilot 同步任务注册失败: %v", err)
		return
	}
	c.Start()
	s.mpCron = c
	s.mpEntryID = id
	logf("MoviePilot 自动同步已注册: %s", expr)
}

// SetMoviePilotJob 注入同步动作（由 main 提供，避免 schedule.go 依赖站点表）。
func (s *Scheduler) SetMoviePilotJob(job func(MoviePilotSyncConfig)) {
	s.mu.Lock()
	s.mpJob = job
	s.mu.Unlock()
}

// StopMoviePilot 停止自动同步任务。
func (s *Scheduler) StopMoviePilot() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mpCron != nil {
		s.mpCron.Stop()
		s.mpCron = nil
	}
}
