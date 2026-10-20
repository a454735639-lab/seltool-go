package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// TargetStore 管理可由 Web 页面修改、并在任务执行时实时读取的目标列表。
//
// 数据来源优先级：targets.json > targets.yml（默认目标）> 注入的默认目标。
// JSON 存在时（哪怕是空数组）一律以 JSON 为准，不再回填 YAML，避免用户清空后又被默认值填满。
type TargetStore struct {
	mu      sync.RWMutex
	file    string
	targets []Target

	yamlFile       string
	defaultTargets []Target
	templates      *TemplateStore
}

// TargetStoreOption 让调用方按需注入模板校验与默认目标，不改变既有 NewTargetStore(file) 调用。
type TargetStoreOption func(*TargetStore)

// WithTemplates 注入模板表，Save 时据此校验模板目标。
func WithTemplates(t *TemplateStore) TargetStoreOption {
	return func(s *TargetStore) { s.templates = t }
}

// WithYAMLFallback 指定 JSON 缺失时使用的 YAML 默认目标文件。
func WithYAMLFallback(file string) TargetStoreOption {
	return func(s *TargetStore) { s.yamlFile = absPath(file) }
}

// WithDefaultTargets 直接注入默认目标（例如 config 里的 automation.targets）。
func WithDefaultTargets(targets []Target) TargetStoreOption {
	return func(s *TargetStore) { s.defaultTargets = targets }
}

func NewTargetStore(file string, opts ...TargetStoreOption) *TargetStore {
	s := &TargetStore{file: absPath(file)}
	// 默认与 targets.json 同目录的 targets.yml。
	ext := filepath.Ext(s.file)
	if ext != "" {
		s.yamlFile = strings.TrimSuffix(s.file, ext) + ".yml"
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// Configure 在 Load 之前注入默认目标与模板表（主线程按此顺序调用）。
// 传 nil 表示该项不启用；defaults 为空时仍会读取同目录 targets.yml。
// 只影响“无 JSON 时的默认值”和“Save 校验”，不会改写已存在的 targets.json。
func (s *TargetStore) Configure(defaults []Target, templates *TemplateStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defaultTargets = defaults
	s.templates = templates
}

// SetTemplates 等价于只注入模板表。
func (s *TargetStore) SetTemplates(templates *TemplateStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.templates = templates
}

func (s *TargetStore) templateStore() *TemplateStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.templates
}

func (s *TargetStore) Load() error {
	data, err := os.ReadFile(s.file)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("无法读取目标配置文件: %w", err)
		}
		// JSON 不存在：内存里已有数据（用户刚保存、文件被移走）时保持不动，绝不用默认值覆盖。
		// 仅在内存为空时用 YAML 默认目标，再退回注入的默认值。
		if len(s.Get()) > 0 {
			return nil
		}
		if fallback, ok, ferr := s.loadDefaults(); ferr != nil {
			return ferr
		} else if ok {
			s.replace(fallback)
		}
		return nil
	}
	var loaded []Target
	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("无法读取目标配置文件: %w", err)
	}
	for i := range loaded {
		normalizeTarget(&loaded[i])
	}
	s.replace(loaded)
	return nil
}

// loadDefaults 读取 YAML 默认目标或注入的默认目标。ok=false 表示两者都没有。
// 只在 Load 发现 targets.json 缺失时调用；Load 从不校验历史数据，避免旧配置被拒。
func (s *TargetStore) loadDefaults() ([]Target, bool, error) {
	s.mu.RLock()
	yamlFile := s.yamlFile
	defaults := s.defaultTargets
	s.mu.RUnlock()
	if yamlFile != "" {
		if data, err := os.ReadFile(yamlFile); err == nil {
			var tf templateFile
			if err := yaml.Unmarshal(data, &tf); err != nil {
				return nil, false, fmt.Errorf("无法读取默认目标文件 %s: %w", yamlFile, err)
			}
			targets := tf.Targets
			for i := range targets {
				normalizeTarget(&targets[i])
			}
			if len(targets) > 0 {
				logf("未找到 %s，使用默认目标文件 %s", s.file, yamlFile)
				return targets, true, nil
			}
		} else if !os.IsNotExist(err) {
			return nil, false, fmt.Errorf("无法读取默认目标文件 %s: %w", yamlFile, err)
		}
	}
	if len(defaults) > 0 {
		targets := make([]Target, len(defaults))
		copy(targets, defaults)
		for i := range targets {
			normalizeTarget(&targets[i])
		}
		logf("未找到 %s，使用配置中的默认目标", s.file)
		return targets, true, nil
	}
	return nil, false, nil
}

func (s *TargetStore) replace(targets []Target) {
	s.mu.Lock()
	s.targets = targets
	s.mu.Unlock()
}

func cloneTargets(in []Target) []Target {
	out := make([]Target, len(in))
	copy(out, in)
	for i := range out {
		if in[i].LocalStorage != nil {
			out[i].LocalStorage = append([]LocalStorage{}, in[i].LocalStorage...)
		}
	}
	return out
}

func (s *TargetStore) Get() []Target {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneTargets(s.targets)
}

func (s *TargetStore) Find(rawURL string) (Target, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.targets {
		if t.URL == rawURL {
			return t, true
		}
	}
	return Target{}, false
}

