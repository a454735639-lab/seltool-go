package main

// 独立验收客户端：只用 Go 标准库。
// 手写 WebSocket 客户端 + RFB 3.8 协议 + VNC DES 认证，验证真实 /websockify 交互。
import (
	"bufio"
	"crypto/des"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// ---------------- WebSocket ----------------

type WS struct {
	c net.Conn
	r *bufio.Reader
}

func dialWS(addr, path string) (*WS, error) {
	c, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return nil, err
	}
	key := make([]byte, 16)
	rand.Read(key)
	k := base64.StdEncoding.EncodeToString(key)
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\n"+
		"Connection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n"+
		"Sec-WebSocket-Protocol: binary\r\n\r\n", path, addr, k)
	if _, err := c.Write([]byte(req)); err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(c, 1<<20)
	line, err := br.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !contains(line, "101") {
		return nil, fmt.Errorf("websocket 升级失败: %s", trim(line))
	}
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if os.Getenv("VNC_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "[dbg] hdr %q\n", l)
		}
		if l == "\r\n" || l == "\n" {
			break
		}
	}
	return &WS{c: c, r: br}, nil
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func trim(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

func (w *WS) writeFrame(op byte, p []byte) error {
	hdr := []byte{0x80 | op}
	n := len(p)
	switch {
	case n < 126:
		hdr = append(hdr, byte(n)|0x80)
	case n <= 65535:
		hdr = append(hdr, 126|0x80, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 127|0x80)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		hdr = append(hdr, b[:]...)
	}
	mask := make([]byte, 4)
	rand.Read(mask)
	hdr = append(hdr, mask...)
	buf := make([]byte, n)
	for i := 0; i < n; i++ {
		buf[i] = p[i] ^ mask[i%4]
	}
	_, err := w.c.Write(append(hdr, buf...))
	return err
}

func (w *WS) Write(p []byte) error { return w.writeFrame(0x2, p) }

// send 把 RFB 字节封成单个二进制帧（websockify 语义：一个 WS 帧 = 一段 RFB 流）
func (r *RFB) send(p []byte) error { return r.ws.writeFrame(0x2, p) }

func (w *WS) ReadMessage() ([]byte, error) {
	var payload []byte
	for {
		h := make([]byte, 2)
		if _, err := io.ReadFull(w.r, h); err != nil {
			return nil, err
		}
		fin := h[0]&0x80 != 0
		op := h[0] & 0x0f
		masked := h[1]&0x80 != 0
		ln := int64(h[1] & 0x7f)
		if ln == 126 {
			var b [2]byte
			if _, err := io.ReadFull(w.r, b[:]); err != nil {
				return nil, err
			}
			ln = int64(binary.BigEndian.Uint16(b[:]))
		} else if ln == 127 {
			var b [8]byte
			if _, err := io.ReadFull(w.r, b[:]); err != nil {
				return nil, err
			}
			ln = int64(binary.BigEndian.Uint64(b[:]))
		}
		var mask []byte
		if masked {
			mask = make([]byte, 4)
			if _, err := io.ReadFull(w.r, mask); err != nil {
				return nil, err
			}
		}
		data := make([]byte, ln)
		if _, err := io.ReadFull(w.r, data); err != nil {
			return nil, err
		}
		if masked {
			for i := range data {
				data[i] ^= mask[i%4]
			}
		}
		switch op {
		case 0x8:
			return nil, io.EOF
		case 0x9:
			w.writeFrame(0xA, data)
			continue
		case 0xA:
			continue
		}
		payload = append(payload, data...)
		if fin {
			return payload, nil
		}
	}
}

// ---------------- RFB ----------------

type RFB struct {
	ws   *WS
	buf  []byte
	W, H int
	Bpp  int
	Rmax, Gmax, Bmax uint16
	Rsh, Gsh, Bsh     uint8
	Name string
}

func (r *RFB) fill(n int, timeout time.Duration) error {
	for len(r.buf) < n {
		r.ws.c.SetReadDeadline(time.Now().Add(timeout))
		m, err := r.ws.ReadMessage()
		if err != nil {
			return err
		}
		r.buf = append(r.buf, m...)
	}
	return nil
}

func (r *RFB) ReadFull(p []byte) error {
	if err := r.fill(len(p), 10*time.Second); err != nil {
		return err
	}
	copy(p, r.buf[:len(p)])
	r.buf = r.buf[len(p):]
	return nil
}

func revbits(b byte) byte {
	var r byte
	for i := 0; i < 8; i++ {
		r = r<<1 | (b>>uint(i))&1
	}
	return r
}

func vncKey(pass string) []byte {
	k := make([]byte, 8)
	copy(k, []byte(pass))
	for i := 0; i < 8; i++ {
		k[i] = revbits(k[i])
	}
	return k
}

// handshake 完成版本协商 + 认证；返回认证结果（nil 表示成功）。
func (r *RFB) handshake(pass string) (secTypes []byte, authErr error) {
	ver := make([]byte, 12)
	if err := r.ReadFull(ver); err != nil {
		return nil, fmt.Errorf("读取版本失败: %w", err)
	}
	serverVer := string(ver)
	if err := r.send([]byte("RFB 003.008\n")); err != nil {
		return nil, err
	}
	var cnt [1]byte
	if err := r.ReadFull(cnt[:]); err != nil {
		return nil, err
	}
	if cnt[0] == 0 {
		var ln [4]byte
		r.ReadFull(ln[:])
		n := binary.BigEndian.Uint32(ln[:])
		reason := make([]byte, n)
		r.ReadFull(reason)
		return nil, fmt.Errorf("服务端拒绝: %s", reason)
	}
	secTypes = make([]byte, cnt[0])
	if err := r.ReadFull(secTypes); err != nil {
		return nil, err
	}
	_ = serverVer
	found := false
	for _, t := range secTypes {
		if t == 2 {
			found = true
		}
	}
	if !found {
		return secTypes, fmt.Errorf("服务端未提供 VNC 认证(2)，提供: %v", secTypes)
	}
	if err := r.send([]byte{2}); err != nil {
		return secTypes, err
	}
	chal := make([]byte, 16)
	if err := r.ReadFull(chal); err != nil {
		return secTypes, err
	}
	blk, err := des.NewCipher(vncKey(pass))
	if err != nil {
		return secTypes, err
	}
	resp := make([]byte, 16)
	blk.Encrypt(resp[0:8], chal[0:8])
	blk.Encrypt(resp[8:16], chal[8:16])
	if err := r.send(resp); err != nil {
		return secTypes, err
	}
	var res [4]byte
	if err := r.ReadFull(res[:]); err != nil {
		return secTypes, fmt.Errorf("读取认证结果失败: %w", err)
	}
	if binary.BigEndian.Uint32(res[:]) != 0 {
		var ln [4]byte
		r.ReadFull(ln[:])
		n := binary.BigEndian.Uint32(ln[:])
		reason := make([]byte, n)
		r.ReadFull(reason)
		return secTypes, fmt.Errorf("认证被拒绝: %s", string(reason))
	}
	return secTypes, nil
}

func (r *RFB) clientInitShared() error {
	if err := r.send([]byte{1}); err != nil { // shared=1
		return err
	}
	hdr := make([]byte, 4)
	if err := r.ReadFull(hdr); err != nil {
		return err
	}
	r.W = int(binary.BigEndian.Uint16(hdr[0:2]))
	r.H = int(binary.BigEndian.Uint16(hdr[2:4]))
	pf := make([]byte, 16)
	if err := r.ReadFull(pf); err != nil {
		return err
	}
	r.Bpp = int(pf[0])
	r.Rmax = binary.BigEndian.Uint16(pf[4:6])
	r.Gmax = binary.BigEndian.Uint16(pf[6:8])
	r.Bmax = binary.BigEndian.Uint16(pf[8:10])
	r.Rsh, r.Gsh, r.Bsh = pf[10], pf[11], pf[12]
	var nl [4]byte
	if err := r.ReadFull(nl[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(nl[:])
	name := make([]byte, n)
	if err := r.ReadFull(name); err != nil {
		return err
	}
	r.Name = string(name)
	return nil
}

func (r *RFB) setRawEncoding() error {
	msg := make([]byte, 4+4)
	msg[0] = 2
	binary.BigEndian.PutUint16(msg[2:4], 1)
	binary.BigEndian.PutUint32(msg[4:8], 0) // Raw
	return r.send(msg)
}

func (r *RFB) pointer(mask byte, x, y int) error {
	m := make([]byte, 6)
	m[0] = 5
	m[1] = mask
	binary.BigEndian.PutUint16(m[2:4], uint16(x))
	binary.BigEndian.PutUint16(m[4:6], uint16(y))
	return r.send(m)
}

func (r *RFB) key(down bool, keysym uint32) error {
	m := make([]byte, 8)
	m[0] = 4
	if down {
		m[1] = 1
	}
	binary.BigEndian.PutUint32(m[4:8], keysym)
	return r.send(m)
}

type Rect struct {
	X, Y, W, H int
	Enc        int32
}

// capture 请求整屏原始像素，返回 BGRA 缓冲区 + 矩形列表。
func (r *RFB) capture(incremental bool) ([]byte, []Rect, error) {
	req := make([]byte, 10)
	req[0] = 3
	if incremental {
		req[1] = 1
	}
	binary.BigEndian.PutUint16(req[6:8], uint16(r.W))
	binary.BigEndian.PutUint16(req[8:10], uint16(r.H))
	if err := r.send(req); err != nil {
		return nil, nil, err
	}
	fb := make([]byte, r.W*r.H*4)
	var rects []Rect
	got := 0
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var t [1]byte
		if err := r.fill(1, 4*time.Second); err != nil {
			if len(rects) > 0 {
				break
			}
			return nil, rects, err
		}
		copy(t[:], r.buf[:1])
		r.buf = r.buf[1:]
		switch t[0] {
		case 0:
			h := make([]byte, 3)
			if err := r.ReadFull(h); err != nil {
				return nil, rects, err
			}
			nr := int(binary.BigEndian.Uint16(h[1:3]))
			for i := 0; i < nr; i++ {
				rh := make([]byte, 12)
				if err := r.ReadFull(rh); err != nil {
					return nil, rects, err
				}
				rc := Rect{
					X: int(binary.BigEndian.Uint16(rh[0:2])), Y: int(binary.BigEndian.Uint16(rh[2:4])),
					W: int(binary.BigEndian.Uint16(rh[4:6])), H: int(binary.BigEndian.Uint16(rh[6:8])),
					Enc: int32(binary.BigEndian.Uint32(rh[8:12])),
				}
				rects = append(rects, rc)
				if rc.Enc != 0 {
					return nil, rects, fmt.Errorf("收到非 Raw 编码 %d", rc.Enc)
				}
				px := make([]byte, rc.W*rc.H*4)
				if err := r.ReadFull(px); err != nil {
					return nil, rects, err
				}
				for y := 0; y < rc.H; y++ {
					src := y * rc.W * 4
					dst := ((rc.Y+y)*r.W + rc.X) * 4
					if dst+rc.W*4 <= len(fb) {
						copy(fb[dst:dst+rc.W*4], px[src:src+rc.W*4])
					}
				}
				got++
			}
			return fb, rects, nil
		case 1: // SetColourMapEntries
			h := make([]byte, 5)
			r.ReadFull(h)
			n := int(binary.BigEndian.Uint16(h[3:5]))
			r.ReadFull(make([]byte, n*6))
		case 2: // Bell
		case 3: // ServerCutText
			h := make([]byte, 7)
			r.ReadFull(h)
			n := binary.BigEndian.Uint32(h[3:7])
			r.ReadFull(make([]byte, n))
		default:
			return nil, rects, fmt.Errorf("未知消息类型 %d", t[0])
		}
	}
	return fb, rects, nil
}

type Stats struct {
	Distinct int    `json:"distinct_colors"`
	MD5      string `json:"md5"`
	NonBlack int    `json:"nonblack_pixels"`
}

func stats(fb []byte) Stats {
	seen := map[uint32]struct{}{}
	nb := 0
	for i := 0; i+3 < len(fb); i += 4 {
		v := binary.LittleEndian.Uint32(fb[i : i+4])
		if len(seen) < 4096 {
			seen[v] = struct{}{}
		}
		if v&0x00ffffff != 0 {
			nb++
		}
	}
	h := md5.Sum(fb)
	return Stats{Distinct: len(seen), MD5: hex.EncodeToString(h[:]), NonBlack: nb}
}

func errOf(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}

func diffPixels(a, b []byte) int {
	n := 0
	for i := 0; i+3 < len(a) && i+3 < len(b); i += 4 {
		if a[i] != b[i] || a[i+1] != b[i+1] || a[i+2] != b[i+2] {
			n++
		}
	}
	return n
}

func writePPM(path string, fb []byte, w, h int, rsh, gsh, bsh uint8) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintf(f, "P6\n%d %d\n255\n", w, h)
	out := make([]byte, 0, w*h*3)
	for i := 0; i+3 < len(fb); i += 4 {
		v := binary.LittleEndian.Uint32(fb[i : i+4])
		out = append(out, byte(v>>rsh), byte(v>>gsh), byte(v>>bsh))
	}
	_, err = f.Write(out)
	return err
}

func main() {
	addr := flag.String("addr", "127.0.0.1:7900", "vncproxy 地址")
	good := flag.String("pass", "", "正确的 VNC 密码")
	bad := flag.String("bad", "definitely-wrong-pw", "错误的 VNC 密码")
	out := flag.String("out", "/tmp", "输出目录")
	flag.Parse()

	res := map[string]any{}
	emit := func(k string, v any) { res[k] = v }

	// 1. 静态资源 HTTP
	func() {
		cl := &http.Client{Timeout: 5 * time.Second}
		r, err := cl.Get("http://" + *addr + "/vnc.html")
		if err != nil {
			emit("http", map[string]any{"error": err.Error()})
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		r.Body.Close()
		emit("http", map[string]any{
			"status": r.StatusCode,
			"bytes":  len(body),
			"has_novnc": contains(string(body), "noVNC") || contains(string(body), "vnc"),
		})
	}()

	// 2. 错误密码必须被拒
	func() {
		ws, err := dialWS(*addr, "/websockify")
		if err != nil {
			emit("auth_wrong", map[string]any{"error": err.Error()})
			return
		}
		r := &RFB{ws: ws}
		types, authErr := r.handshake(*bad)
		ws.c.Close()
		m := map[string]any{"sec_types": types}
		if authErr != nil {
			m["rejected"] = true
			m["reason"] = authErr.Error()
		} else {
			m["rejected"] = false
		}
		emit("auth_wrong", m)
	}()

	// 3. 正确密码 + 完整会话
	ws, err := dialWS(*addr, "/websockify")
	if err != nil {
		emit("fatal", "dial: "+err.Error())
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
		os.Exit(1)
	}
	r := &RFB{ws: ws}
	types, err := r.handshake(*good)
	if err != nil {
		emit("auth_ok", map[string]any{"sec_types": types, "error": err.Error()})
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
		os.Exit(1)
	}
	emit("auth_ok", map[string]any{"sec_types": types, "ok": true})
	if err := r.clientInitShared(); err != nil {
		emit("fatal", "clientinit: "+err.Error())
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
		os.Exit(1)
	}
	emit("server_init", map[string]any{
		"width": r.W, "height": r.H, "bpp": r.Bpp,
		"truecolor": true, "name": r.Name,
		"rmax": r.Rmax, "gmax": r.Gmax, "bmax": r.Bmax,
	})
	if err := r.setRawEncoding(); err != nil {
		emit("fatal", "setenc: "+err.Error())
	}

	fb1, rects1, err := r.capture(false)
	if err != nil {
		emit("framebuffer", map[string]any{"error": err.Error()})
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
		os.Exit(1)
	}
	s1 := stats(fb1)
	writePPM(*out+"/fb1.ppm", fb1, r.W, r.H, r.Rsh, r.Gsh, r.Bsh)
	emit("framebuffer", map[string]any{
		"rects": len(rects1), "rect_list": rects1,
		"bytes": len(fb1), "stats": s1,
	})

	// 4. 鼠标：右键根窗口弹 fluxbox 菜单（左下角空白区，避开居中弹窗）
	time.Sleep(300 * time.Millisecond)
	r.pointer(0, 60, r.H-60)
	r.pointer(4, 60, r.H-60) // button1
	r.pointer(0, 60, r.H-60)
	time.Sleep(800 * time.Millisecond)
	fb2, _, err2 := r.capture(false)
	if err2 != nil {
		emit("mouse", map[string]any{"error": err2.Error()})
	} else {
		writePPM(*out+"/fb2.ppm", fb2, r.W, r.H, r.Rsh, r.Gsh, r.Bsh)
		emit("mouse", map[string]any{
			"action": "right click (button3) at (60,H-60) -> fluxbox root menu",
			"diff_pixels_vs_fb1": diffPixels(fb1, fb2),
			"stats": stats(fb2),
		})
	}
	// 关闭菜单
	r.key(true, 0xff1b)
	r.key(false, 0xff1b)
	time.Sleep(500 * time.Millisecond)

	// 5. 鼠标点击 xev 窗口 + 键盘输入，随后外部读取 xev 日志核对
	r.pointer(0, 260, 240)
	r.pointer(1, 260, 240)
	r.pointer(0, 260, 240)
	time.Sleep(300 * time.Millisecond)
	for _, ks := range []uint32{0x61, 0x62, 0x63, 0xff0d} { // a b c Return
		r.key(true, ks)
		r.key(false, ks)
		time.Sleep(150 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	fb3, _, err3 := r.capture(false)
	if err3 == nil {
		writePPM(*out+"/fb3.ppm", fb3, r.W, r.H, r.Rsh, r.Gsh, r.Bsh)
		emit("xev_input", map[string]any{"stats": stats(fb3), "diff_vs_fb1": diffPixels(fb1, fb3)})
	}

	// 6. 断线重连：关闭连接后重新认证，验证会话存活 + 画面一致
	ws.c.Close()
	time.Sleep(1 * time.Second)
	ws2, err := dialWS(*addr, "/websockify")
	if err != nil {
		emit("reconnect", map[string]any{"error": err.Error()})
	} else {
		r2 := &RFB{ws: ws2}
		_, err := r2.handshake(*good)
		if err != nil {
			emit("reconnect", map[string]any{"error": "auth: " + err.Error()})
		} else if err := r2.clientInitShared(); err != nil {
			emit("reconnect", map[string]any{"error": "init: " + err.Error()})
		} else {
			fb4, _, err := r2.capture(false)
			if err != nil {
				emit("reconnect", map[string]any{"error": "capture: " + err.Error()})
			} else {
				writePPM(*out+"/fb4.ppm", fb4, r.W, r.H, r.Rsh, r.Gsh, r.Bsh)
				// 再验证重连后仍可交互：移动鼠标并观察增量更新
				r2.pointer(0, 300, 300)
				r2.pointer(1, 300, 300)
				r2.pointer(0, 300, 300)
				time.Sleep(300 * time.Millisecond)
				r2.key(true, 0x7a)
				r2.key(false, 0x7a)
				time.Sleep(300 * time.Millisecond)
				// 重连后再开菜单，用增量请求验证服务端主动推送 FramebufferUpdate
				r2.pointer(0, 60, r2.H-60)
				r2.pointer(4, 60, r2.H-60)
				r2.pointer(0, 60, r2.H-60)
				time.Sleep(700 * time.Millisecond)
				fb5, rects5, err5 := r2.capture(true)
				emit("reconnect", map[string]any{
					"ok": true, "width": r2.W, "height": r2.H,
					"stats": stats(fb4),
					"diff_vs_fb3": diffPixels(fb3, fb4),
					"input_after_reconnect": "click(300,300)+key z",
					"incremental": map[string]any{
						"rects": len(rects5), "err": errOf(err5),
						"rect_list": rects5,
						"diff_vs_fb4": diffPixels(fb4, fb5),
					},
				})
				if fb5 != nil {
					writePPM(*out+"/fb5.ppm", fb5, r2.W, r2.H, r2.Rsh, r2.Gsh, r2.Bsh)
				}
			}
		}
		ws2.c.Close()
	}

	b, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(b))
}
