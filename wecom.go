package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// 企业微信通知，对齐 MoviePilot 的两种模式：
//
//	bot —— 群机器人 webhook，支持 text / markdown
//	app —— 企业微信应用消息，corpid+corpsecret 换 access_token 后按 agentid 下发
//
// 两种模式都以 {"errcode":0} 判定成功；HTTP 200 不代表发送成功。
type WeCom struct {
	cfg    WeComConfig
	client *http.Client

	mu       sync.Mutex
	token    string
	expireAt time.Time
}

func NewWeCom(cfg WeComConfig) *WeCom {
	timeout := 15 * time.Second
	return &WeCom{cfg: cfg, client: &http.Client{Timeout: timeout}}
}

func (w *WeCom) Mode() string {
	m := strings.ToLower(strings.TrimSpace(w.cfg.Mode))
	if m == "" {
		if strings.TrimSpace(w.cfg.WebhookURL) != "" {
			return "bot"
		}
		if strings.TrimSpace(w.cfg.CorpID) != "" {
			return "app"
		}
	}
	return m
}

func (w *WeCom) Enabled() bool {
	switch w.Mode() {
	case "bot":
		return strings.TrimSpace(w.cfg.WebhookURL) != ""
	case "app":
		return strings.TrimSpace(w.cfg.CorpID) != "" && strings.TrimSpace(w.cfg.CorpSecret) != "" && w.cfg.AgentID > 0
	}
	return false
}

// Send 发送一条通知。title 是状态标题，content 是正文。
func (w *WeCom) Send(title, content string) {
	if !w.Enabled() {
		return
	}
	var err error
	if w.Mode() == "app" {
		err = w.sendApp(title, content)
	} else {
		err = w.sendBot(title, content)
	}
	if err != nil {
		logf("企业微信通知发送失败: %v", err)
	}
}

// ---- 群机器人 ----

func (w *WeCom) sendBot(title, content string) error {
	endpoint := strings.TrimSpace(w.cfg.WebhookURL)
	msgType := strings.ToLower(strings.TrimSpace(w.cfg.MsgType))
	if msgType == "" {
		msgType = "markdown"
	}

	var payload map[string]any
	switch msgType {
	case "text":
		body := "【" + appName + "】" + title + "\n" + toPlainText(content)
		payload = map[string]any{"msgtype": "text", "text": map[string]string{"content": truncateBytes(body, 2048)}}
	default:
		// 企业微信 markdown 上限 4096 字节
		md := "**" + appName + " · " + title + "**\n" + markdownBody(content)
		payload = map[string]any{"msgtype": "markdown", "markdown": map[string]string{"content": truncateBytes(md, 4000)}}
	}
	return w.postJSON(endpoint, payload)
}

// ---- 应用消息 ----

func (w *WeCom) sendApp(title, content string) error {
	token, err := w.accessToken()
	if err != nil {
		return err
	}
	base := strings.TrimRight(strings.TrimSpace(w.cfg.Proxy), "/")
	if base == "" {
		base = "https://qyapi.weixin.qq.com"
	}
	endpoint := base + "/cgi-bin/message/send?access_token=" + url.QueryEscape(token)

	touser := strings.TrimSpace(w.cfg.ToUser)
	if touser == "" {
		touser = "@all"
	}

	// 应用文本消息上限 2048 字节，按行切分，和 MoviePilot 一致
	full := title
	if content != "" {
		full += "\n" + strings.ReplaceAll(toPlainText(content), "\n\n", "\n")
	}
	for _, chunk := range splitByBytes(full, 2000) {
		payload := map[string]any{
			"touser":  touser,
			"msgtype": "text",
			"agentid": w.cfg.AgentID,
			"text":    map[string]string{"content": chunk},
			"safe":    0,
		}
		if err := w.postJSON(endpoint, payload); err != nil {
			return err
		}
	}
	return nil
}

