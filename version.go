package main

import (
	_ "embed"
	"net/http"
	"strings"
)

// versionFile 是仓库根目录的 VERSION 文件，编译期嵌入。
// 这样镜像内不需要额外挂载文件也能报告自身版本。
//
//go:embed VERSION
var versionFile string

// buildVersion 可由 CI 通过 -ldflags "-X main.buildVersion=..." 覆盖，
// 留空时回退到嵌入的 VERSION 文件内容。
var buildVersion string

// appVersion 返回当前运行的镜像版本号，例如 "1.0.8"。
func appVersion() string {
	if v := strings.TrimSpace(buildVersion); v != "" {
		return v
	}
	if v := strings.TrimSpace(versionFile); v != "" {
		return v
	}
	return "dev"
}

// handleVersion 返回版本信息。无需鉴权：登录页也要显示版本号，
// 且该接口只暴露版本字符串，不含任何环境或配置信息。
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"version": appVersion()})
}
