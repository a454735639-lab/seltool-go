package main

import (
	"os"
	"strconv"
	"strings"
)

// isChromeProcess 判断 pid 是否真的是 Chromium。
// 容器重启后 PID 会从 1 重新分配，仅用 kill(pid,0) 探活会把无关进程误认成残留 Chrome。
func isChromeProcess(pid int) bool {
	if pid <= 0 {
		return false
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return false
	}
	cmd := strings.ToLower(strings.ReplaceAll(string(raw), "\x00", " "))
	return strings.Contains(cmd, "chromium") || strings.Contains(cmd, "chrome")
}
