package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestF1WeComBotMarkdown(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer srv.Close()

	wc := NewWeCom(WeComConfig{WebhookURL: srv.URL, MsgType: "markdown"})
	wc.Send("任务完成", "目标数: 3\n成功: 3\n失败: 0")
	if got["msgtype"] != "markdown" {
		t.Fatalf("msgtype=%v", got["msgtype"])
	}
	content := got["markdown"].(map[string]any)["content"].(string)
	if !strings.Contains(content, "selenium-tool") || !strings.Contains(content, "> 成功: 3") {
		t.Fatalf("markdown 内容不符: %s", content)
	}
}

func TestF1WeComRejectsBusinessError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":93000,"errmsg":"invalid webhook url"}`))
	}))
	defer srv.Close()

	wc := NewWeCom(WeComConfig{WebhookURL: srv.URL})
	if err := wc.sendBot("t", "c"); err == nil {
		t.Fatal("HTTP 200 + errcode!=0 必须判失败")
	}
}

func TestF1WeComAppTokenAndSend(t *testing.T) {
	var tokenCalls, sendCalls int32
	var lastSend map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/gettoken":
			atomic.AddInt32(&tokenCalls, 1)
			if r.URL.Query().Get("corpid") != "corp" || r.URL.Query().Get("corpsecret") != "secret" {
				_, _ = w.Write([]byte(`{"errcode":40001,"errmsg":"invalid credential"}`))
				return
			}
			_, _ = w.Write([]byte(`{"errcode":0,"access_token":"TOKEN123","expires_in":7200}`))
		case "/cgi-bin/message/send":
			atomic.AddInt32(&sendCalls, 1)
			if r.URL.Query().Get("access_token") != "TOKEN123" {
				_, _ = w.Write([]byte(`{"errcode":40014,"errmsg":"invalid access_token"}`))
				return
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &lastSend)
			_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	wc := NewWeCom(WeComConfig{Mode: "app", CorpID: "corp", CorpSecret: "secret", AgentID: 1000002, ToUser: "zhangsan", Proxy: srv.URL})
	wc.Send("定时执行完成", "目标数: 2")
	wc.Send("失败重试完成", "目标数: 1")

	if atomic.LoadInt32(&tokenCalls) != 1 {
		t.Fatalf("access_token 应缓存复用，实际请求 %d 次", tokenCalls)
	}
	if atomic.LoadInt32(&sendCalls) != 2 {
		t.Fatalf("send 次数=%d", sendCalls)
	}
	if lastSend["touser"] != "zhangsan" || lastSend["msgtype"] != "text" {
		t.Fatalf("请求体不符: %v", lastSend)
	}
	if int64(lastSend["agentid"].(float64)) != 1000002 {
		t.Fatalf("agentid 不符: %v", lastSend["agentid"])
	}
}

func TestF1WeComAppSplitsLongMessage(t *testing.T) {
	var chunks []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/gettoken") {
			_, _ = w.Write([]byte(`{"errcode":0,"access_token":"T","expires_in":7200}`))
			return
		}
		var body struct {
			Text struct{ Content string } `json:"text"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		chunks = append(chunks, body.Text.Content)
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer srv.Close()

	wc := NewWeCom(WeComConfig{Mode: "app", CorpID: "c", CorpSecret: "s", AgentID: 1, Proxy: srv.URL})
	long := strings.Repeat("一行内容\n", 400) // 远超 2048 字节
	wc.Send("标题", long)
	if len(chunks) < 2 {
		t.Fatalf("长消息未切分，块数=%d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 2100 {
			t.Fatalf("第 %d 块过大: %d 字节", i, len(c))
		}
	}
}

func TestF1WeComDisabledIsSilent(t *testing.T) {
	if NewWeCom(WeComConfig{}).Enabled() {
		t.Fatal("空配置不应启用")
	}
	if NewWeCom(WeComConfig{Mode: "app", CorpID: "c"}).Enabled() {
		t.Fatal("缺 corpsecret/agentid 不应启用")
	}
	// 不应 panic，也不应发起请求
	NewWeCom(WeComConfig{}).Send("x", "y")
}

func TestF1LegacyWebhookStillWorks(t *testing.T) {
	var hit int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hit, 1)
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer srv.Close()
	w := NewWebhook(StartupNotificationConfig{WebhookURL: srv.URL})
	w.Send("启动成功", "ok")
	if atomic.LoadInt32(&hit) != 1 {
		t.Fatal("旧 webhook-url 配置应继续可用")
	}
}
