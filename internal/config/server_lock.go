//go:build linux || darwin

package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// AcquireServerLock 防止两个服务同时恢复和使用同一份数据。
func AcquireServerLock(dataDir string) (io.Closer, error) {
	if dataDir == "" {
		return nil, errors.New("服务数据目录不能为空")
	}
	dir, err := unix.Open(filepath.Clean(dataDir), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("无法打开服务锁目录：%w", err)
	}
	defer unix.Close(dir)

	// 固定目录后再开文件，不跟随链接；非阻塞也避免特殊文件让启动卡住。
	fd, err := unix.Openat(dir, ".server.lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, fmt.Errorf("无法打开服务锁文件：%w", err)
	}
	file := os.NewFile(uintptr(fd), ".server.lock")
	kept := false
	defer func() {
		if !kept {
			file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("服务锁必须是普通文件")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, errors.New("数据目录已被另一个服务实例占用")
		}
		return nil, fmt.Errorf("无法锁定服务数据目录：%w", err)
	}
	if err := file.Chmod(0600); err != nil {
		return nil, fmt.Errorf("无法设置服务锁文件权限：%w", err)
	}
	kept = true
	return &serverLock{file: file}, nil
}

type serverLock struct {
	file *os.File
	once sync.Once
	err  error
}

func (lock *serverLock) Close() error {
	// 不删除锁文件，否则另一个实例能锁住新文件，绕过还活着的旧锁。
	lock.once.Do(func() { lock.err = lock.file.Close() })
	return lock.err
}
