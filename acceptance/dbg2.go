package main

// 探针：只测 dialWS + 首帧
import (
	"fmt"
	"os"
	"time"
)

func main() {
	fmt.Println("start")
	w, err := dialWS("127.0.0.1:7900", "/websockify")
	fmt.Println("dialWS err:", err)
	if err != nil {
		os.Exit(1)
	}
	w.c.SetReadDeadline(time.Now().Add(3 * time.Second))
	m, err := w.ReadMessage()
	fmt.Printf("msg %q err=%v\n", m, err)
}
