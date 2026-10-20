package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// 上次同步信息记录在缓存文件旁的小文件里，避免改动 CookieCloud 缓存格式。
type syncRecord struct {
	At      string `json:"at"`
	Domains int    `json:"domains"`
	Cookies int    `json:"cookies"`
}

var (
	syncMu   sync.RWMutex
	syncRec  syncRecord
	syncFile = "./config/cookiecloud-sync.json"
)

// syncCookiesOK 记录一次成功同步。
func syncCookiesOK(data map[string][]Cookie) {
	total := 0
	for _, list := range data {
		total += len(list)
	}
	rec := syncRecord{
		At:      time.Now().Format(time.RFC3339),
		Domains: len(data),
		Cookies: total,
	}
	syncMu.Lock()
	syncRec = rec
	syncMu.Unlock()
	// 写失败要留下日志：否则「上次同步时间」会一直显示旧值，
	// 而用户以为同步没生效，排查时看不到任何线索。
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		logf("序列化同步记录失败: %v", err)
		return
	}
	if err := writeFileAtomic(absPath(syncFile), b, 0o644); err != nil {
		logf("保存同步记录失败 %s: %v（上次同步时间可能显示为旧值）", syncFile, err)
	}
}

// lastSyncInfo 返回上次同步时间、域名数与 Cookie 数。
// 文件不存在时回退到 CookieCloud 缓存文件的修改时间。
func lastSyncInfo() (string, int, int) {
	syncMu.RLock()
	rec := syncRec
	syncMu.RUnlock()

	if rec.At == "" {
		if b, err := os.ReadFile(absPath(syncFile)); err == nil {
			var r syncRecord
			if json.Unmarshal(b, &r) == nil && r.At != "" {
				rec = r
			}
		}
	}
	if rec.At != "" {
		return rec.At, rec.Domains, rec.Cookies
	}
	// 回退：用缓存文件的 mtime
	if fi, err := os.Stat(absPath("./config/cookiecloud-cache.json")); err == nil {
		return fi.ModTime().Format(time.RFC3339), 0, 0
	}
	return "", 0, 0
}

// loadSyncRecord 启动时读入上次同步记录。
func loadSyncRecord() {
	if b, err := os.ReadFile(absPath(syncFile)); err == nil {
		var r syncRecord
		if json.Unmarshal(b, &r) == nil {
			syncMu.Lock()
			syncRec = r
			syncMu.Unlock()
		}
	}
}
