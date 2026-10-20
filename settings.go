package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// CookieCloudSettings 是允许在 Web 面板里修改、并立即生效的 CookieCloud 配置。
// 单独存文件，避免改动 custom.yml 后需要重启。
type CookieCloudSettings struct {
	URL      string `json:"url"`
	Key      string `json:"key"`
	Password string `json:"password"`
}

type SettingsStore struct {
	mu   sync.RWMutex
	file string
	cur  CookieCloudSettings
}

func NewSettingsStore(file string) *SettingsStore {
	return &SettingsStore{file: absPath(file)}
}

// Load 读取运行时设置。ok=true 表示「设置文件存在且有效」，即使 URL 为空：
// 空 URL 是用户主动禁用 CookieCloud，调用方不能因此回退到 yaml/env 把远端重新启用。
func (s *SettingsStore) Load() (CookieCloudSettings, bool, error) {
	data, err := os.ReadFile(s.file)
	if err != nil {
		if os.IsNotExist(err) {
			return CookieCloudSettings{}, false, nil
		}
		return CookieCloudSettings{}, false, fmt.Errorf("无法读取 CookieCloud 设置: %w", err)
	}
	var saved CookieCloudSettings
	if err := json.Unmarshal(data, &saved); err != nil {
		return CookieCloudSettings{}, false, fmt.Errorf("无法解析 CookieCloud 设置: %w", err)
	}
	saved.URL = strings.TrimRight(strings.TrimSpace(saved.URL), "/")
	saved.Key = strings.TrimSpace(saved.Key)
	s.mu.Lock()
	s.cur = saved
	s.mu.Unlock()
	return saved, true, nil
}

func (s *SettingsStore) Current() CookieCloudSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// Save 写入设置。password 为空表示沿用已有密码，方便前端不回显敏感值。
func (s *SettingsStore) Save(in CookieCloudSettings) (CookieCloudSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	in.URL = strings.TrimRight(strings.TrimSpace(in.URL), "/")
	in.Key = strings.TrimSpace(in.Key)
	if in.Password == "" {
		in.Password = s.cur.Password
	}
	if in.URL != "" {
		if !strings.HasPrefix(in.URL, "http://") && !strings.HasPrefix(in.URL, "https://") {
			return CookieCloudSettings{}, fmt.Errorf("服务器地址必须以 http:// 或 https:// 开头")
		}
	}

	if err := writeJSONAtomic(s.file, in, 0o600); err != nil {
		return CookieCloudSettings{}, fmt.Errorf("无法保存 CookieCloud 设置: %w", err)
	}
	s.cur = in
	return in, nil
}
