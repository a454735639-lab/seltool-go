package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// vncproxy 替代 websockify：把 noVNC 的 WebSocket 流量转发到本地 VNC 端口。
// 自带静态文件服务，因此也不需要 Debian 的 novnc 包（它会拖进整个 nodejs）。

const (
	wsBufferSize = 32 * 1024
	// noVNC 固定用 binary 子协议；没有子协议的老客户端也放行（websockify 同样宽容）
	subprotocolBinary = "binary"
)

var errTextFrame = errors.New("收到非 binary 帧，已关闭连接")

// 测试里替换成会短写的连接，生产走 net.DialTimeout
var dialVNC = net.DialTimeout

var upgrader = websocket.Upgrader{
	ReadBufferSize:  wsBufferSize,
	WriteBufferSize: wsBufferSize,
	Subprotocols:    []string{subprotocolBinary},
	CheckOrigin:     checkOrigin,
}

// allowedHosts 是从环境变量 VNCPROXY_ALLOWED_HOSTS 读入的额外允许主机名/IP
// （逗号分隔）。容器场景下，请求的 Host 常是宿主机地址（如 10.0.0.1 或
// Tailscale 的 10.0.0.2），而容器内网卡只有 Docker 网段，解析不到该地址，
// 会被 isLocalRequestHost 误判为非法。把实际访问地址加进白名单即可。
var allowedHosts = func() map[string]bool {
	out := map[string]bool{}
	for _, v := range strings.Split(os.Getenv("VNCPROXY_ALLOWED_HOSTS"), ",") {
		v = strings.ToLower(strings.Trim(strings.TrimSpace(v), "[]"))
		if v != "" {
			out[v] = true
		}
	}
	return out
}()

// checkOrigin 只接受同源或没有 Origin 的请求。
// 浏览器直连时 noVNC 的 Origin 与 Host 同源；经 SSH 隧道访问时 Host/Origin 都是
// localhost/127.0.0.1 之一，按回环地址等价处理，避免把隧道挡在门外。
func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		// Origin 与 Host 字符串相等仍可能是 DNS rebinding（evil.com 解析到回环后
		// 浏览器把两个头都写成 evil.com）。先查白名单（部署者显式放行的访问地址），
		// 再校验请求 Host 真的解析到本机。
		if isAllowedHost(hostOnly(r.Host)) {
			return true
		}
		return isLocalRequestHost(r)
	}
	// 回环主机名互换（localhost ↔ 127.0.0.1 ↔ [::1]）但端口必须一致
	if isLoopbackHost(u.Hostname()) && isLoopbackHost(hostOnly(r.Host)) &&
		portOf(u.Host) == portOf(r.Host) {
		return true
	}
	// 双方都命中白名单时也放行（例如经反向代理访问，Origin 与 Host 不同名）
	return isAllowedHost(u.Hostname()) && isAllowedHost(hostOnly(r.Host))
}

// isAllowedHost 判断主机名是否在 VNCPROXY_ALLOWED_HOSTS 白名单内。
func isAllowedHost(h string) bool {
	h = strings.ToLower(strings.Trim(strings.TrimSpace(h), "[]"))
	if h == "" {
		return false
	}
	return allowedHosts[h]
}

// portOf：没写端口时按 scheme 默认端口处理不了（这里只做字符串比较，空则空）
func portOf(hostport string) string {
	_, p, err := net.SplitHostPort(hostport)
	if err != nil {
		return ""
	}
	return p
}

// isLocalRequestHost 校验请求的 Host 确实解析到回环或本机地址，
// 阻断 DNS rebinding：Origin/Host 都写成攻击者域名也进不来。
func isLocalRequestHost(r *http.Request) bool {
	host := hostOnly(r.Host)
	if host == "" {
		return false
	}
	if isLoopbackHost(host) {
		return true
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	local := map[string]bool{}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			local[ipnet.IP.String()] = true
		}
	}
	for _, ip := range ips {
		if local[ip.String()] {
			return true
		}
	}
	return false
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

