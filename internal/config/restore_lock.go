//go:build linux || darwin

package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const RestoreStateName = "restore-state.json"

// AcquireRestoreLock 让维护命令共享访问，恢复切换独占访问。
func AcquireRestoreLock(dataDir string, exclusive bool) (io.Closer, error) {
	if dataDir == "" {
		return nil, errors.New("恢复锁数据目录不能为空")
	}
	dir, err := unix.Open(filepath.Clean(dataDir), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("无法打开恢复锁目录：%w", err)
	}
	defer unix.Close(dir)
	fd, err := unix.Openat(dir, ".restore.lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, fmt.Errorf("无法打开恢复锁文件：%w", err)
	}
	file := os.NewFile(uintptr(fd), ".restore.lock")
	kept := false
	defer func() {
		if !kept {
			file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("恢复锁必须是普通文件")
	}
	mode := unix.LOCK_SH
	if exclusive {
		mode = unix.LOCK_EX
	}
	if err := unix.Flock(fd, mode|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, errors.New("数据目录正在维护或恢复，请稍后重试")
		}
		return nil, fmt.Errorf("无法锁定恢复数据目录：%w", err)
	}
	if err := file.Chmod(0600); err != nil {
		return nil, fmt.Errorf("无法设置恢复锁文件权限：%w", err)
	}
	kept = true
	return &serverLock{file: file}, nil
}
