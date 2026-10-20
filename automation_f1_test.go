package main

import (
	"path/filepath"
	"testing"
)

func TestF1StartupBrowserFailureHasError(t *testing.T) {
	cfg := defaultConfig()
	cfg.Automation.Browser.BinaryPath = "/missing-fixture-chromium"
	cfg.Automation.Browser.UserDataDir = t.TempDir()
	store := NewTargetStore(filepath.Join(t.TempDir(), "targets.json"))
	_, err := store.Save([]Target{{URL: "http://fixture.invalid/", LocalStorage: []LocalStorage{}}})
	if err != nil {
		t.Fatal(err)
	}
	alerts := NewAlertState()
	a := NewAutomation(cfg, store, NewHistory(filepath.Join(t.TempDir(), "history.json")), NewCookieCloud(CookieCloudConfig{}, alerts), NewWebhook(StartupNotificationConfig{}), alerts)
	defer a.Shutdown()
	if !a.ExecuteOnce("启动执行") {
		t.Fatal("task not accepted")
	}
	if a.TaskError() == nil {
		t.Fatal("browser startup failure reported as successful startup")
	}
}
