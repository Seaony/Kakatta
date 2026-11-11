// Package remote 在需要用户操作时提供已认证的浏览器画面。
package remote

import (
	"context"
	"errors"
	"io"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

var displayPattern = regexp.MustCompile(`^:[0-9]+(?:\.[0-9]+)?$`)

// Viewport 是验证码在虚拟屏幕上的范围，坐标包含浏览器工具栏。
type Viewport struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type Session struct {
	ctx       context.Context
	parent    context.Context
	cancel    context.CancelFunc
	address   string
	cmd       *exec.Cmd
	done      chan struct{}
	preview   *readOnlyPreview
	closeOnce sync.Once
	closeErr  error
	mu        sync.Mutex
	closed    bool
	normalEnd bool
	viewport  *Viewport
	conns     map[net.Conn]struct{}
	readers   int
	active    sync.WaitGroup
}

// Start 只接已有的虚拟屏幕，端口始终只在本机开放。
func Start(ctx context.Context, display string) (*Session, error) {
	return StartWithLog(ctx, display, nil)
}

func StartWithLog(ctx context.Context, display string, output io.Writer) (*Session, error) {
	if runtime.GOOS != "linux" {
		return nil, errors.New("远程画面需要 Linux 容器")
	}
	if !displayPattern.MatchString(display) {
		return nil, errors.New("虚拟屏幕编号不正确")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := exec.LookPath("x11vnc")
	if err != nil {
		return nil, &vncStartError{"reason_missing", err}
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, &vncStartError{"reason_port", err}
	}
	address := l.Addr().String()
	_, port, _ := net.SplitHostPort(address)
	l.Close()
	cmd := exec.Command(path, x11vncArgs(display, port)...)
	cmd.Stderr = output
	configureProcess(cmd)
	sessionCtx, cancel := context.WithCancel(context.Background())
	s := &Session{ctx: sessionCtx, parent: ctx, cancel: cancel, address: address, cmd: cmd, done: make(chan struct{}), conns: map[net.Conn]struct{}{}}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, &vncStartError{"reason_start", err}
	}
	go func() {
		cmd.Wait()
		close(s.done)
		s.close(false)
	}()
	go func() {
		select {
		case <-ctx.Done():
			s.close(false)
		case <-s.done:
		}
	}()
	if err := s.waitReady(ctx); err != nil {
		if closeErr := s.Close(); closeErr != nil {
			return nil, closeErr
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func x11vncArgs(display, port string) []string {
	return []string{"-display", display, "-listen", "127.0.0.1", "-rfbport", port,
		"-noipv6", "-forever", "-shared", "-nopw", "-nolookup", "-noremote", "-quiet"}
}

func (s *Session) waitReady(ctx context.Context) error {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		conn, err := net.DialTimeout("tcp4", s.address, 100*time.Millisecond)
		if err == nil {
			conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			greeting := make([]byte, 12)
			_, err = io.ReadFull(conn, greeting)
			conn.Close()
			if err == nil && strings.HasPrefix(string(greeting), "RFB ") {
				return nil
			}
		}
		select {
		case <-s.done:
			return &vncStartError{reason: "reason_exit"}
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return &vncStartError{reason: "reason_timeout"}
		case <-tick.C:
		}
	}
}

func (s *Session) Address() string { return s.address }

func (s *Session) HasViewer() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns) > 0 || s.readers > 0
}

func (s *Session) SetViewport(view *Viewport) error {
	if view != nil && (view.X < 0 || view.Y < 0 || view.Width < 1 || view.Height < 1 || view.Width > 1920 || view.Height > 1080 || view.X > 1920-view.Width || view.Y > 1080-view.Height) {
		return errors.New("验证码位置超出虚拟屏幕")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if view == nil {
		s.viewport = nil
	} else {
		copy := *view
		s.viewport = &copy
	}
	return nil
}

func (s *Session) Close() error {
	return s.close(true)
}

func (s *Session) close(normal bool) error {
	s.closeOnce.Do(func() {
		if s.parent != nil && s.parent.Err() != nil {
			normal = false
		}
		select {
		case <-s.done:
			normal = false
		default:
		}
		s.mu.Lock()
		s.closed = true
		s.normalEnd = normal
		s.mu.Unlock()
		s.cancel()
		s.mu.Lock()
		for conn := range s.conns {
			conn.Close()
		}
		s.mu.Unlock()
		if s.cmd != nil && s.cmd.Process != nil {
			stopProcess(s.cmd.Process.Pid)
			select {
			case <-s.done:
			case <-time.After(2 * time.Second):
				s.closeErr = errors.New("远程画面进程未能退出")
			}
		}
		s.active.Wait()
	})
	return s.closeErr
}

func (s *Session) register(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[conn] = struct{}{}
	s.active.Add(1)
	return true
}

func (s *Session) unregister(conn net.Conn) {
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
	s.active.Done()
}
