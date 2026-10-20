package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

func logf(format string, args ...any) {
	log.Printf(format, args...)
}

// writeFileAtomic 先写临时文件再 rename，避免进程中断留下半截配置。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func writeJSONAtomic(path string, v any, perm os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data, perm)
}

// RunRecord 与 Java 版 TargetRunRecord 字段一致。
type RunRecord struct {
	ExecutedAt string `json:"executedAt"`
	Success    bool   `json:"success"`
	Message    string `json:"message"`
}

const maxHistorySize = 7

// History 保存每个目标最近 7 条运行结果。
type History struct {
	mu      sync.Mutex
	file    string
	records map[string][]RunRecord
}

func NewHistory(file string) *History {
	return &History{file: absPath(file), records: map[string][]RunRecord{}}
}

func (h *History) Load() {
	data, err := os.ReadFile(h.file)
	if err != nil {
		if !os.IsNotExist(err) {
			logf("无法读取目标运行记录文件 %s，将使用空记录: %v", h.file, err)
		}
		return
	}
	var loaded map[string][]RunRecord
	if err := json.Unmarshal(data, &loaded); err != nil {
		logf("无法读取目标运行记录文件 %s，将使用空记录: %v", h.file, err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = map[string][]RunRecord{}
	for url, list := range loaded {
		h.records[url] = trimRecords(list)
	}
}

func (h *History) Record(targetURL string, success bool, message string) {
	if targetURL == "" {
		return
	}
	h.mu.Lock()
	list := append([]RunRecord{{
		ExecutedAt: time.Now().Format("2006-01-02T15:04:05-07:00"),
		Success:    success,
		Message:    message,
	}}, h.records[targetURL]...)
	h.records[targetURL] = trimRecords(list)
	snapshot := make(map[string][]RunRecord, len(h.records))
	for k, v := range h.records {
		snapshot[k] = v
	}
	h.mu.Unlock()

	if err := writeJSONAtomic(h.file, snapshot, 0o644); err != nil {
		logf("无法保存目标运行记录文件 %s: %v", h.file, err)
	}
}

// Prune 删除不在 keep 里的 URL 记录，返回清理的 URL 数。
//
// 目标被删除后其历史记录原本会永久留在文件里（trimRecords 只限每 URL 条数，
// 不限 URL 数量），长期运行会让文件持续膨胀，也会让 Get() 返回已删除目标的数据。
func (h *History) Prune(keep map[string]bool) int {
	h.mu.Lock()
	removed := 0
	for url := range h.records {
		if !keep[url] {
			delete(h.records, url)
			removed++
		}
	}
	if removed == 0 {
		h.mu.Unlock()
		return 0
	}
	snapshot := make(map[string][]RunRecord, len(h.records))
	for k, v := range h.records {
		snapshot[k] = v
	}
	h.mu.Unlock()

	if err := writeJSONAtomic(h.file, snapshot, 0o644); err != nil {
		logf("清理历史记录后写盘失败 %s: %v", h.file, err)
	}
	return removed
}

func (h *History) Get(targetURL string) []RunRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	list := h.records[targetURL]
	out := make([]RunRecord, len(list))
	copy(out, list)
	return out
}

func (h *History) Latest() map[string]RunRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	latest := make(map[string]RunRecord, len(h.records))
	for url, list := range h.records {
		if len(list) > 0 {
			latest[url] = list[0]
		}
	}
	return latest
}

func trimRecords(list []RunRecord) []RunRecord {
	if len(list) > maxHistorySize {
		return list[:maxHistorySize]
	}
	return list
}

func absPath(p string) string {
	if p == "" {
		return p
	}
	if filepath.IsAbs(p) {
		return p
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

// AlertState 收集本轮任务中的失败目标与启动告警，供 webhook 汇总。
type AlertState struct {
	mu       sync.Mutex
	failures []string
	warnings []string
}

func NewAlertState() *AlertState { return &AlertState{} }

func (a *AlertState) AddTargetFailure(line string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failures = append(a.failures, line)
}

func (a *AlertState) AddStartupWarning(line string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.warnings = append(a.warnings, line)
}

func (a *AlertState) DrainTargetFailures() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.failures
	a.failures = nil
	return out
}

func (a *AlertState) DrainStartupWarnings() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.warnings
	a.warnings = nil
	return out
}

func (a *AlertState) StartupWarningText() string {
	warnings := a.DrainStartupWarnings()
	if len(warnings) == 0 {
		return ""
	}
	text := ""
	for _, w := range warnings {
		text += "\n- " + w
	}
	return fmt.Sprintf("启动告警:%s", text)
}
