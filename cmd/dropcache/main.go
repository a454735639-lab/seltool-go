// dropcache 对指定文件调用 posix_fadvise(POSIX_FADV_DONTNEED)，
// 让内核丢弃该文件的页缓存。不需要 root，只要文件可读。
//
// 用途：Chromium 二进制 268MB，每次任务运行都会被读入页缓存并长期驻留。
// 任务结束后调用本工具，可把这部分缓存还给系统，降低容器的 memory.current。
package main

import (
	"fmt"
	"os"
	"syscall"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("用法: dropcache <文件路径> [更多文件...]")
		os.Exit(2)
	}
	var total int64
	for _, path := range os.Args[1:] {
		f, err := os.Open(path)
		if err != nil {
			fmt.Printf("  跳过 %s: %v\n", path, err)
			continue
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			continue
		}
		// POSIX_FADV_DONTNEED：该范围若未被修改，内核可丢弃其页缓存
		_, _, errno := syscall.Syscall6(
			syscall.SYS_FADVISE64,
			f.Fd(),
			0,
			uintptr(st.Size()),
			uintptr(4), // POSIX_FADV_DONTNEED
			0, 0,
		)
		f.Close()
		if errno != 0 {
			fmt.Printf("  %s: fadvise 失败 %v\n", path, errno)
			continue
		}
		fmt.Printf("  已释放 %s（%.1f MB）\n", path, float64(st.Size())/1048576)
		total += st.Size()
	}
	fmt.Printf("合计尝试释放 %.1f MB 页缓存\n", float64(total)/1048576)
}
