//go:build !windows

package browser

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCancelDuringChromeStartupReturnsPromptly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slow-chrome")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if s, err := Start(ctx, Options{ExecPath: path, ProfileDir: t.TempDir()}); err == nil {
		s.Close()
		t.Fatal("卡住的浏览器不应该启动成功")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("父上下文取消后仍在等浏览器启动")
	}
}
