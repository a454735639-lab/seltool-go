package main

import _ "embed"

// appName 用于通知标题前缀，与容器名/控制台保持一致。
const appName = "selenium-tool"

// 内置站点中文名表，可由面板更新覆盖（见 ptsites.go）
//
//go:embed assets/pt-sites.json
var builtinPTSitesJSON []byte

// 内置站点图标（从 MoviePilot 的 siteicon 表导出），可由 config/pt-icons.json 覆盖
//
//go:embed assets/pt-icons.json
var builtinPTIconsJSON []byte
