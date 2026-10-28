//go:build !linux

package main

import (
	"fmt"
	"os"
)

// 非 Linux 平台没有 posix_fadvise；保留同名的空实现，让 go build ./...
// 在 macOS/Windows 上也能通过（这个工具只在容器里用）。
func main() {
	fmt.Println("dropcache 仅支持 Linux（依赖 posix_fadvise）")
	os.Exit(1)
}
