package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestF1BrowserResourcesAndStorage(t *testing.T) {
	if os.Getenv("SELTOOL_BROWSER_TEST") != "1" {
		t.Skip("requires Chromium + Xvfb")
	}
	var png, webp atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a.png":
			png.Add(1)
			w.Write([]byte("png"))
		case "/b.webp":
			webp.Add(1)
			w.Write([]byte("webp"))
		case "/warmup":
			w.Write([]byte(`<html><body>init</body></html>`))
		case "/verify":
			ck, e := r.Cookie("f1sid")
			if e == nil && ck.Value == "fixture" {
				w.Write([]byte("true"))
			} else {
				w.Write([]byte("false"))
			}
		default:
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<html><title>F1 integration</title><body><img src="/a.png?fixture=1"><img src="/b.webp"><script>fetch('/verify').then(r=>r.text()).then(s=>{if(s==='true'&&localStorage.getItem('f1token')==='stored'){let x=document.createElement('div');x.id='verified';document.body.appendChild(x)}})</script></body></html>`))
		}
	}))
	defer srv.Close()
	cfg := defaultConfig().Automation.Browser
	cfg.UserDataDir = t.TempDir()
	cfg.Headless = false
	cfg.BinaryPath = "/usr/bin/chromium"
	cfg.DisableImages = false
	cfg.PageStaySeconds = 1
	cfg.BlockResourceSuffixes = []string{"png"}
	cfg.PageLoadTimeout = Duration(8 * time.Second)
	b, err := NewBrowser(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	tr := Target{Name: "F1 fixture", URL: srv.URL + "/", WarmupPath: "/warmup", OKSelector: "#verified", LocalStorage: []LocalStorage{{Key: "f1token", Value: "stored"}}}
	err = b.Visit(tr, []Cookie{{Name: "f1sid", Value: "fixture", Domain: "127.0.0.1", Path: "/", HTTPOnly: true, HostOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	if png.Load() != 0 {
		t.Fatalf("blocked png reached server %d", png.Load())
	}
	if webp.Load() == 0 {
		t.Fatal("unblocked webp not requested")
	}
	if !b.Alive() {
		t.Fatal("browser-domain liveness probe failed")
	}
}
