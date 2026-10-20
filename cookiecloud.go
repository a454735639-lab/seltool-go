package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// HTTPStatusError 保留状态码，便于区分「连不上」和「连上了但没数据」。
// Body 不再保存远端响应正文：正文可能含 Cookie 或密码，禁止进入日志/API。
type HTTPStatusError struct {
	Code int
	Body string // 保留字段兼容调用方，恒为空
}

func (e *HTTPStatusError) Error() string {
	if reason := statusReason(e.Code); reason != "" {
		return fmt.Sprintf("HTTP %d (%s)", e.Code, reason)
	}
	return fmt.Sprintf("HTTP %d", e.Code)
}

func statusReason(code int) string {
	switch code {
	case 401, 403:
		return "认证被拒绝"
	case 404:
		return "路径或 KEY 不存在"
	case 500:
		return "服务端错误，常见原因是密码错误"
	case 502, 503, 504:
		return "服务不可用"
	}
	return ""
}

// Cookie 是从 CookieCloud 解析出来的、可直接注入浏览器的 Cookie。
// HostOnly=true 表示只对精确主机生效（不带前导点、不匹配子域）；缺省 false 保持旧行为。
type Cookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain"`
	Path     string `json:"path"`
	Secure   bool   `json:"secure"`
	HTTPOnly bool   `json:"httpOnly"`
	HostOnly bool   `json:"hostOnly"`
}

type CookieCloud struct {
	mu     sync.RWMutex
	cfg    CookieCloudConfig
	client *http.Client
	alerts *AlertState
}

// Config 返回当前生效的配置（Web 面板可热更新）。
func (c *CookieCloud) Config() CookieCloudConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg
}

// Update 应用新的配置，无需重启。
func (c *CookieCloud) Update(cfg CookieCloudConfig) {
	c.mu.Lock()
	c.cfg = cfg
	c.mu.Unlock()
}

