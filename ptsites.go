package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// PTSiteTable 是站点中文名对照表，可由面板更新，不必重新编译。
//
// 数据来源：assets/pt-sites.json（内置基线，对齐 MoviePilot 的站点索引）
// 用户更新后写入 config/pt-sites.json，优先于内置基线。
type PTSiteTable struct {
	Version string            `json:"version"`
	Source  string            `json:"source,omitempty"`
	Sites   map[string]string `json:"sites"`

	mu   sync.RWMutex
	file string
}

// 内置基线 builtinPTSitesJSON 由 app.go 的 go:embed 提供

func NewPTSiteTable(configFile string) *PTSiteTable {
	t := &PTSiteTable{file: absPath(configFile)}
	t.load()
	return t
}

// load 读取站点表：用户文件优先，缺失或损坏时回退内置基线。
func (t *PTSiteTable) load() {
	// 用户更新过的表优先
	if data, err := os.ReadFile(t.file); err == nil && len(data) > 0 {
		var tbl PTSiteTable
		if err := json.Unmarshal(data, &tbl); err == nil && len(tbl.Sites) > 0 {
			t.mu.Lock()
			t.Version, t.Source, t.Sites = tbl.Version, tbl.Source, tbl.Sites
			t.mu.Unlock()
			logf("已加载自定义站点表：%s（%d 个站点）", tbl.Version, len(tbl.Sites))
			return
		}
		logf("站点表文件 %s 解析失败，回退内置表", t.file)
	}

	// 内置基线
	var tbl PTSiteTable
	if err := json.Unmarshal(builtinPTSitesJSON, &tbl); err != nil {
		logf("内置站点表解析失败: %v", err)
		t.mu.Lock()
		t.Sites = map[string]string{}
		t.mu.Unlock()
		return
	}
	t.mu.Lock()
	t.Version, t.Source, t.Sites = tbl.Version, tbl.Source, tbl.Sites
	t.mu.Unlock()
	logf("已加载内置站点表：%s（%d 个站点）", tbl.Version, len(tbl.Sites))
}

// Lookup 返回站点中文名；未收录时返回空串。
func (t *PTSiteTable) Lookup(host string) string {
	if t == nil {
		return ""
	}
	bare := strings.ToLower(strings.TrimSpace(host))
	bare = strings.TrimPrefix(bare, "www.")
	t.mu.RLock()
	defer t.mu.RUnlock()
	if n, ok := t.Sites[bare]; ok {
		return n
	}
	// 二级域名也试一次（如 kp.m-team.cc → m-team.cc）
	labels := strings.Split(bare, ".")
	for i := 1; i < len(labels)-1; i++ {
		if n, ok := t.Sites[strings.Join(labels[i:], ".")]; ok {
			return n
		}
	}
	return ""
}

// Snapshot 返回当前表内容，供面板展示。
func (t *PTSiteTable) Snapshot() (version, source string, count int) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Version, t.Source, len(t.Sites)
}

// All 返回站点表的浅拷贝。
func (t *PTSiteTable) All() map[string]string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]string, len(t.Sites))
	for k, v := range t.Sites {
		out[k] = v
	}
	return out
}

// Replace 用面板提交的内容替换站点表；校验通过才落盘。
//
// 接受两种格式：
//  1. {"version":"...","sites":{"hddolby.com":"高清杜比",...}}
//  2. {"hddolby.com":"高清杜比",...}   直接是映射
func (t *PTSiteTable) Replace(raw []byte) (version string, count int, err error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "", 0, fmt.Errorf("内容为空")
	}

	var tbl PTSiteTable
	if err := json.Unmarshal(raw, &tbl); err != nil || len(tbl.Sites) == 0 {
		// 尝试直接映射格式
		var direct map[string]string
		if err2 := json.Unmarshal(raw, &direct); err2 != nil || len(direct) == 0 {
			return "", 0, fmt.Errorf("JSON 格式不正确：需要 {\"sites\":{\"域名\":\"名称\"}} 或直接是域名到名称的映射")
		}
		tbl.Sites = direct
		tbl.Version = "自定义"
	}

	// 规范化：域名小写去 www.，名称去空白
	clean := make(map[string]string, len(tbl.Sites))
	for k, v := range tbl.Sites {
		host := strings.ToLower(strings.TrimSpace(k))
		host = strings.TrimPrefix(host, "www.")
		name := strings.TrimSpace(v)
		if host == "" || name == "" {
			continue
		}
		clean[host] = name
	}
	if len(clean) == 0 {
		return "", 0, fmt.Errorf("没有有效的站点条目")
	}
	if tbl.Version == "" {
		tbl.Version = "自定义"
	}

	// 落盘与内存态分开：PTSiteTable 内嵌 sync.RWMutex，
	// 直接对它做 json.Marshal 会复制锁（go vet 报 copies lock value，
	// 且复制锁是未定义行为）。这里用不含锁的纯数据结构序列化。
	out := PTSiteTable{Version: tbl.Version, Source: tbl.Source, Sites: clean}
	data, err := json.MarshalIndent(ptSiteTableFile{
		Version: out.Version,
		Source:  out.Source,
		Sites:   out.Sites,
	}, "", "  ")
	if err != nil {
		return "", 0, err
	}
	if err := writeFileAtomic(t.file, data, 0o644); err != nil {
		return "", 0, fmt.Errorf("写入失败: %w", err)
	}

	t.mu.Lock()
	t.Version, t.Source, t.Sites = out.Version, out.Source, out.Sites
	t.mu.Unlock()
	logf("站点表已更新：%s（%d 个站点）", out.Version, len(out.Sites))
	return out.Version, len(out.Sites), nil
}

// Reset 删除自定义表，回退到内置基线。
func (t *PTSiteTable) Reset() error {
	if err := os.Remove(t.file); err != nil && !os.IsNotExist(err) {
		return err
	}
	t.load()
	return nil
}

// ptSiteTableFile 是站点表落盘用的纯数据结构。
//
// 单独定义的原因：PTSiteTable 内嵌 sync.RWMutex，直接序列化会复制锁。
// 该结构不含任何锁，json 字段与 PTSiteTable 保持一致，保证文件格式不变。
type ptSiteTableFile struct {
	Version string            `json:"version"`
	Source  string            `json:"source,omitempty"`
	Sites   map[string]string `json:"sites"`
}
