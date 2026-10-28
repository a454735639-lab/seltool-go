//go:build unix

package main

import (
	"os"
	"syscall"
)

// openProfileLock 以读写方式打开（必要时创建）profile 锁文件。
func openProfileLock(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
}

// lockFileExclusive 取非阻塞独占锁；已有实例持锁时立刻返回错误。
func lockFileExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
