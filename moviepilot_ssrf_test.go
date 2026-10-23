package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// MoviePilot 图标同步的两个客户端不能混用：
//   - 管理员配置的接口地址通常就在内网（10.x / 192.168.x），必须放行，
//     否则图标同步功能等于废掉；
//   - 接口响应里给的第三方 url 必须受防护，否则就是免点击的 SSRF。
func TestMoviePilotIconSplitClients(t *testing.T) {
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/site/icon/1":
			// 模拟第三方（或中间人）返回一个指向内网的 url
			fmt.Fprintf(w, `{"data":{"url":"%s/api/v1/secret"}}`, base)
		case "/api/v1/site/icon/2":
			// 正常返回内联图标
			fmt.Fprintf(w, `{"data":{"icon":%q}}`,
				"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
		default:
			w.Write([]byte("SECRET"))
		}
	}))
	defer srv.Close()
	base = srv.URL

	cfg := MoviePilotSyncConfig{URL: srv.URL}

	if got := fetchMoviePilotIcon(cfg, 1); got != "" {
		t.Fatalf("第三方 url 指向内网时必须返回空，实际拿到 %q", got)
	}
	if got := fetchMoviePilotIcon(cfg, 2); !strings.HasPrefix(got, "data:image/") {
		t.Fatalf("内网 MoviePilot 接口的内联图标应正常返回，实际 %q", got)
	}
}
