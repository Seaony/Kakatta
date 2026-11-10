package modelservice

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

func waitModelJobs(t *testing.T, s *Service, jobs int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		got := s.jobs
		s.mu.Unlock()
		if got == jobs {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("活动工作没有变为%d", jobs)
}

func acquireSlot(t *testing.T, s *Service) func() {
	t.Helper()
	release, err := s.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return release
}

func TestSlotsShareCurrentLimitAndReleaseExactlyOnce(t *testing.T) {
	f := newModelFixture(t)
	applyModelSettings(t, f, map[string]any{"ai.max_concurrent_requests": 2})
	first, second := acquireSlot(t, f.s), acquireSlot(t, f.s)
	type slotResult struct {
		release func()
		err     error
	}
	result := make(chan slotResult, 1)
	go func() { r, err := f.s.Acquire(t.Context()); result <- slotResult{r, err} }()
	waitModelJobs(t, f.s, 3)
	select {
	case <-result:
		t.Fatal("共享上限已满仍取得名额")
	default:
	}
	first()
	first()
	var third slotResult
	select {
	case third = <-result:
	case <-time.After(time.Second):
		t.Fatal("释放名额未唤醒等待")
	}
	if third.err != nil {
		t.Fatal(third.err)
	}
	f.s.slots.mu.Lock()
	active := f.s.slots.active
	f.s.slots.mu.Unlock()
	if active != 2 {
		t.Fatalf("重复release破坏计数：%d", active)
	}
	second()
	third.release()
	waitModelJobs(t, f.s, 0)
}

func TestSlotsRereadSettingsAfterReleaseAndDoNotCancelActive(t *testing.T) {
	f := newModelFixture(t)
	applyModelSettings(t, f, map[string]any{"ai.max_concurrent_requests": 2})
	first, second := acquireSlot(t, f.s), acquireSlot(t, f.s)
	applyModelSettings(t, f, map[string]any{"ai.max_concurrent_requests": 1})
	result := make(chan func(), 1)
	errorsCh := make(chan error, 1)
	go func() {
		release, err := f.s.Acquire(t.Context())
		if err != nil {
			errorsCh <- err
			return
		}
		result <- release
	}()
	waitModelJobs(t, f.s, 3)
	first()
	// 还有一个已发调用，降低上限不撤销它，也不再放新调用。
	select {
	case <-result:
		t.Fatal("降低上限后在途数等于上限仍放行")
	case err := <-errorsCh:
		t.Fatal(err)
	case <-time.After(20 * time.Millisecond):
	}
	applyModelSettings(t, f, map[string]any{"ai.max_concurrent_requests": 3})
	second()
	select {
	case release := <-result:
		release()
	case err := <-errorsCh:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("释放后未重读新上限")
	}
	waitModelJobs(t, f.s, 0)
}

func TestSlotsCancelWaitAndCloseStillWaitsForOwnerRelease(t *testing.T) {
	f := newModelFixture(t)
	applyModelSettings(t, f, map[string]any{"ai.max_concurrent_requests": 1})
	release := acquireSlot(t, f.s)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := f.s.Acquire(ctx); result <- err }()
	waitModelJobs(t, f.s, 2)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("等待取消丢失：%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("等待取消不及时")
	}
	deadline, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	if err := f.s.Close(deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("持有名额尚未归还却报告关闭：%v", err)
	}
	release()
	release()
	if err := f.s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Acquire(t.Context()); err == nil {
		t.Fatal("关闭之后仍接名额")
	}
}

func TestSlotsAccountingFaultWakesWaiterEvenDuringSettingsRead(t *testing.T) {
	f := newModelFixture(t)
	applyModelSettings(t, f, map[string]any{"ai.max_concurrent_requests": 1})
	release := acquireSlot(t, f.s)
	defer release()
	conn, err := f.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var closeOnce sync.Once
	defer closeOnce.Do(func() { _ = conn.Close() })
	result := make(chan error, 1)
	go func() { _, err := f.s.Acquire(t.Context()); result <- err }()
	waitModelJobs(t, f.s, 2)
	// 等待者正阻塞在读取设置；故障先唤醒旧通道，读取结束后也必须看见故障。
	f.s.accounting.mu.Lock()
	fault := f.s.latchAccountingFault(errors.New("local failure"))
	f.s.accounting.mu.Unlock()
	closeOnce.Do(func() { _ = conn.Close() })
	select {
	case err := <-result:
		if err != fault {
			t.Fatalf("等待者没有返回共享故障：%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("读取设置期间错过故障唤醒")
	}
}

func TestSlotsSettingsReadFailureDoesNotLeakWork(t *testing.T) {
	f := newModelFixture(t)
	f.exec(t, `INSERT INTO settings(key,value_json,updated_at) VALUES('ai.max_concurrent_requests','0',1700000000000)`)
	if _, err := f.s.Acquire(t.Context()); err == nil {
		t.Fatal("损坏设置仍取得名额")
	}
	waitModelJobs(t, f.s, 0)
	f.exec(t, `DELETE FROM settings WHERE key='ai.max_concurrent_requests'`)
	release := acquireSlot(t, f.s)
	release()
}
