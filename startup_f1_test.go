package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestF1StartupWarningsDelivered(t *testing.T) {
	var text string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var d struct {
			Text struct {
				Content string `json:"content"`
			} `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&d)
		text = d.Text.Content
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	cfg := StartupNotificationConfig{WebhookURL: srv.URL, Message: "ready"}
	alerts := NewAlertState()
	alerts.AddStartupWarning("fixture remote unavailable")
	notifyStartup(NewWebhook(cfg), cfg, alerts, nil)
	if !strings.Contains(text, "启动成功") || !strings.Contains(text, "fixture remote unavailable") {
		t.Fatalf("warning missing: %s", text)
	}
	notifyStartup(NewWebhook(cfg), cfg, alerts, errors.New("fixture invalid target json"))
	if !strings.Contains(text, "启动失败") || !strings.Contains(text, "fixture invalid target json") {
		t.Fatalf("failure notification missing: %s", text)
	}
}