func isLoopbackHost(h string) bool {
	switch strings.ToLower(strings.Trim(h, "[]")) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// subprotocolOK：没声明子协议，或声明里含 binary，才继续。
func subprotocolOK(r *http.Request) bool {
	raw := r.Header.Get("Sec-WebSocket-Protocol")
	if raw == "" {
		return true
	}
	for _, p := range strings.Split(raw, ",") {
		if strings.EqualFold(strings.TrimSpace(p), subprotocolBinary) {
			return true
		}
	}
	return false
}

// writeAll 处理 TCP 短写：Write 可能只写一部分且不报错，必须循环到写完。
func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if n > 0 {
			p = p[n:]
		}
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

// wsBinaryWriter 让 io.Copy 每块数据落成一个 binary 帧。
// io.Copy 自己会循环到写满，短写不会丢字节。
type wsBinaryWriter struct {
	c *websocket.Conn
}

func (w wsBinaryWriter) Write(p []byte) (int, error) {
	if err := w.c.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// pumpWS：WebSocket → VNC。只收 binary；text 按协议关闭（1003）。
func pumpWS(tcp net.Conn, ws *websocket.Conn) error {
	for {
		mt, data, err := ws.ReadMessage()
		if err != nil {
			return err
		}
		if mt != websocket.BinaryMessage {
			_ = ws.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseUnsupportedData, "only binary frames are supported"),
				time.Now().Add(time.Second))
			return errTextFrame
		}
		if err := writeAll(tcp, data); err != nil {
			return err
		}
	}
}

// pumpTCP：VNC → WebSocket。
func pumpTCP(tcp net.Conn, ws *websocket.Conn) error {
	_, err := io.CopyBuffer(wsBinaryWriter{c: ws}, tcp, make([]byte, wsBufferSize))
	return err
}

// relay 双向搬运；任一侧结束就关掉两个连接，让另一个协程的阻塞读立刻返回。
func relay(tcp net.Conn, ws *websocket.Conn) {
	done := make(chan struct{}, 2)
	go func() { defer func() { done <- struct{}{} }(); _ = pumpWS(tcp, ws) }()
	go func() { defer func() { done <- struct{}{} }(); _ = pumpTCP(tcp, ws) }()
	<-done
	_ = tcp.Close()
	_ = ws.Close()
	<-done
}

func serveVNC(target string, dialTimeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !subprotocolOK(r) {
			http.Error(w, "only the binary subprotocol is supported", http.StatusBadRequest)
			return
		}
		// 先拨后端再升级：后端不通时回 502，而不是先 101 再断，客户端能看懂错误
		tcp, err := dialVNC("tcp", target, dialTimeout)
		if err != nil {
			log.Printf("连接 VNC %s 失败: %v", target, err)
			http.Error(w, "VNC backend unavailable", http.StatusBadGateway)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			_ = tcp.Close()
			log.Printf("websocket 升级失败: %v", err)
			return
		}
		relay(tcp, conn)
	}
}

// probeRFB 探测 VNC 端口：读到 "RFB " 横幅才算就绪。
// entrypoint 用它判断 x11vnc 是否真在服务，不引入 python/node/curl。
func probeRFB(addr string, timeout time.Duration) error {
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 12)
	if _, err := io.ReadFull(c, buf); err != nil {
		return err
	}
	if !strings.HasPrefix(string(buf), "RFB ") {
		return fmt.Errorf("不是 RFB 服务: %q", buf)
	}
	return nil
}

func main() {
	listen := flag.String("listen", ":7900", "HTTP 监听地址")
	target := flag.String("target", "127.0.0.1:5900", "VNC 服务地址")
	web := flag.String("web", "/usr/share/novnc", "noVNC 静态文件目录")
	probe := flag.String("tcp-probe", "", "只探测该 TCP 地址的 RFB 横幅，就绪返回 0，否则返回 1")
	probeTimeout := flag.Duration("tcp-probe-timeout", 2*time.Second, "探测超时")
	dialTimeout := flag.Duration("dial-timeout", 10*time.Second, "后端拨号超时")
	flag.Parse()

	if *probe != "" {
		if err := probeRFB(*probe, *probeTimeout); err != nil {
			fmt.Fprintf(os.Stderr, "探测 %s 失败: %v\n", *probe, err)
			os.Exit(1)
		}
		fmt.Printf("RFB 就绪: %s\n", *probe)
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/websockify", serveVNC(*target, *dialTimeout))
	mux.Handle("/", http.FileServer(http.Dir(*web)))

	srv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("VNC 代理监听 %s，转发到 %s，静态目录 %s", *listen, *target, *web)
	log.Fatal(srv.ListenAndServe())
}
