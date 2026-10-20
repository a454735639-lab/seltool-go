package main

// 极简 HTTP 探针：在容器内验证能否访问指定地址。
// 编译成静态二进制后 docker cp 进容器执行，避免依赖容器内的 curl/wget。

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("用法: httpprobe <url>")
		os.Exit(2)
	}
	url := os.Args[1]

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			// 关键：容器里注入了 HTTP_PROXY，这里显式禁用，测的是直连
			Proxy:           nil,
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		fmt.Println("构造请求失败:", err)
		os.Exit(1)
	}
	req.Header.Set("User-Agent", "seltool-probe")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("请求失败:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	fmt.Printf("HTTP %d  Content-Type: %s\n", resp.StatusCode, resp.Header.Get("Content-Type"))
	fmt.Printf("响应前 300 字节: %s\n", string(body))
}
