package remote

import (
	"context"
	"io"
	"net"
	"os"
	"runtime"
	"slices"
	"testing"
	"time"
)

func TestViewportRejectsOutsideScreen(t *testing.T) {
	s := testSession(t, "")
	for _, view := range []Viewport{{X: -1, Width: 1, Height: 1}, {Y: -1, Width: 1, Height: 1}, {Width: 0, Height: 1}, {Width: 1, Height: 0}, {X: 1900, Width: 21, Height: 1}, {Y: 1000, Width: 1, Height: 81}, {X: int(^uint(0) >> 1), Width: 2, Height: 1}} {
		if s.SetViewport(&view) == nil {
			t.Fatalf("越界坐标未被拒绝：%+v", view)
		}
	}
	if err := s.SetViewport(nil); err != nil {
		t.Fatal(err)
	}
}

func TestX11VNCArgumentsOnlyListenOnLoopback(t *testing.T) {
	args := x11vncArgs(":92", "12345")
	for _, flag := range []string{"-noipv6", "-noremote", "-nolookup", "-forever", "-shared"} {
		if !slices.Contains(args, flag) {
			t.Fatalf("启动参数缺少 %s", flag)
		}
	}
	for flag, value := range map[string]string{"-display": ":92", "-listen": "127.0.0.1", "-rfbport": "12345"} {
		i := slices.Index(args, flag)
		if i < 0 || args[i+1] != value {
			t.Fatalf("%s 参数不正确", flag)
		}
	}
}

func TestX11VNCInExplicitContainer(t *testing.T) {
	if os.Getenv("KAKATTA_REMOTE_X11_TEST") != "1" {
		t.Skip("只在明确指定的容器里运行真实 x11vnc")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("真实画面测试必须在 Linux 容器里运行")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := Start(ctx, os.Getenv("DISPLAY"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	conn, err := net.DialTimeout("tcp4", s.Address(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(time.Second))
	greeting := make([]byte, 12)
	if _, err := io.ReadFull(conn, greeting); err != nil || string(greeting) != "RFB 003.008\n" {
		t.Fatalf("没有收到 RFB 握手：%q %v", greeting, err)
	}
	cancel()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := net.DialTimeout("tcp4", s.Address(), 100*time.Millisecond); err == nil {
		t.Fatal("结束会话后 x11vnc 仍在监听")
	}
}
