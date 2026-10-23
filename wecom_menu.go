package main

import (
	"net/http"
)

// handleCreateWeComMenu 在企业微信工作台创建「PT 保活」指令菜单。
// 菜单项全部是 click 类型，点了就是给应用发一条文字指令（暂停定时 / 全部重试 / 站点导入），
// 不依赖任何网页，因此不需要外网域名或快捷页密钥。
func (s *Server) handleCreateWeComMenu(w http.ResponseWriter, r *http.Request) {
	ns := s.notify.Current()
	wecom := NewWeCom(ns.toConfig())
	if err := wecom.CreateQuickMenu(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "已创建，刷新企业微信工作台即可见「PT 保活」菜单"})
}
