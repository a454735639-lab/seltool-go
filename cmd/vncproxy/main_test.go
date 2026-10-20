package main

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ---------- 短写 ----------

// shortConn 每次 Write 最多写 max 字节，模拟 TCP 短写。
type shortConn struct {
	net.Conn
	max int
}

func (c shortConn) Write(p []byte) (int, error) {
	if len(p) > c.max {
		p = p[:c.max]
	}
	return c.Conn.Write(p)
}

func stubDial(t *testing.T, fn func() (net.Conn, error)) {
	t.Helper()
	old := dialVNC
	dialVNC = func(_, _ string, _ time.Duration) (net.Conn, error) { return fn() }
	t.Cleanup(func() { dialVNC = old })
}

func TestWriteAllHandlesShortWrites(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 1000)
	var buf bytes.Buffer
	if err := writeAll(struct{ io.Writer }{&buf}, payload); err != nil {
		t.Fatalf("writeAll: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), payload) {
		t.Fatalf("writeAll 丢了字节: got %d want %d", buf.Len(), len(payload))
	}
	// 0,nil 必须当成错误，否则死循环
	if err := writeAll(zeroWriter{}, []byte("abc")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("0 字节写应返回 ErrShortWrite, got %v", err)
	}
}

type zeroWriter struct{}

func (zeroWriter) Write(p []byte) (int, error) { return 0, nil }

