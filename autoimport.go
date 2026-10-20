package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// 包级引用，由 main 注入；自动导入新站后用于补图标。
var (
	mpSyncStore *MoviePilotSyncStore
	ptIconTable *PTIconTable
)

// AutoImportSettings 控制「CookieCloud 里出现新 PT 站时是否自动加入目标列表」。
type AutoImportSettings struct {
	Enabled bool `json:"enabled"`
	// Notify 为 true 时，自动导入了站点会发一条企业微信通知
	Notify bool `json:"notify"`
}

type AutoImportStore struct {
	mu   sync.RWMutex
	file string
	cur  AutoImportSettings
}

func NewAutoImportStore(file string) *AutoImportStore {
	return &AutoImportStore{file: absPath(file)}
}

func (s *AutoImportStore) Load() (AutoImportSettings, bool, error) {
	data, err := os.ReadFile(s.file)
	if err != nil {
		if os.IsNotExist(err) {
			return AutoImportSettings{}, false, nil
		}
		return AutoImportSettings{}, false, err
	}
	var c AutoImportSettings
	if err := json.Unmarshal(data, &c); err != nil {
		return AutoImportSettings{}, false, err
	}
	s.mu.Lock()
	s.cur = c
	s.mu.Unlock()
	return c, true, nil
}

func (s *AutoImportStore) Current() AutoImportSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

func (s *AutoImportStore) Save(in AutoImportSettings) (AutoImportSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeJSONAtomic(s.file, in, 0o644); err != nil {
		return AutoImportSettings{}, err
	}
	s.cur = in
	return in, nil
}

// AutoImportResult 是一次自动导入的结果，供日志与通知使用。
type AutoImportResult struct {
	Added   []string // 新增站点的显示名
	Skipped int      // 已存在而跳过的数量
}

func (r AutoImportResult) Summary() string {
	if len(r.Added) == 0 {
		return ""
	}
	names := r.Added
	if len(names) > 8 {
		names = append(names[:8], fmt.Sprintf("… 共 %d 个", len(r.Added)))
	}
	return strings.Join(names, "、")
}

// RunAutoImport 从 CookieCloud 数据里挑出 PT 站，把目标列表里还没有的自动加入。
//
// 这是「CookieCloud 出现新站 → 自动加入」的唯一实现，手动同步与每日自动同步共用，
// 保证两条路径行为一致。去重沿用注册域名规则（www 与 apex 视为同一站）。
func RunAutoImport(data map[string][]Cookie, store *TargetStore) (AutoImportResult, error) {
	var out AutoImportResult
	if store == nil || len(data) == 0 {
		return out, nil
	}

	domains := detectPTSites(data)
	if len(domains) == 0 {
		return out, nil
	}

	existing := store.Get()
	incoming, _ := BuildImportTargets(domains, existing, data)
	if len(incoming) == 0 {
		return out, nil
	}

	added, skipped := mergeTargets(incoming, existing)
	out.Skipped = skipped
	if len(added) == 0 {
		return out, nil
	}

	merged := append(append([]Target{}, existing...), added...)
	if _, err := store.Save(merged); err != nil {
		return out, fmt.Errorf("保存目标失败: %w", err)
	}
	for _, t := range added {
		out.Added = append(out.Added, t.DisplayName())
	}

	// 新导入的站点补图标：复用已保存的 MoviePilot 配置，失败不影响导入结果。
	if mpSyncStore != nil && ptIconTable != nil {
		if cfg, ok, err := mpSyncStore.Load(); err == nil && ok && cfg.URL != "" && cfg.Token != "" {
			if n, ierr := SyncIconsFromMoviePilot(cfg, ptIconTable); ierr != nil {
				logf("自动导入后同步图标失败: %v", ierr)
			} else if n > 0 {
				logf("自动导入后补充 %d 个站点图标", n)
			}
		}
	}
	return out, nil
}
