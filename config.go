package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration 支持 "2m"、"30s"、"1h" 这类写法，同时兼容纯数字（按秒解释）。
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err != nil {
		var seconds int64
		if err2 := value.Decode(&seconds); err2 != nil {
			return err
		}
		*d = Duration(time.Duration(seconds) * time.Second)
		return nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("无法解析时长 %q: %w", raw, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

type Config struct {
	Automation AutomationConfig `yaml:"automation"`
}

type AutomationConfig struct {
	TargetsFile         string                    `yaml:"targets-file"`
	TargetHistoryFile   string                    `yaml:"target-history-file"`
	UserAccountFile     string                    `yaml:"user-account-file"`
	TemplatesFile       string                    `yaml:"target-templates-file"`
	RunOnStartup        bool                      `yaml:"run-on-startup"`
	Listen              string                    `yaml:"listen"`
	Schedule            ScheduleConfig            `yaml:"schedule"`
	Browser             BrowserConfig             `yaml:"browser"`
	CookieCloud         CookieCloudConfig         `yaml:"cookie-cloud"`
	StartupNotification StartupNotificationConfig `yaml:"startup-notification"`
	Notify              NotifyConfig              `yaml:"notify"`
	Targets             []Target                  `yaml:"targets"`
}

type ScheduleConfig struct {
	Cron string `yaml:"cron"`
	Zone string `yaml:"zone"`
}

type BrowserConfig struct {
	Headless           bool     `yaml:"headless"`
	NoSandbox          bool     `yaml:"no-sandbox"`
	DisableDevShmUsage bool     `yaml:"disable-dev-shm-usage"`
	PageStaySeconds    int      `yaml:"page-stay-seconds"`
	PageLoadTimeout    Duration `yaml:"page-load-timeout"`
	BinaryPath         string   `yaml:"binary-path"`
	UserDataDir        string   `yaml:"user-data-dir"`
	DriverPath         string   `yaml:"driver-path"`
	Proxy              string   `yaml:"proxy"`
	// ProxyMode 是全局默认出口：direct（直连，默认）或 proxy（走 Proxy 配置的代理）。
	// 单个目标可用 proxyMode 覆盖。
	ProxyMode             string   `yaml:"proxy-mode"`
	Arguments             []string `yaml:"arguments"`
	DisableImages         bool     `yaml:"disable-images"`
	BlockResourceSuffixes []string `yaml:"block-resource-suffixes"`
}

type CookieCloudConfig struct {
	URL                string `yaml:"url"`
	Key                string `yaml:"key"`
	Password           string `yaml:"password"`
	CacheFile          string `yaml:"cache-file"`
	AllowCacheFallback bool   `yaml:"allow-cache-fallback"`
}

func (c CookieCloudConfig) Enabled() bool { return strings.TrimSpace(c.URL) != "" }

// NotifyConfig 承载企业微信通知配置（对齐 MoviePilot 的机器人/应用双模式）。
type NotifyConfig struct {
	// OnlyOnFailure 为 true 时，全部成功的任务不再发通知。
	OnlyOnFailure bool        `yaml:"only-on-failure"`
	WeCom         WeComConfig `yaml:"wecom"`
}

type WeComConfig struct {
	// bot = 群机器人 webhook；app = 企业微信应用消息
	Mode       string `yaml:"mode"`
	WebhookURL string `yaml:"webhook-url"`
	// 群机器人消息类型：markdown（默认）或 text
	MsgType string `yaml:"msgtype"`
	// 应用模式参数
	CorpID     string `yaml:"corpid"`
	CorpSecret string `yaml:"corpsecret"`
	AgentID    int64  `yaml:"agentid"`
	ToUser     string `yaml:"touser"`
	// 企业微信 API 代理，默认 https://qyapi.weixin.qq.com
	Proxy string `yaml:"proxy"`
	// 接收消息回调（企业微信应用后台「接收消息」生成的）
	CallbackEnabled bool   `yaml:"callback-enabled"`
	CallbackToken   string `yaml:"callback-token"`
	CallbackAESKey  string `yaml:"callback-aeskey"`
	CallbackOwner   string `yaml:"callback-owner"`
}

type StartupNotificationConfig struct {
	WebhookURL string `yaml:"webhook-url"`
	Message    string `yaml:"message"`
}

func (c StartupNotificationConfig) Enabled() bool { return strings.TrimSpace(c.WebhookURL) != "" }

type Target struct {
	TemplateID   string         `yaml:"templateId" json:"templateId"`
	Name         string         `yaml:"name" json:"name"`
	URL          string         `yaml:"url" json:"url"`
	CookieDomain string         `yaml:"cookieDomain" json:"cookieDomain,omitempty"`
	WarmupPath   string         `yaml:"warmupPath" json:"warmupPath"`
	LocalStorage []LocalStorage `yaml:"localStorage" json:"localStorage"`
	// 以下为可选增强：配置后按 DOM 选择器判定登录态，优先于标题关键字匹配。
	OKSelector   string `yaml:"okSelector" json:"okSelector,omitempty"`
	FailSelector string `yaml:"failSelector" json:"failSelector,omitempty"`
	LoginPath    string `yaml:"loginPath" json:"loginPath,omitempty"`
	// NoProxy 为 true 时该目标使用直连（不注入全局代理），用于被代理出口 IP 拦截的站点。
	NoProxy bool `yaml:"noProxy" json:"noProxy,omitempty"`
	// ProxyMode 按目标覆盖网络出口：""（跟随全局默认）/"direct"（直连）/"proxy"（走代理）。
	ProxyMode string `yaml:"proxyMode" json:"proxyMode,omitempty"`
}

type LocalStorage struct {
	Key   string `yaml:"key" json:"key"`
	Value string `yaml:"value" json:"value"`
}

// DisplayName 返回用于日志与通知的目标名称，缺省取 host。
func (t Target) DisplayName() string {
	if strings.TrimSpace(t.Name) != "" {
		return t.Name
	}
	host := hostOf(t.URL)
	if host == "" {
		return t.URL
	}
	return host
}

// EffectiveWarmupPath 缺省与 Java 版一致，使用 /favicon.ico。
func (t Target) EffectiveWarmupPath() string {
	p := strings.TrimSpace(t.WarmupPath)
	if p == "" {
		return "/favicon.ico"
	}
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}

func defaultConfig() *Config {
	return &Config{Automation: AutomationConfig{
		TargetsFile:       "./config/targets.json",
		TargetHistoryFile: "./config/target-run-history.json",
		UserAccountFile:   "./config/user-account.json",
		TemplatesFile:     "./config/target-templates.yml",
		RunOnStartup:      true,
		Listen:            ":8080",
		Schedule: ScheduleConfig{
			Cron: "0 0 8 * * *",
			Zone: "Asia/Shanghai",
		},
		Browser: BrowserConfig{
			Headless:           false,
			NoSandbox:          true,
			DisableDevShmUsage: true,
			PageStaySeconds:    3,
			PageLoadTimeout:    Duration(2 * time.Minute),
			UserDataDir:        "./data/chrome-profile",
			ProxyMode:          "direct",
			Arguments:          []string{"--window-size=1400,900", "--disable-gpu"},
			DisableImages:      true,
		},
		CookieCloud: CookieCloudConfig{
			CacheFile:          "./config/cookiecloud-cache.json",
			AllowCacheFallback: true,
		},
		StartupNotification: StartupNotificationConfig{Message: "selenium-tool启动成功"},
	}}
}

// loadConfig 合并内置默认值 → config/custom.yml → 环境变量。
// yaml 反序列化到已填充的结构体只覆盖文档中出现的键，天然实现逐字段覆盖。
func loadConfig(path string) (*Config, error) {
	cfg := defaultConfig()

	if path == "" {
		path = "./config/custom.yml"
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
		}
	case os.IsNotExist(err):
		// 允许缺省
	default:
		return nil, fmt.Errorf("读取 %s 失败: %w", path, err)
	}

	applyEnvOverrides(cfg)
	normalizeConfig(cfg)
	return cfg, nil
}

// applyEnvOverrides 允许用环境变量注入敏感项，避免密码落到挂载文件里。
func applyEnvOverrides(cfg *Config) {
	setStr := func(dst *string, key string) {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			*dst = v
		}
	}
	setStr(&cfg.Automation.CookieCloud.URL, "SELTOOL_COOKIECLOUD_URL")
	setStr(&cfg.Automation.CookieCloud.Key, "SELTOOL_COOKIECLOUD_KEY")
	setStr(&cfg.Automation.CookieCloud.Password, "SELTOOL_COOKIECLOUD_PASSWORD")
	setStr(&cfg.Automation.StartupNotification.WebhookURL, "SELTOOL_WEBHOOK_URL")
	setStr(&cfg.Automation.Notify.WeCom.WebhookURL, "SELTOOL_WECOM_WEBHOOK_URL")
	setStr(&cfg.Automation.Notify.WeCom.Mode, "SELTOOL_WECOM_MODE")
	setStr(&cfg.Automation.Notify.WeCom.CorpID, "SELTOOL_WECOM_CORPID")
	setStr(&cfg.Automation.Notify.WeCom.CorpSecret, "SELTOOL_WECOM_CORPSECRET")
	setStr(&cfg.Automation.Notify.WeCom.ToUser, "SELTOOL_WECOM_TOUSER")
	setStr(&cfg.Automation.Notify.WeCom.Proxy, "SELTOOL_WECOM_PROXY")
	if v := strings.TrimSpace(os.Getenv("SELTOOL_WECOM_AGENTID")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.Automation.Notify.WeCom.AgentID = n
		}
	}
	setStr(&cfg.Automation.Schedule.Cron, "SELTOOL_CRON")
	setStr(&cfg.Automation.Schedule.Zone, "SELTOOL_TIMEZONE")
	setStr(&cfg.Automation.Listen, "SELTOOL_LISTEN")
	setStr(&cfg.Automation.Browser.BinaryPath, "SELTOOL_CHROME_BIN")
	setStr(&cfg.Automation.Browser.Proxy, "SELTOOL_PROXY")
	setStr(&cfg.Automation.Browser.ProxyMode, "SELTOOL_PROXY_MODE")
	setStr(&cfg.Automation.TemplatesFile, "SELTOOL_TEMPLATES_FILE")
	setStr(&cfg.Automation.TargetsFile, "SELTOOL_TARGETS_FILE")
	setStr(&cfg.Automation.TargetHistoryFile, "SELTOOL_HISTORY_FILE")
	setStr(&cfg.Automation.UserAccountFile, "SELTOOL_ACCOUNT_FILE")
	if v := strings.TrimSpace(os.Getenv("SELTOOL_RUN_ON_STARTUP")); v != "" {
		cfg.Automation.RunOnStartup = v == "1" || strings.EqualFold(v, "true")
	}
}

func normalizeConfig(cfg *Config) {
	a := &cfg.Automation
	if a.Listen == "" {
		a.Listen = ":8080"
	}
	if a.Schedule.Cron == "" {
		a.Schedule.Cron = "0 0 8 * * *"
	}
	if a.Schedule.Zone == "" {
		a.Schedule.Zone = "Asia/Shanghai"
	}
	if a.Browser.UserDataDir == "" {
		a.Browser.UserDataDir = "./data/chrome-profile"
	}
	if a.Browser.PageLoadTimeout <= 0 {
		a.Browser.PageLoadTimeout = Duration(2 * time.Minute)
	}
	if a.Browser.PageStaySeconds < 0 {
		a.Browser.PageStaySeconds = 0
	}
	if a.CookieCloud.CacheFile == "" {
		a.CookieCloud.CacheFile = "./config/cookiecloud-cache.json"
	}
	if a.TemplatesFile == "" {
		a.TemplatesFile = "./config/target-templates.yml"
	}
	for i := range a.Targets {
		normalizeTarget(&a.Targets[i])
	}
}

func normalizeTarget(t *Target) {
	t.URL = strings.TrimSpace(t.URL)
	if t.LocalStorage == nil {
		t.LocalStorage = []LocalStorage{}
	}
}
