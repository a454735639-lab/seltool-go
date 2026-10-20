package main

import (
	_ "embed"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// embeddedTemplates 保证配置文件缺失时模板列表仍然可用（容器里挂载 ./config 会盖掉默认文件）。
//
//go:embed assets/target-templates.yml
var embeddedTemplates []byte

// TemplateStore 从 YAML 加载新增目标时可选的站点模板。
type TemplateStore struct {
	mu        sync.RWMutex
	file      string
	templates []Template
}

func NewTemplateStore(file string) *TemplateStore {
	return &TemplateStore{file: file}
}

func (s *TemplateStore) Load() error {
	data, err := os.ReadFile(s.file)
	if err != nil {
		if os.IsNotExist(err) {
			logf("未找到模板文件 %s，使用内置模板", s.file)
			data = embeddedTemplates
		} else {
			return fmt.Errorf("读取模板文件失败: %w", err)
		}
	}
	var tf templateFile
	if err := yaml.Unmarshal(data, &tf); err != nil {
		return fmt.Errorf("解析模板文件失败: %w", err)
	}
	if err := validateTemplates(tf.Templates); err != nil {
		return err
	}
	s.mu.Lock()
	s.templates = tf.Templates
	s.mu.Unlock()
	return nil
}

func (s *TemplateStore) All() []Template {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Template, len(s.templates))
	copy(out, s.templates)
	return out
}

// Find 按 id 查找模板，供保存目标时校验。
func (s *TemplateStore) Find(id string) (Template, bool) {
	if s == nil {
		return Template{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.templates {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}

func validateTemplates(templates []Template) error {
	seen := map[string]bool{}
	for _, t := range templates {
		if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.Name) == "" ||
			strings.TrimSpace(t.URL) == "" || strings.TrimSpace(t.WarmupPath) == "" {
			return fmt.Errorf("目标模板必须配置 id、name、url 和 warmupPath")
		}
		if seen[t.ID] {
			return fmt.Errorf("目标模板 id 重复: %s", t.ID)
		}
		seen[t.ID] = true
		u, err := url.Parse(t.URL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("目标模板 URL 无效: %s", t.URL)
		}
		keys := map[string]bool{}
		for _, k := range t.LocalStorageKeys {
			if strings.TrimSpace(k) == "" {
				return fmt.Errorf("目标模板 localStorageKeys 不能包含空值: %s", t.ID)
			}
			if keys[k] {
				return fmt.Errorf("目标模板 localStorageKeys 重复: %s/%s", t.ID, k)
			}
			keys[k] = true
		}
	}
	return nil
}
