//go:build linux || darwin

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func denyRestoreLock(t *testing.T, dir string, exclusive bool) {
	t.Helper()
	lock, err := AcquireRestoreLock(dir, exclusive)
	if lock != nil {
		lock.Close()
		t.Fatal("失败时返回了锁")
	}
	if err == nil {
		t.Fatal("应当拒绝取得恢复锁")
	}
}

func TestRestoreSharedLocksAndExclusiveLock(t *testing.T) {
	dir := t.TempDir()
	first, err := AcquireRestoreLock(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Close() })
	second, err := AcquireRestoreLock(dir, false)
	if err != nil {
		t.Fatal("共享锁不能共存", err)
	}
	t.Cleanup(func() { second.Close() })
	if lock, err := AcquireRestoreLock(dir, true); lock != nil || err == nil || !strings.Contains(err.Error(), "正在维护或恢复") {
		if lock != nil {
			lock.Close()
		}
		t.Fatal("独占锁没有明确报告忙", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	denyRestoreLock(t, dir, true)
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	exclusive, err := AcquireRestoreLock(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exclusive.Close() })
	denyRestoreLock(t, dir, false)
	denyRestoreLock(t, dir, true)
	if err := exclusive.Close(); err != nil {
		t.Fatal(err)
	}
	shared, err := AcquireRestoreLock(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	shared.Close()
}

func TestRestoreLockPreservesInodeContentAndIdempotentClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".restore.lock")
	const content = "原地锁不能删除或截断"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := AcquireRestoreLock(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Close() })
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireRestoreLock(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close() })
	if err := first.Close(); err != nil {
		t.Fatal("重复关闭失败", err)
	}
	denyRestoreLock(t, dir, false)
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || after.Mode().Perm() != 0600 {
		t.Fatal("恢复锁inode或权限错误", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != content {
		t.Fatal("恢复锁内容被截断或修改", err)
	}
}

func TestRestoreLockRejectsUnsafeFilesAndDirectories(t *testing.T) {
	for _, kind := range []string{"symlink", "dangling", "directory", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".restore.lock")
			target := filepath.Join(t.TempDir(), "target")
			if err := os.WriteFile(target, []byte("不改外部文件"), 0644); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(target, path)
			case "dangling":
				err = os.Symlink(target+"-missing", path)
			case "directory":
				err = os.Mkdir(path, 0700)
			case "fifo":
				err = unix.Mkfifo(path, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			denyRestoreLock(t, dir, false)
			denyRestoreLock(t, dir, true)
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "不改外部文件" {
				t.Fatal("拒绝锁时改动了目标文件", err)
			}
			info, err := os.Stat(target)
			if err != nil || info.Mode().Perm() != 0644 {
				t.Fatal("拒绝锁时改动了目标权限", err)
			}
			if _, err := os.Lstat(target + "-missing"); !os.IsNotExist(err) {
				t.Fatal("跟随无效链接创建了目标文件", err)
			}
		})
	}
	outside := t.TempDir()
	linked := filepath.Join(t.TempDir(), "data")
	if err := os.Symlink(outside, linked); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{linked, linked + "/", linked + "/.", filepath.Join(t.TempDir(), "missing"), ""} {
		denyRestoreLock(t, path, false)
	}
	if _, err := os.Lstat(filepath.Join(outside, ".restore.lock")); !os.IsNotExist(err) {
		t.Fatal("跟随目录链接创建了恢复锁", err)
	}
}
