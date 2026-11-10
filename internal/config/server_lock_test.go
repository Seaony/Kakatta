//go:build linux || darwin

package config

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestServerLockRejectsSecondOpenAndCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	first, err := AcquireServerLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Close() })
	assertServerLockBlocked(t, dir)
	before, err := os.Stat(filepath.Join(dir, ".server.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireServerLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close() })
	if err := first.Close(); err != nil {
		t.Fatalf("重复关闭失败：%v", err)
	}
	// 旧锁重复关闭不能释放刚被复用的文件描述符。
	assertServerLockBlocked(t, dir)
	after, err := os.Stat(filepath.Join(dir, ".server.lock"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("关闭后锁文件被删除或替换：%v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := AcquireServerLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestServerLockPreservesFileAndTightensPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".server.lock")
	const content = "锁文件不能被截断或删除"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireServerLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatalf("锁文件类型或权限错误：%v %v", info, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != content {
		t.Fatalf("现有锁文件被修改：%q %v", got, err)
	}
}

func TestServerLockConcurrentClose(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireServerLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	const count = 8
	errs := make([]error, count)
	start := make(chan struct{})
	var group sync.WaitGroup
	for i := range count {
		group.Go(func() {
			<-start
			errs[i] = lock.Close()
		})
	}
	close(start)
	group.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("并发关闭失败：%v", err)
		}
	}
	next, err := AcquireServerLock(dir)
	if err != nil {
		t.Fatalf("并发关闭后未释放锁：%v", err)
	}
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestServerLockRejectsUnsafePaths(t *testing.T) {
	for _, kind := range []string{"symlink", "dangling", "directory", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".server.lock")
			target := filepath.Join(t.TempDir(), "target")
			if err := os.WriteFile(target, []byte("不能改动"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(target, 0644); err != nil {
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
			assertServerLockBlocked(t, dir)
			got, err := os.ReadFile(target)
			if err != nil || string(got) != "不能改动" {
				t.Fatalf("拒绝路径时改动了外部文件：%q %v", got, err)
			}
			info, err := os.Stat(target)
			if err != nil || info.Mode().Perm() != 0644 {
				t.Fatalf("拒绝路径时改动了外部权限：%v %v", info, err)
			}
			if _, err := os.Lstat(target + "-missing"); !os.IsNotExist(err) {
				t.Fatalf("跟随链接创建了外部文件：%v", err)
			}
		})
	}
	t.Run("data directory", func(t *testing.T) {
		outside := t.TempDir()
		linked := filepath.Join(t.TempDir(), "data")
		if err := os.Symlink(outside, linked); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{linked, linked + "/", linked + "/."} {
			assertServerLockBlocked(t, path)
		}
		if _, err := os.Lstat(filepath.Join(outside, ".server.lock")); !os.IsNotExist(err) {
			t.Fatalf("跟随数据目录链接创建了锁：%v", err)
		}
		missing := filepath.Join(t.TempDir(), "missing")
		assertServerLockBlocked(t, missing)
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Fatalf("擅自创建了数据目录：%v", err)
		}
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, nil, 0600); err != nil {
			t.Fatal(err)
		}
		assertServerLockBlocked(t, file)
		assertServerLockBlocked(t, "")
	})
}

func TestServerLockExcludesOtherProcess(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireServerLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	probeServerLock(t, dir, "blocked")
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	probeServerLock(t, dir, "acquired")
}

func TestServerLockChildCloseReleasesWhileProcessLives(t *testing.T) {
	dir := t.TempDir()
	cmd, input, output := startServerLockHolder(t, dir)
	assertServerLockBlocked(t, dir)
	if _, err := input.Write([]byte{'c'}); err != nil {
		t.Fatal(err)
	}
	if line, err := output.ReadString('\n'); err != nil || line != "released\n" {
		t.Fatalf("子进程未释放锁：%q %v", line, err)
	}
	lock, err := AcquireServerLock(dir)
	if err != nil {
		t.Fatalf("子进程关闭文件后仍持锁：%v", err)
	}
	t.Cleanup(func() { lock.Close() })
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("子进程正常退出失败：%v", err)
	}
}

func TestServerLockChildCrashReleases(t *testing.T) {
	dir := t.TempDir()
	cmd, _, _ := startServerLockHolder(t, dir)
	assertServerLockBlocked(t, dir)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("子进程未被终止")
	}
	lock, err := AcquireServerLock(dir)
	if err != nil {
		t.Fatalf("崩溃后未自动释放锁：%v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestServerLockProcessHelper(t *testing.T) {
	mode := os.Getenv("KAKATTA_SERVER_LOCK_TEST_MODE")
	if mode == "" {
		return
	}
	lock, err := AcquireServerLock(os.Getenv("KAKATTA_SERVER_LOCK_TEST_DIR"))
	if mode == "probe" {
		if err != nil {
			fmt.Println("blocked")
			return
		}
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}
		fmt.Println("acquired")
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	fmt.Println("locked")
	var command [1]byte
	if _, err := io.ReadFull(os.Stdin, command[:]); err != nil || command[0] != 'c' {
		t.Fatalf("子进程控制失败：%v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	fmt.Println("released")
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}

func assertServerLockBlocked(t *testing.T, dir string) {
	t.Helper()
	lock, err := AcquireServerLock(dir)
	if lock != nil {
		lock.Close()
		t.Fatal("失败时返回了可用锁")
	}
	if err == nil {
		t.Fatal("应当拒绝取得服务锁")
	}
}

func serverLockCommand(t *testing.T, mode, dir string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestServerLockProcessHelper$")
	cmd.Env = append(os.Environ(), "KAKATTA_SERVER_LOCK_TEST_MODE="+mode, "KAKATTA_SERVER_LOCK_TEST_DIR="+dir)
	return cmd
}

func probeServerLock(t *testing.T, dir, want string) {
	t.Helper()
	output, err := serverLockCommand(t, "probe", dir).CombinedOutput()
	if err != nil || !strings.HasPrefix(string(output), want+"\n") {
		t.Fatalf("子进程锁检查错误：想要%s，得到%q %v", want, output, err)
	}
}

func startServerLockHolder(t *testing.T, dir string) (*exec.Cmd, io.WriteCloser, *bufio.Reader) {
	t.Helper()
	cmd := serverLockCommand(t, "hold", dir)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		input.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		input.Close()
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	reader := bufio.NewReader(output)
	if line, err := reader.ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("子进程未取得锁：%q %v", line, err)
	}
	return cmd, input, reader
}
