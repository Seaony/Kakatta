package browser

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func freeDisplayAccount(t *testing.T) int {
	t.Helper()
	for id := 7000 + os.Getpid()%4000; id < 12767; id++ {
		_, socketErr := os.Lstat(fmt.Sprintf("/tmp/.X11-unix/X%d", 100+id))
		_, lockErr := os.Lstat(fmt.Sprintf("/tmp/.X%d-lock", 100+id))
		if os.IsNotExist(socketErr) && os.IsNotExist(lockErr) {
			return id
		}
	}
	t.Fatal("找不到可用于测试的显示号")
	return 0
}

func fakeXvfb(t *testing.T, mode string) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Xvfb"), []byte("#!/bin/sh\nexec \"$KAKATTA_DISPLAY_TEST_BINARY\" -test.run=TestDisplayHelperProcess -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "pid")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KAKATTA_DISPLAY_TEST_BINARY", binary)
	t.Setenv("KAKATTA_DISPLAY_TEST_MODE", mode)
	t.Setenv("KAKATTA_DISPLAY_TEST_PID_FILE", pidFile)
	return pidFile
}

func TestDisplayHelperProcess(t *testing.T) {
	mode := os.Getenv("KAKATTA_DISPLAY_TEST_MODE")
	if mode == "" {
		return
	}
	at := slices.Index(os.Args, "--")
	if at < 0 || len(os.Args) != at+8 {
		os.Exit(2)
	}
	args := os.Args[at+1:]
	if !strings.HasPrefix(args[0], ":") || strings.Join(args[1:], " ") != "-screen 0 1920x1080x24 -nolisten tcp -noreset" {
		os.Exit(3)
	}
	pid := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(os.Getenv("KAKATTA_DISPLAY_TEST_PID_FILE"), []byte(pid), 0600); err != nil {
		os.Exit(4)
	}
	if mode == "exit" {
		os.Exit(7)
	}
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, os.Interrupt)
	if mode == "unready" {
		<-quit
		return
	}
	number := strings.TrimPrefix(args[0], ":")
	lock := "/tmp/.X" + number + "-lock"
	if err := os.WriteFile(lock, []byte(pid+"\n"), 0600); err != nil {
		os.Exit(5)
	}
	defer os.Remove(lock)
	if mode == "lock_only" {
		<-quit
		return
	}
	if err := os.MkdirAll("/tmp/.X11-unix", 01777); err != nil {
		os.Exit(6)
	}
	listener, err := net.Listen("unix", "/tmp/.X11-unix/X"+number)
	if err != nil {
		os.Exit(8)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	if mode == "child" {
		child := exec.Command("sleep", "60")
		if err := child.Start(); err != nil {
			os.Exit(9)
		}
		os.WriteFile(os.Getenv("KAKATTA_DISPLAY_TEST_PID_FILE")+".child", []byte(strconv.Itoa(child.Process.Pid)), 0600)
	}
	if mode == "stubborn" {
		for range quit {
		}
	}
	<-quit
}

func displayProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("虚拟屏幕进程 %d 没有被回收", pid)
}

func TestDisplayOwnsOneAccountAndKeepsOtherScreenAlive(t *testing.T) {
	fakeXvfb(t, "ready")
	firstID := freeDisplayAccount(t)
	first, err := StartDisplay(context.Background(), firstID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Close() })
	if first.Name() != fmt.Sprintf(":%d", 100+firstID) || first.PID() <= 0 {
		t.Fatalf("显示号或进程不正确：%s %d", first.Name(), first.PID())
	}
	if duplicate, err := StartDisplay(context.Background(), firstID); duplicate != nil || err == nil {
		t.Fatal("同一账号的显示不能重复启动")
	}
	second, err := StartDisplay(context.Background(), freeDisplayAccount(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close() })
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal("重复关闭应该安全：", err)
	}
	displayProcessGone(t, first.PID())
	conn, err := net.DialTimeout("unix", "/tmp/.X11-unix/X"+strings.TrimPrefix(second.Name(), ":"), time.Second)
	if err != nil {
		t.Fatal("关闭一个账号影响了另一个账号：", err)
	}
	conn.Close()
}

func TestDisplayStartupNeedsListeningSocket(t *testing.T) {
	pidFile := fakeXvfb(t, "lock_only")
	start := time.Now()
	if display, err := startDisplay(context.Background(), freeDisplayAccount(t), 100*time.Millisecond); display != nil || err == nil {
		t.Fatal("只有锁文件、没有监听套接字时不能算就绪")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("虚拟屏幕启动没有按时结束")
	}
	pid, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	processID, _ := strconv.Atoi(string(pid))
	displayProcessGone(t, processID)
}