func TestRelayShortWriteKeepsAllBytes(t *testing.T) {
	serverEnd, backendEnd := net.Pipe()
	stubDial(t, func() (net.Conn, error) { return shortConn{Conn: serverEnd, max: 3}, nil })

	srv := httptest.NewServer(http.HandlerFunc(serveVNC("ignored", time.Second)))
	defer srv.Close()

	ws := dialWS(t, srv.URL, []string{subprotocolBinary})
	defer ws.Close()

	payload := bytes.Repeat([]byte("A"), 7000)
	if err := ws.WriteMessage(websocket.BinaryMessage, payload); err != nil {
		t.Fatalf("写 WS: %v", err)
	}
	_ = backendEnd.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(backendEnd, got); err != nil {
		t.Fatalf("读后端: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("短写路径丢了字节")
	}
}

// ---------- 只接受 binary ----------

func TestTextFrameRejected(t *testing.T) {
	serverEnd, backendEnd := net.Pipe()
	stubDial(t, func() (net.Conn, error) { return serverEnd, nil })

	srv := httptest.NewServer(http.HandlerFunc(serveVNC("ignored", time.Second)))
	defer srv.Close()

	ws := dialWS(t, srv.URL, []string{subprotocolBinary})
	defer ws.Close()
	if err := ws.WriteMessage(websocket.TextMessage, []byte("hello")); err != nil {
		t.Fatalf("写 text 帧: %v", err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err := ws.ReadMessage()
	var ce *websocket.CloseError
	if !errors.As(err, &ce) {
		t.Fatalf("期望 close 帧, got %v", err)
	}
	if ce.Code != websocket.CloseUnsupportedData {
		t.Fatalf("close code = %d, want %d", ce.Code, websocket.CloseUnsupportedData)
	}
	// 后端不应收到任何字节
	_ = backendEnd.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, 4)
	if n, _ := backendEnd.Read(buf); n != 0 {
		t.Fatalf("text 帧被转发到 VNC: %q", buf[:n])
	}
}

// ---------- 后端不通给 502 ----------

func TestBackendDialFailIs502(t *testing.T) {
	stubDial(t, func() (net.Conn, error) { return nil, errors.New("connection refused") })

	srv := httptest.NewServer(http.HandlerFunc(serveVNC("ignored", time.Second)))
	defer srv.Close()

	c, err := net.DialTimeout("tcp", strings.TrimPrefix(srv.URL, "http://"), 3*time.Second)
	if err != nil {
		t.Fatalf("拨号: %v", err)
	}
	defer c.Close()
	req := "GET /websockify HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\n" +
		"Connection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: binary\r\n\r\n"
	if _, err := c.Write([]byte(req)); err != nil {
		t.Fatalf("写请求: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 64)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("读响应: %v", err)
	}
	line := string(buf[:n])
	if !strings.Contains(line, "502") {
		t.Fatalf("期望 502, got %q", line)
	}
	if strings.Contains(line, "101") {
		t.Fatalf("不应先升级再断: %q", line)
	}
}

// ---------- 子协议 ----------

func TestBinarySubprotocolNegotiated(t *testing.T) {
	serverEnd, backendEnd := net.Pipe()
	stubDial(t, func() (net.Conn, error) { return serverEnd, nil })
	srv := httptest.NewServer(http.HandlerFunc(serveVNC("ignored", time.Second)))
	defer srv.Close()

	ws := dialWS(t, srv.URL, []string{"binary", "base64"})
	defer ws.Close()
	if got := ws.Subprotocol(); got != subprotocolBinary {
		t.Fatalf("子协议 = %q, want binary", got)
	}
	_ = backendEnd.Close()

	// 没有子协议的客户端也放行
	serverEnd2, backendEnd2 := net.Pipe()
	stubDial(t, func() (net.Conn, error) { return serverEnd2, nil })
	ws2 := dialWS(t, srv.URL, nil)
	defer ws2.Close()
	if got := ws2.Subprotocol(); got != "" {
		t.Fatalf("无子协议时应为空, got %q", got)
	}
	_ = backendEnd2.Close()

	// 只有 text 子协议：明确拒绝
	_, resp, err := websocket.DefaultDialer.Dial(wsURL(srv.URL), http.Header{
		"Sec-WebSocket-Protocol": []string{"text"},
	})
	if err == nil {
		t.Fatal("text 子协议不应升级成功")
	}
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("期望 400, got %+v", resp)
	}
}

// ---------- Origin ----------

func TestCheckOrigin(t *testing.T) {
	cases := []struct {
		name, origin, host string
		want               bool
	}{
		{"无 Origin", "", "127.0.0.1:7900", true},
		{"同源", "http://127.0.0.1:7900", "127.0.0.1:7900", true},
		{"隧道 localhost→127.0.0.1", "http://localhost:7900", "127.0.0.1:7900", true},
		{"隧道 ::1", "http://[::1]:7900", "127.0.0.1:7900", true},
		{"外部站点", "http://evil.example", "127.0.0.1:7900", false},
		{"同主机不同端口", "http://127.0.0.1:8080", "127.0.0.1:7900", false},
		{"Origin 非法", "://bad", "127.0.0.1:7900", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/websockify", nil)
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := checkOrigin(r); got != c.want {
			t.Errorf("%s: checkOrigin = %v, want %v", c.name, got, c.want)
		}
	}
}

// ---------- 双向退出 ----------

func TestRelayExitsBothWays(t *testing.T) {
	for _, side := range []string{"ws", "tcp"} {
		serverEnd, backendEnd := net.Pipe()
		stubDial(t, func() (net.Conn, error) { return serverEnd, nil })
		srv := httptest.NewServer(http.HandlerFunc(serveVNC("ignored", time.Second)))
		ws := dialWS(t, srv.URL, []string{subprotocolBinary})

		start := time.Now()
		if side == "ws" {
			ws.Close()
		} else {
			backendEnd.Close()
		}
		_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				break
			}
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("%s 侧关闭后代理未退出 (%v)", side, d)
		}
		ws.Close()
		srv.Close()
	}
}

// ---------- RFB 探测 ----------

func TestProbeRFB(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				c.Write([]byte("RFB 003.008\n"))
				time.Sleep(50 * time.Millisecond)
			}(c)
		}
	}()
	if err := probeRFB(ln.Addr().String(), time.Second); err != nil {
		t.Fatalf("probeRFB: %v", err)
	}

	ln2, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln2.Close()
	go func() {
		c, err := ln2.Accept()
		if err != nil {
			return
		}
		c.Write([]byte("HTTP/1.1 200 OK\r\n"))
		c.Close()
	}()
	if err := probeRFB(ln2.Addr().String(), time.Second); err == nil {
		t.Fatal("非 RFB 服务应探测失败")
	}
	if err := probeRFB("127.0.0.1:1", 200*time.Millisecond); err == nil {
		t.Fatal("无监听端口应探测失败")
	}
}

// ---------- helpers ----------

func wsURL(base string) string {
	return "ws" + strings.TrimPrefix(base, "http") + "/websockify"
}

func dialWS(t *testing.T, base string, protocols []string) *websocket.Conn {
	t.Helper()
	d := websocket.Dialer{Subprotocols: protocols, HandshakeTimeout: 3 * time.Second}
	c, _, err := d.Dial(wsURL(base), nil)
	if err != nil {
		t.Fatalf("WS 拨号: %v", err)
	}
	return c
}
