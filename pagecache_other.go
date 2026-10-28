//go:build !linux

package main

// dropPageCache 在非 Linux 平台是空操作。
// posix_fadvise(POSIX_FADV_DONTNEED) 只有 Linux 提供，而页面缓存回收本身
// 也只对「容器里 Chromium 常驻内存」这个场景有意义。
func dropPageCache() int64 { return 0 }