func TestDisplayParentCancellationCleansStartedAndStartingProcesses(t *testing.T) {
	for _, mode := range []string{"ready", "unready"} {
		t.Run(mode, func(t *testing.T) {
			pidFile := fakeXvfb(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			d, err := StartDisplay(ctx, freeDisplayAccount(t))
			if mode == "ready" {
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-d.done:
				case <-time.After(3 * time.Second):
					t.Fatal("父上下文结束后屏幕仍在运行")
				}
			} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("启动未就绪时取消应返回上下文错误：%v", err)
			}
			pid, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			processID, _ := strconv.Atoi(string(pid))
			displayProcessGone(t, processID)
		})
	}
}

func TestDisplayExitBeforeReadyFailsAndInvalidAccountDoesNotStart(t *testing.T) {
	fakeXvfb(t, "exit")
	if display, err := StartDisplay(context.Background(), freeDisplayAccount(t)); display != nil || err == nil {
		t.Fatal("就绪前退出不能返回成功")
	}
	for _, id := range []int{0, -1, 12768} {
		if display, err := StartDisplay(context.Background(), id); display != nil || err == nil {
			t.Fatalf("无效账号编号不能启动：%d", id)
		}
	}
}

func TestDisplayStartupFailureKeepsProcessOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Xvfb"), []byte("#!/bin/sh\nprintf 'local-xvfb-startup-error\\n' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var output bytes.Buffer
	if display, err := StartDisplayWithLog(t.Context(), freeDisplayAccount(t), &output); display != nil || err == nil {
		t.Fatal("故障显示被当成启动成功")
	}
	if !bytes.Contains(output.Bytes(), []byte("local-xvfb-startup-error")) {
		t.Fatal("启动失败前的标准错误输出丢失", output.String())
	}
}

func TestDisplayClosesRemainingChildProcesses(t *testing.T) {
	pidFile := fakeXvfb(t, "child")
	d, err := StartDisplay(context.Background(), freeDisplayAccount(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	var childData []byte
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		childData, err = os.ReadFile(pidFile + ".child")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	childID, _ := strconv.Atoi(string(childData))
	displayProcessGone(t, childID)
}

func TestDisplayCloseKillsUnresponsiveProcess(t *testing.T) {
	fakeXvfb(t, "stubborn")
	accountID := freeDisplayAccount(t)
	d, err := StartDisplay(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		d.Close()
		os.Remove(fmt.Sprintf("/tmp/.X11-unix/X%d", 100+accountID))
		os.Remove(fmt.Sprintf("/tmp/.X%d-lock", 100+accountID))
	})
	start := time.Now()
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < time.Second || elapsed > 3*time.Second {
		t.Fatalf("不响应的屏幕没有在正常结束的等待期后被强制结束：%s", elapsed)
	}
	displayProcessGone(t, d.PID())
}

func TestRealDisplayDimensionsAndSocketCleanup(t *testing.T) {
	requireBrowser(t)
	if _, err := exec.LookPath("Xvfb"); err != nil {
		t.Fatal("显式开启容器测试后必须安装 Xvfb：", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	d, err := StartDisplay(ctx, freeDisplayAccount(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	socket := "/tmp/.X11-unix/X" + strings.TrimPrefix(d.Name(), ":")
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte{'l', 0, 11, 0, 0, 0, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	prefix := make([]byte, 8)
	if _, err := io.ReadFull(conn, prefix); err != nil || prefix[0] != 1 {
		t.Fatalf("虚拟屏幕未接受本机 X11 连接：%v %v", prefix, err)
	}
	setup := make([]byte, int(binary.LittleEndian.Uint16(prefix[6:8]))*4)
	if _, err := io.ReadFull(conn, setup); err != nil || len(setup) < 32 || setup[20] != 1 {
		t.Fatalf("X11 屏幕配置无效：%v", err)
	}
	vendorLength := int(binary.LittleEndian.Uint16(setup[16:18]))
	screen := 32 + (vendorLength+3)/4*4 + int(setup[21])*8
	if len(setup) < screen+40 {
		t.Fatal("X11 没有返回完整屏幕参数")
	}
	width := binary.LittleEndian.Uint16(setup[screen+20 : screen+22])
	height := binary.LittleEndian.Uint16(setup[screen+22 : screen+24])
	if width != 1920 || height != 1080 || setup[screen+38] != 24 {
		t.Fatalf("屏幕应为 1920x1080x24，实际 %dx%dx%d", width, height, setup[screen+38])
	}
	number, _ := strconv.Atoi(strings.TrimPrefix(d.Name(), ":"))
	if tcp, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(6000+number), 100*time.Millisecond); err == nil {
		tcp.Close()
		t.Fatal("虚拟屏幕不应监听 TCP")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	displayProcessGone(t, d.PID())
	if c, err := net.DialTimeout("unix", socket, 100*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("关闭后仍可连接屏幕")
	}
}
