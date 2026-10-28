package main

// 任务结束后释放页缓存并记录日志。
//
// 具体实现分平台：Linux 走 posix_fadvise（pagecache.go），其他平台是空操作
// （pagecache_other.go）—— 这样 go build ./... 在 macOS/Windows 上也能过。

// dropPageCacheAfterTask 在任务结束后释放缓存并记录日志。
// 浏览器关闭后再调用，避免影响正在运行的任务。
func (a *Automation) dropPageCacheAfterTask() {
	if n := dropPageCache(); n > 0 {
		logf("已释放 %.0f MB 页缓存（Chromium 等大文件）", float64(n)/1048576)
	}
}
