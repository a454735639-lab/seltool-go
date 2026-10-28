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
	"regexp"
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

// dataImageMimeRe 限定 data URI 的 MIME 前缀。
//
// 只校验逗号后的 base64 是不够的：攻击者可以构造
//
//	data:x"><img src=x onerror=alert(1)>;base64,<合法 PNG>
//
// 解码校验能通过，但整串被前端拼进 innerHTML 后就是可执行脚本（同源，可读走
// panelKey / webhook / CookieCloud KEY）。因此 MIME 部分必须白名单校验，
// 且不放开 svg（可内嵌脚本）。
var dataImageMimeRe = regexp.MustCompile(`^data:image/(png|jpeg|jpg|gif|webp|x-icon|vnd\.microsoft\.icon);base64,`)

// maxIconDataURI 限制单条图标体积，避免超长字符串撑爆内存/配置。
const maxIconDataURI = 4 << 20 // 4MiB

// looksLikeImage 判断 data URI 里的内容是否真的是图片。
//
// MoviePilot 的 siteicon 表里混进了抓取失败时保存的 HTML 错误页
// （如 Cloudflare 的 403 Forbidden），这类条目直接丢弃，避免前端拿到破图。
func looksLikeImage(dataURI string) bool {
	if len(dataURI) > maxIconDataURI {
		return false
	}
	if !dataImageMimeRe.MatchString(dataURI) {
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
	// 不再接受「MIME 说是图片、内容却是 SVG」的错配条目：SVG 可内嵌脚本。
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
	dropped := 0
	for k, v := range f.Icons {
		key := normalizeIconKey(k)
		if key == "" || v == "" {
			continue
		}
		// 与构造函数走同一套校验：只判空会让 HTML 错误页、非图片 data URI
		// 直接落盘，前端拼进 innerHTML 后就是可执行内容。
		if !looksLikeImage(v) {
			dropped++
			continue
		}
		clean[key] = v
	}
	if len(clean) == 0 {
		return 0, fmt.Errorf("没有有效的图标条目")
	}
	if dropped > 0 {
		logf("图标导入：已丢弃 %d 条非图片或超限的条目", dropped)
	}
	f.Icons = clean
	if err := writeJSONAtomic(t.path, f, 0o644); err != nil {
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
	// 与其他配置文件一致走原子写：中途写坏会让整张图标表回退到内置表。
	return writeJSONAtomic(t.path, f, 0o644)
}
