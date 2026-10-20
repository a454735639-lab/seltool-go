package main

// 站点图标：从 MoviePilot 的 siteicon 表导出，格式为 data URI（data:image/ico;base64,...）。
//
// 图标按域名存一份，前端按目标的注册域名查找。内置一份（assets/pt-icons.json），
// 用户可通过 config/pt-icons.json 覆盖，与站点名表的行为保持一致。

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ptIconsFile 是落盘格式，故意不含锁，便于序列化。
type ptIconsFile struct {
	Version string            `json:"version"`
	Source  string            `json:"source,omitempty"`
	Icons   map[string]string `json:"icons"`
}

// PTIconTable 管理域名到图标 data URI 的映射。
type PTIconTable struct {
	mu    sync.RWMutex
	path  string
	icons map[string]string
}

// NewPTIconTable 用内置数据初始化，再用用户文件（若存在且合法）覆盖。
func NewPTIconTable(userPath string, builtin []byte) *PTIconTable {
	t := &PTIconTable{path: userPath, icons: map[string]string{}}
	if len(builtin) > 0 {
		var f ptIconsFile
		if err := json.Unmarshal(builtin, &f); err == nil {
			for k, v := range f.Icons {
				if k = normalizeIconKey(k); k != "" && v != "" && looksLikeImage(v) {
					t.icons[k] = v
				}
			}
		} else {
			logf("内置站点图标解析失败: %v", err)
		}
	}
	if strings.TrimSpace(userPath) != "" {
		if data, err := os.ReadFile(userPath); err == nil {
			var f ptIconsFile
			if err := json.Unmarshal(data, &f); err == nil {
				for k, v := range f.Icons {
					if k = normalizeIconKey(k); k != "" && v != "" && looksLikeImage(v) {
						t.icons[k] = v
					}
				}
				logf("已加载用户站点图标 %s：%d 个", userPath, len(t.icons))
			} else {
				logf("用户站点图标解析失败 %s: %v（回退内置）", userPath, err)
			}
		}
	}
	return t
}

// Lookup 按域名查找图标；支持 www 前缀与注册域名两级回退。
func (t *PTIconTable) Lookup(domain string) string {
	if t == nil {
		return ""
	}
	key := normalizeIconKey(domain)
	if key == "" {
		return ""
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if v, ok := t.icons[key]; ok {
		return v
	}
	// 去掉 www 再试一次
	if bare := strings.TrimPrefix(key, "www."); bare != key {
		if v, ok := t.icons[bare]; ok {
			return v
		}
	}
	// 按注册域名再试一次（如 pt.gtkpw.xyz → gtkpw.xyz）
	if reg := registeredDomain(key); reg != "" && reg != key {
		if v, ok := t.icons[reg]; ok {
			return v
		}
	}
	return ""
}

// All 返回图标表的副本，供接口输出。
func (t *PTIconTable) All() map[string]string {
	if t == nil {
		return map[string]string{}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]string, len(t.icons))
	for k, v := range t.icons {
		out[k] = v
	}
	return out
}

// Count 返回图标数量。
func (t *PTIconTable) Count() int {
	if t == nil {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.icons)
}

// looksLikeImage 判断 data URI 里的内容是否真的是图片。
//
// MoviePilot 的 siteicon 表里混进了抓取失败时保存的 HTML 错误页
// （如 Cloudflare 的 403 Forbidden），这类条目直接丢弃，避免前端拿到破图。
func looksLikeImage(dataURI string) bool {
	if !strings.HasPrefix(dataURI, "data:") {
		return false
	}
	idx := strings.Index(dataURI, ",")
	if idx < 0 || idx+1 >= len(dataURI) {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(dataURI[idx+1:])
	if err != nil {
		// 兼容无填充的 base64
		raw, err = base64.RawStdEncoding.DecodeString(dataURI[idx+1:])
		if err != nil {
			return false
		}
	}
	if len(raw) < 16 {
		return false
	}
	switch {
	case bytes.HasPrefix(raw, []byte("\x00\x00\x01\x00")): // ICO
		return true
	case bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")): // PNG
		return true
	case bytes.HasPrefix(raw, []byte("\xff\xd8")): // JPEG
		return true
	case bytes.HasPrefix(raw, []byte("GIF8")): // GIF
		return true
	case bytes.HasPrefix(raw, []byte("RIFF")) && len(raw) > 12 && bytes.HasPrefix(raw[8:], []byte("WEBP")):
		return true
	}
	head := raw
	if len(head) > 256 {
		head = head[:256]
	}
	if bytes.Contains(bytes.ToLower(head), []byte("<svg")) {
		return true
	}
	return false
}

func normalizeIconKey(v string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(v)), ".")
}

// Replace 用给定 JSON 覆盖图标表并落盘，返回图标数量。
func (t *PTIconTable) Replace(raw []byte) (int, error) {
	var f ptIconsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, fmt.Errorf("JSON 格式错误: %w", err)
	}
	if len(f.Icons) == 0 {
		return 0, fmt.Errorf("图标表为空")
	}
	clean := make(map[string]string, len(f.Icons))
	for k, v := range f.Icons {
		key := normalizeIconKey(k)
		if key == "" || v == "" {
			continue
		}
		clean[key] = v
	}
	if len(clean) == 0 {
		return 0, fmt.Errorf("没有有效的图标条目")
	}
	f.Icons = clean
	data, err := json.Marshal(f)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return 0, err
	}
	if err := os.WriteFile(t.path, data, 0o644); err != nil {
		return 0, err
	}
	t.mu.Lock()
	t.icons = clean
	t.mu.Unlock()
	return len(clean), nil
}

// Reset 删除用户图标文件并回退到内置表。
func (t *PTIconTable) Reset() (int, error) {
	_ = os.Remove(t.path)
	built := &PTIconTable{}
	if len(builtinPTIconsJSON) > 0 {
		var f ptIconsFile
		if err := json.Unmarshal(builtinPTIconsJSON, &f); err == nil {
			for k, v := range f.Icons {
				if k = normalizeIconKey(k); k != "" && v != "" {
					built.icons[k] = v
				}
			}
		}
	}
	t.mu.Lock()
	t.icons = built.icons
	n := len(t.icons)
	t.mu.Unlock()
	return n, nil
}

// Set 写入单个图标（内存），需再调 Save 落盘。
func (t *PTIconTable) Set(domain, dataURI string) {
	key := normalizeIconKey(domain)
	if key == "" || dataURI == "" {
		return
	}
	t.mu.Lock()
	if t.icons == nil {
		t.icons = map[string]string{}
	}
	t.icons[key] = dataURI
	t.mu.Unlock()
}

// Save 把当前图标表写入用户文件（config/pt-icons.json）。
func (t *PTIconTable) Save() error {
	t.mu.RLock()
	snapshot := make(map[string]string, len(t.icons))
	for k, v := range t.icons {
		snapshot[k] = v
	}
	t.mu.RUnlock()

	f := ptIconsFile{Version: time.Now().Format("2006-01-02 15:04"), Source: "MoviePilot 自动同步 + 手工补充", Icons: snapshot}
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(t.path, data, 0o644)
}
