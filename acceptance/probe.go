package main

// 调试探针：打印 /websockify 的 HTTP 响应头与首帧字节
import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

func main() {
	addr := os.Args[1]
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		fmt.Println("dial err:", err)
		return
	}
	key := make([]byte, 16)
	rand.Read(key)
	k := base64.StdEncoding.EncodeToString(key)
	fmt.Fprintf(c, "GET /websockify HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\n"+
		"Connection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", addr, k)
	br := bufio.NewReader(c)
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		l, err := br.ReadString('\n')
		fmt.Printf("HDR %q err=%v\n", l, err)
		if err != nil || l == "\r\n" {
			break
		}
	}
	buf := make([]byte, 64)
	n, err := br.Read(buf)
	fmt.Printf("FRAME %s err=%v\n", hex.EncodeToString(buf[:n]), err)
	if n > 2 {
		fmt.Printf("PAYLOAD %q\n", string(buf[2:n]))
	}
	_ = io.EOF
}
