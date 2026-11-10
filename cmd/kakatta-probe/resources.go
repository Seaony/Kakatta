package main

import (
	"io/fs"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
)

// memWatch 定时统计浏览器所有进程的内存，记下峰值。
type memWatch struct {
	peak atomic.Int64
	stop chan struct{}
	wg   sync.WaitGroup
}

func watchMemory(pid int, every time.Duration) *memWatch {
	m := &memWatch{stop: make(chan struct{})}
	if pid == 0 {
		return m
	}
	sample := func() {
		if n, err := browser.TreeMemory(pid); err == nil && n > m.peak.Load() {
			m.peak.Store(n)
		}
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		sample()
		for {
			select {
			case <-m.stop:
				return
			case <-t.C:
				sample()
			}
		}
	}()
	return m
}

// done 停止统计，返回峰值（MB）。
func (m *memWatch) done() int64 {
	close(m.stop)
	m.wg.Wait()
	return m.peak.Load() >> 20
}

// dirSizeMB 返回目录里所有文件加起来的大小（MB）。
func dirSizeMB(dir string) int64 {
	var total int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total >> 20
}
