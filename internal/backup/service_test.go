package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestBackupNewRejectsIncompleteDependencies(t *testing.T) {
	s, _, _ := exportFixture(t)
	for name, change := range map[string]func(*Dependencies){
		"data":      func(d *Dependencies) { d.DataDir = "" },
		"app":       func(d *Dependencies) { d.AppVersion = "" },
		"schema":    func(d *Dependencies) { d.SchemaVersion = 0 },
		"runtime":   func(d *Dependencies) { d.Runtime = nil },
		"typed_nil": func(d *Dependencies) { d.Runtime = (*exportRuntime)(nil) },
	} {
		t.Run(name, func(t *testing.T) {
			deps := s.deps
			change(&deps)
			if got, err := New(deps); !errors.Is(err, ErrInvalidDependencies) || got != nil {
				t.Fatal("缺少必需依赖仍创建备份服务")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	deps := s.deps
	deps.DataDir = path
	if _, err := New(deps); !errors.Is(err, ErrInvalidDependencies) {
		t.Fatal("普通文件被当作数据目录")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(s.deps.DataDir, link); err != nil {
		t.Fatal(err)
	}
	deps.DataDir = link
	if _, err := New(deps); !errors.Is(err, ErrInvalidDependencies) {
		t.Fatal("符号链接被当作数据目录")
	}
}

func awaitExportSignal(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("备份生命周期没有按时推进")
	}
}

func TestBackupExportSerialWaitCancellationAndCloseJoin(t *testing.T) {
	s, runtime, _ := exportFixture(t)
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	runtime.before = func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		return ctx.Err()
	}
	first := make(chan error, 1)
	go func() { first <- s.Export(t.Context(), testPassword, io.Discard) }()
	awaitExportSignal(t, entered)
	waitCtx, stopWait := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer stopWait()
	if err := s.Export(waitCtx, testPassword, io.Discard); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("排队操作未响应取消", err)
	}
	queued := make(chan error, 1)
	go func() { queued <- s.Export(t.Context(), testPassword, io.Discard) }()
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	awaitExportSignal(t, cancelled)
	select {
	case <-closed:
		t.Fatal("Close未等运行代回调真正退出")
	default:
	}
	if err := s.Export(t.Context(), testPassword, io.Discard); !errors.Is(err, ErrClosed) {
		t.Fatal("关闭后仍接纳导出", err)
	}
	unblock()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal("运行中的导出未被取消", err)
	}
	if err := <-queued; !errors.Is(err, context.Canceled) && !errors.Is(err, ErrClosed) {
		t.Fatal("等待导出未被关闭", err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if runtime.calls.Load() != 1 || runtime.closeCalls.Load() != 0 {
		t.Fatal("串行名额或全局Runtime所有权错误")
	}
	assertNoExportFiles(t, s.deps.DataDir)
}

func TestBackupExportCleanupFailureIsReportedByExportAndClose(t *testing.T) {
	s, _, _ := exportFixture(t)
	original := s.deps.DataDir
	moved := original + "-moved"
	changed := false
	defer func() {
		if changed {
			_ = os.Remove(original)
			if err := os.Rename(moved, original); err != nil {
				t.Error("测试临时目录未恢复")
			}
		}
	}()
	failure := errors.New("本地模拟写失败")
	writer := checkingExportWriter{dst: &failWriter{remaining: 0, err: failure}, check: func() {
		if changed {
			return
		}
		if err := os.Rename(original, moved); err != nil {
			t.Fatal(err)
		}
		changed = true
		if err := os.WriteFile(original, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}}
	if err := s.Export(t.Context(), testPassword, writer); !errors.Is(err, ErrIO) || !errors.Is(err, failure) {
		t.Fatal("写入和清理失败没有返回安全错误")
	}
	if err := s.Close(); !errors.Is(err, ErrIO) {
		t.Fatal("Close丢失待处理的明文清理失败")
	}
}
