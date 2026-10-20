package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestF1ProbeUsesDraftWithoutSaving(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cookiecloud/get/draft" {
			t.Errorf("draft ignored: %s", r.URL.Path)
		}
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b["password"] != "current-fixture" {
			t.Error("empty draft password did not retain current")
		}
		w.Write([]byte(`{"cookie_data":{}}`))
	}))
	defer srv.Close()
	c := NewCookieCloud(CookieCloudConfig{URL: "http://unreachable.invalid", Key: "original", Password: "current-fixture"}, NewAlertState())
	s := &Server{cookies: c, settings: NewSettingsStore(filepath.Join(t.TempDir(), "settings.json"))}
	req := httptest.NewRequest("POST", "/api/settings/cookiecloud/test", strings.NewReader(`{"url":"`+srv.URL+`/cookiecloud","key":"draft","password":""}`))
	w := httptest.NewRecorder()
	s.handleTestCookieCloud(w, req)
	if w.Code != 200 {
		t.Fatalf("draft probe %d %s", w.Code, w.Body.String())
	}
	if c.Config().Key != "original" {
		t.Fatal("draft test mutated active settings")
	}
}
