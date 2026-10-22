package main

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
)

// NotifySettings 是 Web 面板可编辑的通知配置；存独立文件，保存即热更新。
// 字段与 WeComConfig 一一对应，敏感项（corpsecret/webhook-url）不回显原文。
type NotifySettings struct {
	Mode       string `json:"mode"`
	WebhookURL string `json:"webhookUrl"`
	MsgType    string `json:"msgType"`
	CorpID     string `json:"corpId"`
	CorpSecret string `json:"corpSecret"`
	AgentID    int64  `json:"agentId"`
	ToUser     string `json:"toUser"`
	Proxy      string `json:"proxy"`
	// OnlyOnFailure 勾选后，全部成功的任务不再推送。
	OnlyOnFailure bool `json:"onlyOnFailure"`
	// 企业微信「接收消息」回调（交互式命令）。
	CallbackEnabled bool   `json:"callbackEnabled"`
	CallbackToken   string `json:"callbackToken"`
	CallbackAESKey  string `json:"callbackAesKey"`
	CallbackOwner   string `json:"callbackOwner"`
	// 快捷指令 H5 页访问密钥（非空即启用 /pt-panel 数据接口）。
	PanelKey string `json:"panelKey"`
}

func (s NotifySettings) toConfig() WeComConfig {
	return WeComConfig{
		Mode:            s.Mode,
		WebhookURL:      strings.TrimSpace(s.WebhookURL),
		MsgType:         s.MsgType,
		CorpID:          strings.TrimSpace(s.CorpID),
		CorpSecret:      strings.TrimSpace(s.CorpSecret),
		AgentID:         s.AgentID,
		ToUser:          strings.TrimSpace(s.ToUser),
		Proxy:           strings.TrimSpace(s.Proxy),
		CallbackEnabled: s.CallbackEnabled,
		CallbackToken:   strings.TrimSpace(s.CallbackToken),
		CallbackAESKey:  strings.TrimSpace(s.CallbackAESKey),
		CallbackOwner:   strings.TrimSpace(s.CallbackOwner),
	}
}

type NotifyStore struct {
	mu   sync.RWMutex
	file string
	cur  NotifySettings
}

func NewNotifyStore(file string) *NotifyStore {
	return &NotifyStore{file: absPath(file)}
}

// Load 读取面板保存的通知配置；文件不存在返回 ok=false，回退到 yaml/env。
func (s *NotifyStore) Load() (NotifySettings, bool, error) {
	data, err := os.ReadFile(s.file)
	if err != nil {
		if os.IsNotExist(err) {
			return NotifySettings{}, false, nil
		}
		return NotifySettings{}, false, err
	}
	var saved NotifySettings
	if err := json.Unmarshal(data, &saved); err != nil {
		return NotifySettings{}, false, err
	}
	s.mu.Lock()
	s.cur = saved
	s.mu.Unlock()
	return saved, true, nil
}

func (s *NotifyStore) Current() NotifySettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// Save 保存配置；corpSecret 为空表示沿用已有值（面板不回显密码）。
func (s *NotifyStore) Save(in NotifySettings) (NotifySettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	in.Mode = strings.ToLower(strings.TrimSpace(in.Mode))
	if in.Mode != "app" {
		in.Mode = "bot"
	}
	in.WebhookURL = strings.TrimSpace(in.WebhookURL)
	in.CorpID = strings.TrimSpace(in.CorpID)
	in.ToUser = strings.TrimSpace(in.ToUser)
	in.Proxy = strings.TrimSpace(in.Proxy)
	in.CorpSecret = strings.TrimSpace(in.CorpSecret)
	if in.CorpSecret == "" {
		in.CorpSecret = s.cur.CorpSecret
	}
	in.CallbackToken = strings.TrimSpace(in.CallbackToken)
	in.CallbackAESKey = strings.TrimSpace(in.CallbackAESKey)
	if in.CallbackAESKey == "" {
		in.CallbackAESKey = s.cur.CallbackAESKey
	}
	in.CallbackOwner = strings.TrimSpace(in.CallbackOwner)
	in.PanelKey = strings.TrimSpace(in.PanelKey)
	if in.MsgType == "" {
		in.MsgType = "markdown"
	}

	if err := writeJSONAtomic(s.file, in, 0o600); err != nil {
		return NotifySettings{}, err
	}
	s.cur = in
	return in, nil
}

// mask 返回给前端的视图：敏感字段只给是否已配置。
func (s NotifySettings) mask() map[string]any {
	return map[string]any{
		"mode":              s.Mode,
		"webhookUrl":        s.WebhookURL,
		"msgType":           s.MsgType,
		"corpId":            s.CorpID,
		"agentId":           s.AgentID,
		"toUser":            s.ToUser,
		"proxy":             s.Proxy,
		"onlyOnFailure":     s.OnlyOnFailure,
		"hasCorpSecret":     strings.TrimSpace(s.CorpSecret) != "",
		"callbackEnabled":   s.CallbackEnabled,
		"callbackToken":     s.CallbackToken,
		"callbackOwner":     s.CallbackOwner,
		"hasCallbackAESKey": strings.TrimSpace(s.CallbackAESKey) != "",
		"panelKey":          s.PanelKey,
		"enabled":           NewWeCom(s.toConfig()).Enabled(),
	}
}
