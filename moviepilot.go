package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// MoviePilotSyncConfig 是从 MoviePilot 拉取站点名的配置。
//
// 认证契约（对齐 MoviePilot 的 verify_apikey）：
//   - 请求头 X-API-KEY: <API_TOKEN>
//   - 或查询参数 ?apikey=<API_TOKEN>
//
// API_TOKEN 取自 MoviePilot 的系统设置（全局一个，不是每用户）。
type MoviePilotSyncConfig struct {
	URL     string `json:"url"`     // 形如 http://10.0.0.40:3000
	Token   string `json:"token"`   // API_TOKEN，面板不回显
	Enabled bool   `json:"enabled"` // 是否参与每日自动同步
	Cron    string `json:"cron"`    // 自动同步的 cron，默认 0 0 5 * * *
}

type MoviePilotSyncStore struct {
	mu   sync.RWMutex
	file string
	cur  MoviePilotSyncConfig
}

func NewMoviePilotSyncStore(file string) *MoviePilotSyncStore {
	return &MoviePilotSyncStore{file: absPath(file)}
}

func (s *MoviePilotSyncStore) Load() (MoviePilotSyncConfig, bool, error) {
	data, err := os.ReadFile(s.file)
	if err != nil {
		if os.IsNotExist(err) {
			return MoviePilotSyncConfig{}, false, nil
		}
		return MoviePilotSyncConfig{}, false, err
	}
	var c MoviePilotSyncConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return MoviePilotSyncConfig{}, false, err
	}
	s.mu.Lock()
	s.cur = c
	s.mu.Unlock()
	return c, true, nil
}

func (s *MoviePilotSyncStore) Current() MoviePilotSyncConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// Save 保存配置；token 为空表示沿用已有值（面板不回显密钥）。
func (s *MoviePilotSyncStore) Save(in MoviePilotSyncConfig) (MoviePilotSyncConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	in.URL = strings.TrimRight(strings.TrimSpace(in.URL), "/")
	in.Token = strings.TrimSpace(in.Token)
	in.Cron = strings.TrimSpace(in.Cron)
	if in.Cron == "" {
		in.Cron = "0 0 5 * * *"
	}
	if in.Token == "" {
		in.Token = s.cur.Token
	}
	if in.Enabled && in.URL == "" {
		return MoviePilotSyncConfig{}, fmt.Errorf("启用自动同步前需要填写 MoviePilot 地址")
	}
	if in.Enabled && in.Token == "" {
		return MoviePilotSyncConfig{}, fmt.Errorf("启用自动同步前需要填写 API Token")
	}
	if err := writeJSONAtomic(s.file, in, 0o600); err != nil {
		return MoviePilotSyncConfig{}, err
	}
	s.cur = in
	return in, nil
}

// mask 返回给面板的安全视图，不含 token 明文。
func (s MoviePilotSyncConfig) mask() map[string]any {
	return map[string]any{
		"url":      s.URL,
		"enabled":  s.Enabled,
		"cron":     s.Cron,
		"hasToken": strings.TrimSpace(s.Token) != "",
	}
}

// moviePilotSite 是 /api/v1/site/ 返回的单个站点（只取需要的字段）。
type moviePilotSite struct {
	Name   string `json:"name"`
	Domain string `json:"domain"`
	URL    string `json:"url"`
}