func (s *TargetStore) Save(targets []Target) ([]Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	replacement := cloneTargets(targets)
	for i := range replacement {
		normalizeTarget(&replacement[i])
	}

	// 拒绝重复目标：同一注册域名只允许一条。
	// 与自身已有列表比较时排除「同一条记录」（URL 完全相同视为同一条，允许保留）。
	if dup := findDuplicateTarget(replacement, nil); dup != "" {
		return nil, fmt.Errorf("%s", dup)
	}
	for _, t := range replacement {
		if err := validateTargets([]Target{t}, s.templates); err != nil {
			// Preserve an unchanged legacy record; never silently repair it or allow
			// a new/edited invalid template through. This lets other targets be saved.
			unchanged := false
			for _, old := range s.targets {
				if reflect.DeepEqual(old, t) {
					unchanged = true
					break
				}
			}
			if !unchanged {
				return nil, err
			}
			logf("保留未修改的旧目标配置；模板字段需按当前规则校正后编辑")
		}
	}
	if err := writeJSONAtomic(s.file, replacement, 0o600); err != nil {
		return nil, fmt.Errorf("无法保存目标配置文件: %w", err)
	}
	s.targets = replacement
	return cloneTargets(replacement), nil
}

// findDuplicateTarget 检查传入列表内部、以及与现有目标之间是否存在注册域名重复。
// 返回重复项的描述，无重复返回空串。
//
// 用注册域名（而非完整 URL）判断，与导入侧规则一致：
// www.hddolby.com 与 hddolby.com 视为同一个站，不允许同时存在。
func findDuplicateTarget(incoming, existing []Target) string {
	seen := map[string]string{} // 注册域名 -> 首次出现的目标名
	for _, t := range existing {
		if reg := registeredDomain(t.URL); reg != "" {
			if _, ok := seen[reg]; !ok {
				seen[reg] = t.DisplayName() + "（已存在）"
			}
		}
	}
	for _, t := range incoming {
		reg := registeredDomain(t.URL)
		if reg == "" {
			continue
		}
		if prev, ok := seen[reg]; ok {
			name := t.DisplayName()
			if name == "" {
				name = t.URL
			}
			return fmt.Sprintf("%s 与 %s 是同一站点（%s），不能重复添加", name, prev, reg)
		}
		seen[reg] = t.DisplayName()
	}
	return ""
}

// 目标数量与 URL 长度的上限，避免误操作或异常输入把任务拖垮。
const (
	maxTargets   = 500
	maxURLLength = 2048
	maxNameLen   = 200
)

func validateTargets(targets []Target, templates *TemplateStore) error {
	if len(targets) > maxTargets {
		return fmt.Errorf("目标数量超出上限（%d 个），当前 %d 个", maxTargets, len(targets))
	}
	for i, t := range targets {
		raw := strings.TrimSpace(t.URL)
		if raw == "" {
			return fmt.Errorf("第 %d 个目标缺少 URL", i+1)
		}
		if len(raw) > maxURLLength {
			return fmt.Errorf("第 %d 个目标 URL 过长（%d 字符，上限 %d）", i+1, len(raw), maxURLLength)
		}
		if len([]rune(t.Name)) > maxNameLen {
			return fmt.Errorf("第 %d 个目标名称过长（上限 %d 字符）", i+1, maxNameLen)
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("第 %d 个目标 URL 无效: %s", i+1, raw)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("第 %d 个目标仅支持 http/https: %s", i+1, raw)
		}
		// 普通目标也不允许空 key，否则运行时会往 localStorage 写空键。
		for j, item := range t.LocalStorage {
			if strings.TrimSpace(item.Key) == "" {
				return fmt.Errorf("第 %d 个目标的 localStorage[%d] key 不能为空", i+1, j+1)
			}
		}
		if strings.TrimSpace(t.TemplateID) != "" {
			if err := validateTemplateTarget(t, templates, i); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateTemplateTarget 与 Java 版 TargetConfigurationService.validateTemplateTarget 对齐：
// 模板必须存在，name/url/warmupPath 不可改，localStorage 键集合与顺序必须与模板完全一致且值必填。
func validateTemplateTarget(t Target, templates *TemplateStore, index int) error {
	if templates == nil {
		return fmt.Errorf("第 %d 个目标引用了模板 %s，但未加载模板配置", index+1, t.TemplateID)
	}
	tpl, ok := templates.Find(t.TemplateID)
	if !ok {
		return fmt.Errorf("第 %d 个目标引用了不存在的模板: %s", index+1, t.TemplateID)
	}
	if t.Name != tpl.Name || t.URL != tpl.URL || t.WarmupPath != tpl.WarmupPath {
		return fmt.Errorf("第 %d 个模板目标的 URL、名称和预热路径不可修改", index+1)
	}
	storage := t.LocalStorage
	if storage == nil {
		storage = []LocalStorage{}
	}
	if len(storage) != len(tpl.LocalStorageKeys) {
		return fmt.Errorf("第 %d 个模板目标的 localStorage 键不可增删", index+1)
	}
	for i, expected := range tpl.LocalStorageKeys {
		item := storage[i]
		if item.Key != expected {
			return fmt.Errorf("第 %d 个模板目标的 localStorage 键不可修改", index+1)
		}
		if strings.TrimSpace(item.Value) == "" {
			return fmt.Errorf("第 %d 个模板目标的 localStorage[%s] value 为必填项", index+1, expected)
		}
	}
	return nil
}

// Template 是新增目标时可选的站点模板。
type Template struct {
	ID               string   `yaml:"id" json:"id"`
	Name             string   `yaml:"name" json:"name"`
	URL              string   `yaml:"url" json:"url"`
	WarmupPath       string   `yaml:"warmupPath" json:"warmupPath"`
	LocalStorageKeys []string `yaml:"localStorageKeys" json:"localStorageKeys"`
}

// templateFile 同时承载模板文件与默认目标文件（targets.yml）的顶层结构。
type templateFile struct {
	Templates []Template `yaml:"templates"`
	Targets   []Target   `yaml:"targets"`
}
