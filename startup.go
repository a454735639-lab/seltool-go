package main

import "strings"

func notifyStartup(w *Webhook, cfg StartupNotificationConfig, alerts *AlertState, err error) {
	if err != nil {
		w.Send("启动失败", err.Error())
		return
	}
	content := cfg.Message
	if warnings := alerts.StartupWarningText(); warnings != "" {
		content = strings.TrimSpace(content + "\n" + warnings)
	}
	w.Send("启动成功", content)
}
