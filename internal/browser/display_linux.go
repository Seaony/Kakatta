package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Display 只拥有这次启动的虚拟屏幕，浏览器退出后由调用方关闭。
type Display struct {
	name      string
	cmd       *exec.Cmd
	done      chan struct{}
	waitErr   error
	closeOnce sync.Once
	stopWatch func() bool
	watchMu   sync.Mutex
}

// StartDisplay 给每个账号启动独立的虚拟屏幕，不复用其他进程的显示。
func StartDisplay(ctx context.Context, accountID int) (*Display, error) {
	return startDisplay(ctx, accountID, 10*time.Second)
}

func StartDisplayWithLog(ctx context.Context, accountID int, output io.Writer) (*Display, error) {
	return startDisplayWithLog(ctx, accountID, 10*time.Second, output)
}

func startDisplay(ctx context.Context, accountID int, timeout time.Duration) (*Display, error) {
	return startDisplayWithLog(ctx, accountID, timeout, os.Stderr)
}

func startDisplayWithLog(ctx context.Context, accountID int, timeout time.Duration, output io.Writer) (*Display, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if accountID < 1 || accountID > 12767 {
		return nil, fmt.Errorf("账号编号超出虚拟屏幕范围")
	}
	number := 100 + accountID
	socket := fmt.Sprintf("/tmp/.X11-unix/X%d", number)
	lock := fmt.Sprintf("/tmp/.X%d-lock", number)
	if conn, err := net.DialTimeout("unix", socket, 100*time.Millisecond); err == nil {
		conn.Close()
		return nil, fmt.Errorf("账号的虚拟屏幕 :%d 已被占用", number)
	}
	if data, err := os.ReadFile(lock); err == nil {
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil && pid > 0 && syscall.Kill(pid, 0) != syscall.ESRCH {
			return nil, fmt.Errorf("账号的虚拟屏幕 :%d 仍有进程运行", number)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("检查虚拟屏幕的锁失败：%w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(context.WithoutCancel(ctx), "Xvfb", ":"+strconv.Itoa(number), "-screen", "0", "1920x1080x24", "-nolisten", "tcp", "-noreset")
	configureProcessGroup(cmd)
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动虚拟屏幕失败：%w", err)
	}
	d := &Display{name: ":" + strconv.Itoa(number), cmd: cmd, done: make(chan struct{})}
	go func() {
		d.waitErr = cmd.Wait()
		terminateProcessGroup(cmd.Process.Pid)
		close(d.done)
	}()
	d.watchMu.Lock()
	d.stopWatch = context.AfterFunc(ctx, func() { d.Close() })
	d.watchMu.Unlock()
	startup := time.NewTimer(timeout)
	defer startup.Stop()
	poll := time.NewTicker(25 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-d.done:
			d.Close()
			if d.waitErr == nil {
				return nil, fmt.Errorf("虚拟屏幕在就绪前退出")
			}
			return nil, fmt.Errorf("虚拟屏幕在就绪前退出：%w", d.waitErr)
		case <-ctx.Done():
			d.Close()
			return nil, ctx.Err()
		case <-startup.C:
			d.Close()
			return nil, fmt.Errorf("等待虚拟屏幕就绪超时")
		case <-poll.C:
			data, err := os.ReadFile(lock)
			if err != nil || strings.TrimSpace(string(data)) != strconv.Itoa(cmd.Process.Pid) {
				continue
			}
			conn, err := net.DialTimeout("unix", socket, 100*time.Millisecond)
			if err != nil {
				continue
			}
			conn.Close()
			if err := ctx.Err(); err != nil {
				d.Close()
				return nil, err
			}
			select {
			case <-d.done:
				continue
			default:
				return d, nil
			}
		}
	}
}

func (d *Display) Name() string { return d.name }

func (d *Display) PID() int { return d.cmd.Process.Pid }

// Close 先正常结束屏幕，再结束同一进程组里的残留进程，并等到子进程回收。
func (d *Display) Close() error {
	d.closeOnce.Do(func() {
		d.watchMu.Lock()
		if d.stopWatch != nil {
			d.stopWatch()
		}
		d.watchMu.Unlock()
		select {
		case <-d.done:
			return
		default:
		}
		d.cmd.Process.Signal(syscall.SIGTERM)
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-d.done:
		case <-timer.C:
			syscall.Kill(-d.PID(), syscall.SIGKILL)
			<-d.done
		}
	})
	if d.waitErr != nil {
		var exit *exec.ExitError
		if !errors.As(d.waitErr, &exit) {
			return d.waitErr
		}
	}
	return nil
}