// accessToken 带缓存；提前 5 分钟过期，避免边界失效。
func (w *WeCom) accessToken() (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.token != "" && time.Now().Before(w.expireAt) {
		return w.token, nil
	}

	base := strings.TrimRight(strings.TrimSpace(w.cfg.Proxy), "/")
	if base == "" {
		base = "https://qyapi.weixin.qq.com"
	}
	reqURL := fmt.Sprintf("%s/cgi-bin/gettoken?corpid=%s&corpsecret=%s",
		base, url.QueryEscape(w.cfg.CorpID), url.QueryEscape(w.cfg.CorpSecret))

	resp, err := w.client.Get(reqURL)
	if err != nil {
		return "", fmt.Errorf("获取 access_token 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))

	var out struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("解析 access_token 响应失败: %w", err)
	}
	if out.ErrCode != 0 || out.AccessToken == "" {
		return "", fmt.Errorf("获取 access_token 失败: errcode=%d %s", out.ErrCode, out.ErrMsg)
	}

	w.token = out.AccessToken
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 7200 * time.Second
	}
	if ttl > 5*time.Minute {
		ttl -= 5 * time.Minute
	}
	w.expireAt = time.Now().Add(ttl)
	return w.token, nil
}

// ---- 公共 ----

// postJSON 统一校验 HTTP 状态与企微业务错误码。
func (w *WeCom) postJSON(endpoint string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, preview(raw, 200))
	}
	var out struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &out); err == nil && out.ErrCode != 0 {
		return fmt.Errorf("errcode=%d %s", out.ErrCode, out.ErrMsg)
	}
	return nil
}

// sendTest 发一条测试消息，复用正常发送路径。
func (w *WeCom) sendTest() error {
	title := "测试通知"
	content := "这是一条来自 Web 面板的测试消息\n时间: " + time.Now().Format("2006-01-02 15:04:05")
	if w.Mode() == "app" {
		return w.sendApp(title, content)
	}
	return w.sendBot(title, content)
}

// markdownBody 把普通文本转成企微 markdown：颜色标记转 font 标签，多行内容加引用前缀。
func markdownBody(content string) string {
	content = renderMarkdownMarkers(content)
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines[i] = "> " + line
	}
	return strings.Join(lines, "\n")
}

// truncateBytes 按 UTF-8 字节上限截断，不截断半个字符。
func truncateBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "\n…"
}

// splitByBytes 按行累积切分，单块不超过 limit 字节。
func splitByBytes(s string, limit int) []string {
	if len(s) <= limit {
		return []string{s}
	}
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(s, "\n") {
		line += "\n"
		if cur.Len() > 0 && cur.Len()+len(line) > limit {
			out = append(out, strings.TrimRight(cur.String(), "\n"))
			cur.Reset()
		}
		cur.WriteString(line)
	}
	if cur.Len() > 0 {
		out = append(out, strings.TrimRight(cur.String(), "\n"))
	}
	return out
}

// CreateQuickMenu 在企业微信应用底部创建「PT 保活」快捷菜单（view 型，指向 H5 快捷页）。
// 前提：应用为 app 模式且已配置 corpid/secret/agentid。
func (w *WeCom) CreateQuickMenu(statusURL, retryURL string) error {
	token, err := w.accessToken()
	if err != nil {
		return err
	}
	base := strings.TrimRight(strings.TrimSpace(w.cfg.Proxy), "/")
	if base == "" {
		base = "https://qyapi.weixin.qq.com"
	}
	// agentid 必须作为 URL query 传给企微，放 payload 会报 41011 missing agentid
	endpoint := fmt.Sprintf("%s/cgi-bin/menu/create?access_token=%s&agentid=%d",
		base, url.QueryEscape(token), w.cfg.AgentID)

	// 用 click 类型：点击后企微把事件推到回调 URL，我们执行并直接在会话里回复，
	// 不打开网页。view 类型会跳 H5，多一步操作。
	payload := map[string]any{
		"button": []any{
			map[string]any{
				"type": "click", "name": "查状态", "key": "STATUS",
			},
			map[string]any{
				"type": "click", "name": "重试失败", "key": "RETRY_FAILED",
			},
		},
	}
	return w.postJSON(endpoint, payload)
}
