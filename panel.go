package main

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// 企业微信工作台 H5 快捷页（/pt-panel?k=密钥）调用的接口。
// 页面本身公开，数据与操作都靠 ?key= 访问密钥鉴权（面板「通知设置 → 快捷指令页」生成）。

// panelKeyOK 校验 ?key= 是否匹配配置的面板密钥（未配置则一律拒绝）。
func (s *Server) panelKeyOK(r *http.Request) bool {
	ns := s.notify.Current()
	if ns.PanelKey == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(ns.PanelKey), []byte(r.URL.Query().Get("key"))) == 1
}

func panelDeny(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, map[string]any{"error": "密钥无效或未配置"})
}

// handlePanelSummary 返回状态概览：运行态、总数、成功/失败、失败站列表。
func (s *Server) handlePanelSummary(w http.ResponseWriter, r *http.Request) {
	if !s.panelKeyOK(r) {
		panelDeny(w)
		return
	}
	targets := s.targets.Get()
	latest := s.history.Latest()
	norm := func(u string) string { return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(u), "/")) }
	byURL := make(map[string]Target, len(targets))
	for _, t := range targets {
		byURL[norm(t.URL)] = t
	}
	success, failed := 0, 0
	type fItem struct {
		Name   string `json:"name"`
		URL    string `json:"url"`
		Reason string `json:"reason"`
	}
	list := []fItem{}
	for url, rec := range latest {
		if rec.Success {
			success++
			continue
		}
		failed++
		cat, _ := classifyFailure(byURL[norm(url)], rec.Message)
		name := byURL[norm(url)].DisplayName()
		if name == "" {
			name = url
		}
		list = append(list, fItem{Name: name, URL: url, Reason: cat})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{
		"running":    s.automation.IsRunning(),
		"total":      len(targets),
		"success":    success,
		"failed":     failed,
		"failedList": list,
	})
}

// handlePanelRetryFailed 一键重试所有失败/未运行目标。
func (s *Server) handlePanelRetryFailed(w http.ResponseWriter, r *http.Request) {
	if !s.panelKeyOK(r) {
		panelDeny(w)
		return
	}
	sub := s.automation.ExecuteFailedAsync()
	switch sub.Result {
	case BatchAccepted:
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "count": len(sub.TargetURLs)})
	case BatchBusy:
		writeJSON(w, http.StatusConflict, map[string]any{"accepted": false, "message": "任务执行中，请稍候"})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"accepted": false, "message": "没有失败目标"})
	}
}

// handlePanelRetry 重试单个目标（?url= 指定）。
func (s *Server) handlePanelRetry(w http.ResponseWriter, r *http.Request) {
	if !s.panelKeyOK(r) {
		panelDeny(w)
		return
	}
	url := r.URL.Query().Get("url")
	switch s.automation.ExecuteTargetAsync(url) {
	case ExecAccepted:
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
	case ExecBusy:
		writeJSON(w, http.StatusConflict, map[string]any{"accepted": false, "message": "任务执行中，请稍候"})
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "目标不存在"})
	}
}

// handleCreateWeComMenu 调用企微 API 在工作台创建「PT 保活」快捷菜单。
func (s *Server) handleCreateWeComMenu(w http.ResponseWriter, r *http.Request) {
	ns := s.notify.Current()
	key := ns.PanelKey
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请先填「快捷指令页密钥」并保存"})
		return
	}
	host := ns.PanelHost
	if host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请先填「外网域名」并保存"})
		return
	}
	if !strings.HasPrefix(host, "http") {
		host = "https://" + host
	}
	statusURL := host + "/pt-panel?k=" + url.QueryEscape(key)
	retryURL := statusURL + "&action=retryall"
	wecom := NewWeCom(ns.toConfig())
	if err := wecom.CreateQuickMenu(statusURL, retryURL); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "已创建，刷新企业微信工作台即可见「PT 保活」菜单"})
}
