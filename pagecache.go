package main

// 页缓存释放。
//
// Chromium 二进制 268MB，每次任务运行都会被读入页缓存并长期驻留，
// 导致容器的 memory.current 从 36MB 涨到 250MB 以上。
// 这些缓存本身无害（内存紧张时内核会回收），但会让监控数字难看。
//
// 任务结束后对几个大文件调用 posix_fadvise(POSIX_FADV_DONTNEED)，
// 主动把这部分页缓存还给系统。不需要 root 权限。

import (
	"os"
	"syscall"
)

// cacheTargets 是值得释放页缓存的大文件。
// 只放「任务结束后短期内不会再用」的：Chromium 与两个 Go 二进制。
var cacheTargets = []string{
	"/usr/lib/chromium/chromium",
	"/app/seltool",
	"/app/vncproxy",
}

// dropPageCache 释放 cacheTargets 的页缓存，返回实际尝试释放的字节数。
func dropPageCache() int64 {
	var total int64
	for _, path := range cacheTargets {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			continue
		}
		// POSIX_FADV_DONTNEED = 4
		_, _, errno := syscall.Syscall6(
			syscall.SYS_FADVISE64,
			f.Fd(),
			0,
			uintptr(st.Size()),
			4,
			0, 0,
		)
		f.Close()
		if errno == 0 {
			total += st.Size()
		}
	}
	return total
}

// dropPageCacheAfterTask 在任务结束后释放缓存并记录日志。
// 浏览器关闭后再调用，避免影响正在运行的任务。
func (a *Automation) dropPageCacheAfterTask() {
	if n := dropPageCache(); n > 0 {
		logf("已释放 %.0f MB 页缓存（Chromium 等大文件）", float64(n)/1048576)
	}
}
