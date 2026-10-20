package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Webhook 统一通知入口：优先走 WeCom（机器人/应用），
// 未配置时回退到旧的 startup-notification.webhook-url 纯文本。
type Webhook struct {
	mu     sync.RWMutex
	cfg    StartupNotificationConfig
	wecom  *WeCom
	client *http.Client
}

// WithWeCom 启用企业微信通道；未配置时保持旧行为。
func (w *Webhook) WithWeCom(wc WeComConfig) *Webhook {
	w.SetWeCom(wc)
	return w
}

// SetWeCom 热更新企业微信通道（Web 面板保存后立即生效）。
func (w *Webhook) SetWeCom(wc WeComConfig) {
	var next *WeCom
	if NewWeCom(wc).Enabled() {
		next = NewWeCom(wc)
	}
	w.mu.Lock()
	w.wecom = next
	w.mu.Unlock()
}

func (w *Webhook) currentWeCom() *WeCom {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.wecom
}

func NewWebhook(cfg StartupNotificationConfig) *Webhook {
	return &Webhook{cfg: cfg, client: &http.Client{Timeout: 15 * time.Second}}
}

func (w *Webhook) Send(status, content string) {
	if wc := w.currentWeCom(); wc != nil {
		wc.Send(status, content)
		return
	}
	if err := w.send(status, content); err != nil {
		logf("Webhook 消息发送失败: %v", err)
	}
}

func (w *Webhook) send(status, content string) error {
	if !w.cfg.Enabled() {
		return nil
	}
	message := "【selenium-tool】" + status +
		"\n时间: " + time.Now().Format("2006-01-02 15:04:05") +
		"\n内容: " + content

	payload, err := json.Marshal(map[string]any{
		"msgtype": "text",
		"text":    map[string]string{"content": message},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimSpace(w.cfg.WebhookURL), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, preview(body, 200))
	}
	return nil
}