func NewCookieCloud(cfg CookieCloudConfig, alerts *AlertState) *CookieCloud {
	return &CookieCloud{
		cfg:    cfg,
		alerts: alerts,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

const cookieCloudAPIPath = "cookiecloud/get/"

// endpoint 兼容两种填写方式：
//
//	https://host                 → https://host/cookiecloud/get/{key}
//	https://host/cookiecloud     → https://host/cookiecloud/get/{key}
//
// 用户从扩展里复制过来的地址通常已经带 /cookiecloud，不能再拼一次。
func (c *CookieCloud) endpoint() (string, error) {
	cfg := c.Config()
	raw := strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	if raw == "" {
		return "", fmt.Errorf("CookieCloud 地址不能为空")
	}
	key := url.PathEscape(strings.TrimSpace(cfg.Key))
	if strings.HasSuffix(strings.ToLower(raw), "/cookiecloud") {
		return raw + "/get/" + key, nil
	}
	return raw + "/" + cookieCloudAPIPath + key, nil
}

// FetchAllCookies 一次性取回全量 cookie_data，供循环前统一调用，避免重复请求。
// 未配置 CookieCloud 时返回 nil。远端失败时按 allow-cache-fallback 回退缓存。
func (c *CookieCloud) FetchAllCookies() (map[string][]Cookie, error) {
	cfg := c.Config()
	if !cfg.Enabled() {
		logf("CookieCloud 已禁用，跳过 Cookie 获取")
		return nil, nil
	}

	remote, _, remoteErr := c.fetchRemote()
	if remoteErr == nil {
		var data map[string][]Cookie
		data, remoteErr = parseCookieData(remote)
		if remoteErr == nil {
			c.writeCache(remote)
			return data, nil
		}
	}

	// 远端不可用（网络/状态码/响应结构），按配置决定是否回退缓存。
	c.alerts.AddStartupWarning("CookieCloud 调不通: " + sanitizeOneLine(remoteErr.Error()))
	logf("CookieCloud 调不通: %v", remoteErr)

	cacheFile := c.cachePath()
	cached, cacheErr := c.readCache(cacheFile)
	if cacheErr != nil {
		logf("读取 CookieCloud 缓存失败 %s: %v", cacheFile, cacheErr)
	}
	if cached != nil {
		cachedData, parseErr := parseCookieData(cached)
		if parseErr != nil {
			logf("CookieCloud 缓存内容不合法 %s: %v", cacheFile, parseErr)
		} else if cfg.AllowCacheFallback {
			logf("远端获取失败，回退到旧的 CookieCloud 缓存 %s", cacheFile)
			return cachedData, nil
		} else {
			return nil, fmt.Errorf("CookieCloud 远端获取失败且 allow-cache-fallback 为 false，中止任务: %v", remoteErr)
		}
	}
	if cfg.AllowCacheFallback {
		logf("CookieCloud 远端获取失败且无可用本地缓存，将使用空 Cookie 继续")
		return nil, nil
	}
	return nil, fmt.Errorf("CookieCloud 远端获取失败、无可用本地缓存且 allow-cache-fallback 为 false，中止任务: %v", remoteErr)
}

// Probe 只请求远端并统计，供 Web 面板「测试连接」使用。
// 不写缓存、不读缓存、不记启动告警，确保测试的是「当前输入的配置」。
// reachable=false 表示请求没到达服务端（网络/地址/超时）；状态码类错误 reachable=true。
func (c *CookieCloud) Probe() (domains, cookies int, reachable bool, err error) {
	data, reachable, err := c.probeRemote()
	if err != nil {
		return 0, 0, reachable, err
	}
	for _, list := range data {
		domains++
		cookies += len(list)
	}
	return domains, cookies, true, nil
}

// ProbeWith 用请求里传入的配置测试连接，不修改实例配置、不落盘、不产生告警。
// 供 Web 面板「先测试再保存」使用；保存前的测试结果不应被旧缓存污染。
func (c *CookieCloud) ProbeWith(cfg CookieCloudConfig) (domains, cookies int, reachable bool, err error) {
	probe := &CookieCloud{cfg: cfg, client: c.client}
	return probe.Probe()
}

// probeRemote 返回是否真的收到了 HTTP 响应，用于区分「连不上」和「连上了但内容不可用」。
func (c *CookieCloud) probeRemote() (map[string][]Cookie, bool, error) {
	if !c.Config().Enabled() {
		return nil, false, fmt.Errorf("CookieCloud 地址不能为空")
	}
	payload, gotResponse, err := c.fetchRemote()
	if err != nil {
		return nil, gotResponse, err
	}
	data, err := parseCookieData(payload)
	return data, gotResponse, err
}

// parseCookieData 严格校验远端/缓存结构；缺 cookie_data、类型不对都算错误。
func parseCookieData(payload map[string]any) (map[string][]Cookie, error) {
	raw, ok := payload["cookie_data"]
	if !ok || raw == nil {
		return nil, fmt.Errorf("CookieCloud 返回结果中缺少 cookie_data")
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("CookieCloud 返回的 cookie_data 不是对象")
	}
	out := make(map[string][]Cookie, len(obj))
	for domain, v := range obj {
		list, ok := v.([]any)
		if !ok {
			continue
		}
		cookies := make([]Cookie, 0, len(list))
		for _, item := range list {
			node, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if ck, ok := toCookie(node, domain); ok {
				cookies = append(cookies, ck)
			}
		}
		out[domain] = cookies
	}
	return out, nil
}

// fetchRemote 返回 (payload, 是否收到 HTTP 响应, error)。
// 只要收到了响应，第二个返回值就是 true，即使状态码或响应体不可用。
func (c *CookieCloud) fetchRemote() (map[string]any, bool, error) {
	endpoint, err := c.endpoint()
	if err != nil {
		return nil, false, err
	}
	cfg := c.Config()
	body, _ := json.Marshal(map[string]string{"password": cfg.Password})
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		// 不返回含 key 的完整 URL。
		return nil, false, fmt.Errorf("地址不合法，无法构造请求")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, false, netError(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, true, fmt.Errorf("读取响应失败: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 响应正文可能含 Cookie/密码，不保留、不打印。
		return nil, true, &HTTPStatusError{Code: resp.StatusCode}
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, true, fmt.Errorf("响应体为空")
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, true, fmt.Errorf("响应不是合法 JSON（%d 字节）", len(raw))
	}
	if payload == nil {
		return nil, true, fmt.Errorf("响应不是 JSON 对象")
	}
	logf("CookieCloud 请求成功")
	return payload, true, nil
}

// netError 去掉 url.Error 里的完整 URL（含 key），只保留可诊断的部分。
func netError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return fmt.Errorf("请求超时（%s）", ue.Op)
		}
		return fmt.Errorf("网络错误（%s）: %v", ue.Op, ue.Err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("请求超时")
	}
	return fmt.Errorf("网络错误: %v", err)
}

func (c *CookieCloud) cachePath() string {
	p := c.Config().CacheFile
	if p == "" {
		p = "cookiecloud-cache.json"
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

func (c *CookieCloud) readCache(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (c *CookieCloud) writeCache(payload map[string]any) {
	path := c.cachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		logf("写入 CookieCloud 缓存失败 %s: %v", path, err)
		return
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		logf("序列化 CookieCloud 缓存失败: %v", err)
		return
	}
	if err := writeFileAtomic(path, data, 0o600); err != nil {
		logf("写入 CookieCloud 缓存失败 %s: %v", path, err)
		return
	}
	logf("CookieCloud 缓存已更新 %s", path)
}

// FilterForTarget 按目标域名过滤 Cookie，规则与 Java 版一致：
// 配置域名优先，否则取 URL 的 host；允许父域后缀匹配。
func (c *CookieCloud) FilterForTarget(data map[string][]Cookie, target Target) []Cookie {
	if data == nil {
		return nil
	}
	domain := resolveDomain(target)
	allowed := buildAllowedDomains(domain)
	logf("开始为目标解析 Cookie，配置域名 [%s]，允许匹配域名 %v", target.CookieDomain, allowed)

	var matches []Cookie
	for cookieDomain, cookies := range data {
		normalized := normalizeDomain(cookieDomain)
		if !domainMatches(normalized, allowed) {
			continue
		}
		for _, ck := range cookies {
			if strings.TrimSpace(ck.Domain) == "" {
				ck.Domain = normalized
			}
			matches = append(matches, ck)
		}
	}
	logf("域名 [%s] 共解析到 %d 个 Cookie", domain, len(matches))
	return matches
}

func resolveDomain(target Target) string {
	if strings.TrimSpace(target.CookieDomain) != "" {
		return normalizeDomain(target.CookieDomain)
	}
	return normalizeDomain(hostOf(target.URL))
}

func buildAllowedDomains(domain string) []string {
	normalized := normalizeDomain(domain)
	if normalized == "" {
		return nil
	}
	allowed := []string{normalized}
	segments := strings.Split(normalized, ".")
	for i := 1; i < len(segments)-1; i++ {
		allowed = append(allowed, strings.Join(segments[i:], "."))
	}
	return allowed
}

func domainMatches(cookieDomain string, allowed []string) bool {
	if cookieDomain == "" {
		return false
	}
	for _, a := range allowed {
		if cookieDomain == a || strings.HasSuffix(cookieDomain, "."+a) {
			return true
		}
	}
	return false
}

func normalizeDomain(value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	v = strings.TrimPrefix(v, ".")
	return v
}

func toCookie(node map[string]any, fallbackDomain string) (Cookie, bool) {
	domain := firstText(node, "domain", "host", "site")
	if domain == "" {
		domain = fallbackDomain
	}
	name := firstText(node, "name")
	value, hasValue := node["value"]
	valueStr := ""
	if hasValue && value != nil {
		valueStr = fmt.Sprint(value)
	}
	if domain == "" || name == "" || !hasValue {
		return Cookie{}, false
	}
	path := firstText(node, "path")
	if path == "" {
		path = "/"
	}
	// hostOnly 只在显式 true 时生效：缺省 false 保持旧行为，不改变已有 mock/原版语义。
	hostOnly := false
	if v, ok := node["hostOnly"]; ok {
		if b, ok := v.(bool); ok {
			hostOnly = b
		}
	}
	return Cookie{
		Name:     name,
		Value:    valueStr,
		Domain:   domain,
		Path:     path,
		Secure:   firstBool(node, "secure", "isSecure"),
		HTTPOnly: firstBool(node, "httpOnly", "http_only"),
		HostOnly: hostOnly,
	}, true
}

func firstText(node map[string]any, names ...string) string {
	for _, n := range names {
		if v, ok := node[n]; ok && v != nil {
			if s, ok := v.(string); ok {
				return s
			}
			return fmt.Sprint(v)
		}
	}
	return ""
}

func firstBool(node map[string]any, names ...string) bool {
	for _, n := range names {
		if v, ok := node[n]; ok && v != nil {
			if b, ok := v.(bool); ok {
				return b
			}
		}
	}
	return false
}

func preview(raw []byte, limit int) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > limit {
		return s[:limit] + "..."
	}
	return s
}

func sanitizeOneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
