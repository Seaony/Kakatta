package service

import (
	"errors"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
)

func TestRuntimeFailureDiagnosticsNeverHoldAdmissionLock(t *testing.T) {
	s := runtimeCoreService(t)
	entered, release, finished := make(chan map[string]any, 1), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	calls := 0
	s.diagnosticLog = func(event string, fields map[string]any) error {
		calls++
		if event != "存储故障停机" {
			t.Error("故障事件名称不明确")
		}
		entered <- fields
		<-release
		return nil
	}
	go func() {
		s.setRuntimeError(storageFailure(errors.Join(syscall.ENOSPC, errors.New("fixture-root-cause"))))
		close(finished)
	}()
	fields := <-entered
	if s.RuntimeContext().Err() == nil {
		t.Error("写日志前没有取消访问")
	}
	status := make(chan *locales.Message, 1)
	go func() { status <- s.RuntimeFailure() }()
	select {
	case failure := <-status:
		if failure == nil || failure.Code != "runtime.storage_full" || len(failure.Args) != 0 {
			t.Errorf("对外故障类别改变：%+v", failure)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("写诊断文件占住了调度锁，连只读故障说明也读不到")
	}
	select {
	case <-finished:
	default:
		t.Error("记录故障首因阻塞了关闸门的调用")
	}
	if detail, _ := fields["cause"].(string); !strings.Contains(detail, "fixture-root-cause") {
		t.Error("本机日志没有保留被ServiceError包住的首因")
	}
	once.Do(func() { close(release) })
	<-finished
	<-s.runtimeStopDone
	s.setRuntimeError(errors.New("later-cause"))
	if calls != 1 {
		t.Fatalf("后来错误覆盖或重复写首因：%d", calls)
	}
}