// FetchMoviePilotSites 调用 MoviePilot 的站点接口拉取站点列表。
func FetchMoviePilotSites(cfg MoviePilotSyncConfig) (map[string]string, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("未配置 MoviePilot 地址")
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("未配置 API Token")
	}

	endpoint := strings.TrimRight(cfg.URL, "/") + "/api/v1/site/"

	// 容器内注入了 HTTP_PROXY，这里显式禁用代理走直连；
	// 局域网地址（如 10.0.0.40）经代理会被转走导致超时。
	client := &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
		},
	}

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	// MoviePilot 的 verify_apikey 优先读请求头，其次读查询参数
	req.Header.Set("X-API-KEY", cfg.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "seltool-go")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接 MoviePilot 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("认证失败（HTTP 401）：API Token 无效，请在 MoviePilot 设置→系统里确认 API_TOKEN")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("MoviePilot 返回 HTTP %d", resp.StatusCode)
	}

	// 响应可能是 {success,data:[...]} 或直接是数组，两种都兼容
	var envelope struct {
		Success bool              `json:"success"`
		Message string            `json:"message"`
		Data    []json.RawMessage `json:"data"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	var sites []moviePilotSite
	if err := json.Unmarshal(raw, &sites); err != nil {
		// 试信封格式
		if err2 := json.Unmarshal(raw, &envelope); err2 != nil {
			return nil, fmt.Errorf("解析响应失败: %v", err)
		}
		if !envelope.Success && envelope.Message != "" {
			return nil, fmt.Errorf("MoviePilot 返回错误: %s", envelope.Message)
		}
		for _, item := range envelope.Data {
			var s moviePilotSite
			if json.Unmarshal(item, &s) == nil {
				sites = append(sites, s)
			}
		}
	}

	out := map[string]string{}
	for _, s := range sites {
		host := strings.ToLower(strings.TrimSpace(s.Domain))
		if host == "" {
			// 没有 domain 字段时从 url 兜底
			host = registeredDomain(s.URL)
		}
		name := strings.TrimSpace(s.Name)
		if host == "" || name == "" {
			continue
		}
		out[strings.TrimPrefix(host, "www.")] = name
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("MoviePilot 返回了 0 个站点，请确认它已配置站点")
	}
	return out, nil
}

// SyncFromMoviePilot 拉取 MoviePilot 站点并写入本地站点表，返回站点数。
func SyncFromMoviePilot(cfg MoviePilotSyncConfig, table *PTSiteTable) (int, error) {
	sites, err := FetchMoviePilotSites(cfg)
	if err != nil {
		return 0, err
	}
	payload := map[string]any{
		"version": "MoviePilot " + time.Now().Format("2006-01-02 15:04"),
		"source":  "同步自 " + cfg.URL,
		"sites":   sites,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	if _, n, err := table.Replace(raw); err != nil {
		return 0, err
	} else {
		return n, nil
	}
}

// mpSiteEntry 是 /api/v1/site/ 返回的单个站点条目（只取需要的字段）。
type mpSiteEntry struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Domain string `json:"domain"`
	URL    string `json:"url"`
}

// fetchMoviePilotSiteList 拉取站点列表（含 id，图标接口需要）。
func fetchMoviePilotSiteList(cfg MoviePilotSyncConfig) ([]mpSiteEntry, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("未配置 MoviePilot 地址")
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("未配置 API Token")
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}}
	endpoint := strings.TrimRight(cfg.URL, "/") + "/api/v1/site/"
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-KEY", cfg.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "seltool-go")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接 MoviePilot 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("认证失败（HTTP 401）：API Token 无效")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("MoviePilot 返回 HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	var entries []mpSiteEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		// 兼容 {data:[...]} 包裹形式
		var wrap struct {
			Data []mpSiteEntry `json:"data"`
		}
		if err2 := json.Unmarshal(raw, &wrap); err2 != nil {
			return nil, fmt.Errorf("解析站点列表失败: %w", err)
		}
		entries = wrap.Data
	}
	return entries, nil
}

// fetchMoviePilotIcon 取单个站点图标，返回 data URI。失败返回空串。
func fetchMoviePilotIcon(cfg MoviePilotSyncConfig, siteID int) string {
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: nil}}
	endpoint := fmt.Sprintf("%s/api/v1/site/icon/%d", strings.TrimRight(cfg.URL, "/"), siteID)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("X-API-KEY", cfg.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "seltool-go")

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return ""
	}
	// MoviePilot 的实际响应是 {"success":true,"data":{"icon":"data:image/ico;base64,..."}}
	var payload struct {
		Data struct {
			Icon string `json:"icon"`
			URL  string `json:"url"`
		} `json:"data"`
		// 兼容扁平形式
		Base64 string `json:"base64"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	icon := payload.Data.Icon
	if icon == "" {
		icon = payload.Base64
	}
	if icon != "" {
		// 已带 data: 前缀就原样用，否则按 ico 补前缀
		if strings.HasPrefix(icon, "data:") {
			return icon
		}
		return "data:image/ico;base64," + icon
	}
	if payload.Data.URL != "" {
		payload.URL = payload.Data.URL
	}
	// 只有 url 时下载后转 data URI
	if payload.URL != "" {
		ireq, err := http.NewRequest(http.MethodGet, payload.URL, nil)
		if err != nil {
			return ""
		}
		ireq.Header.Set("User-Agent", "seltool-go")
		iresp, err := client.Do(ireq)
		if err != nil {
			return ""
		}
		defer iresp.Body.Close()
		if iresp.StatusCode != http.StatusOK {
			return ""
		}
		img, err := io.ReadAll(io.LimitReader(iresp.Body, 2<<20))
		if err != nil || len(img) < 16 {
			return ""
		}
		mime := iresp.Header.Get("Content-Type")
		if i := strings.Index(mime, ";"); i > 0 {
			mime = mime[:i]
		}
		if !strings.HasPrefix(mime, "image/") {
			mime = "image/x-icon"
		}
		return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img)
	}
	return ""
}

// SyncIconsFromMoviePilot 拉取所有站点图标并写入图标表，返回成功数量。
//
// 只补充本地没有的域名，已有图标不覆盖（避免手工修正的图标被劣质数据顶掉）。
func SyncIconsFromMoviePilot(cfg MoviePilotSyncConfig, table *PTIconTable) (int, error) {
	entries, err := fetchMoviePilotSiteList(cfg)
	if err != nil {
		return 0, err
	}
	if table == nil {
		return 0, fmt.Errorf("图标表未初始化")
	}

	added := 0
	for _, e := range entries {
		domain := normalizeIconKey(e.Domain)
		if domain == "" || e.ID <= 0 {
			continue
		}
		if table.Lookup(domain) != "" {
			continue // 已有图标，不覆盖
		}
		uri := fetchMoviePilotIcon(cfg, e.ID)
		if uri == "" || !looksLikeImage(uri) {
			continue
		}
		table.Set(domain, uri)
		added++
	}
	if added > 0 {
		if err := table.Save(); err != nil {
			return added, fmt.Errorf("图标已更新但写盘失败: %w", err)
		}
	}
	return added, nil
}
