//go:build windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// openProfileLock 在 Windows 上用「不共享」方式打开锁文件。
//
// Windows 没有 flock，但 CreateFile 的 share mode 提供了等价语义：以
// mode=0 打开后，第二个进程（或同一进程的第二次打开）会拿到
// ERROR_SHARING_VIOLATION。因此这里「打开成功」就已经等于「拿到锁」。
func openProfileLock(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(
		p,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		0, // 不共享：等价于独占锁
		nil,
		syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("锁文件被占用: %w", err)
	}
	return os.NewFile(uintptr(h), path), nil
}

// lockFileExclusive 在 Windows 上无需额外动作：独占性已由 share mode=0 保证。
func lockFileExclusive(*os.File) error { return nil }

// unlockFile 由 Close 句柄释放，这里保持与 unix 实现相同的调用形态。
func unlockFile(*os.File) error { return nil }
