//go:build !windows

package main

import (
	"fmt"
	"os"
	"sync"
	"syscall"
)

func lockJournal(path string) (func() error, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("该账号的实验工具已经在运行")
	}
	var once sync.Once
	var closeErr error
	return func() error {
		once.Do(func() {
			closeErr = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			if err := f.Close(); closeErr == nil {
				closeErr = err
			}
		})
		return closeErr
	}, nil
}
