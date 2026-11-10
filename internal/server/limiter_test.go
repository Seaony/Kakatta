package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type limiterClock struct {
	mu  sync.Mutex
	now time.Time
}

func newLimiterClock() *limiterClock {
	return &limiterClock{now: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
}

func (c *limiterClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *limiterClock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func limiterForTest(clock *limiterClock) *loginLimiter {
	return newLoginLimiter(clock.Now, func(context.Context, LoginFailureEvent) error { return nil })
}

func takeAttempt(t *testing.T, l *loginLimiter, ip string) *loginAttempt {
	t.Helper()
	a, wait, err := l.begin(context.Background(), ip)
	if err != nil || wait != 0 || a == nil {
		t.Fatalf("申请登录名额失败：attempt=%v wait=%v err=%v", a != nil, wait, err)
	}
	return a
}

func finishAttempt(t *testing.T, a *loginAttempt, result attemptResult) {
	t.Helper()
	if err := a.finish(context.Background(), result); err != nil {
		t.Fatalf("结束登录失败：%v", err)
	}
}

func expectWait(t *testing.T, l *loginLimiter, ip string, want time.Duration) {
	t.Helper()
	a, wait, err := l.begin(context.Background(), ip)
	if a != nil || err != nil || wait != want {
		t.Fatalf("等待不符：attempt=%v wait=%v err=%v want=%v", a != nil, wait, err, want)
	}
}

func failDifferentIPs(t *testing.T, l *loginLimiter, count int) {
	t.Helper()
	for i := range count {
		finishAttempt(t, takeAttempt(t, l, fmt.Sprintf("192.0.2.%d", i+1)), attemptFailure)
	}
}

func TestLimiterIPDelayAndBlockedAttempts(t *testing.T) {
	clock := newLimiterClock()
	l := limiterForTest(clock)
	finishAttempt(t, takeAttempt(t, l, "192.0.2.1"), attemptFailure)
	expectWait(t, l, "192.0.2.1", time.Second)
	clock.Add(500 * time.Millisecond)
	expectWait(t, l, "192.0.2.1", 500*time.Millisecond)
	clock.Add(500 * time.Millisecond)
	for _, delay := range []time.Duration{2, 4, 8, 16, 32, 60, 60} {
		finishAttempt(t, takeAttempt(t, l, "192.0.2.1"), attemptFailure)
		expectWait(t, l, "192.0.2.1", delay*time.Second)
		clock.Add(delay * time.Second)
	}
	finishAttempt(t, takeAttempt(t, l, "192.0.2.1"), attemptNeutral)
}

func TestLimiterIPResetAtFifteenMinutes(t *testing.T) {
	clock := newLimiterClock()
	l := limiterForTest(clock)
	finishAttempt(t, takeAttempt(t, l, "192.0.2.1"), attemptFailure)
	clock.Add(15*time.Minute - time.Nanosecond)
	finishAttempt(t, takeAttempt(t, l, "192.0.2.1"), attemptFailure)
	expectWait(t, l, "192.0.2.1", 2*time.Second)
	clock.Add(15 * time.Minute)
	finishAttempt(t, takeAttempt(t, l, "192.0.2.1"), attemptFailure)
	expectWait(t, l, "192.0.2.1", time.Second)
	clock.Add(15 * time.Minute)
	finishAttempt(t, takeAttempt(t, l, "192.0.2.2"), attemptNeutral)
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.ips) != 0 {
		t.Fatalf("旧 IP 记录没有清理：%d", len(l.ips))
	}
}

func TestLimiterNeutralKeepsFailuresAndSuccessResets(t *testing.T) {
	clock := newLimiterClock()
	var events []bool
	l := newLoginLimiter(clock.Now, func(_ context.Context, event LoginFailureEvent) error {
		active := event.Active
		events = append(events, active)
		return nil
	})
	finishAttempt(t, takeAttempt(t, l, "192.0.2.1"), attemptFailure)
	clock.Add(time.Second)
	finishAttempt(t, takeAttempt(t, l, "192.0.2.1"), attemptNeutral)
	finishAttempt(t, takeAttempt(t, l, "192.0.2.1"), attemptFailure)
	expectWait(t, l, "192.0.2.1", 2*time.Second)
	clock.Add(2 * time.Second)
	finishAttempt(t, takeAttempt(t, l, "192.0.2.1"), attemptSuccess)
	finishAttempt(t, takeAttempt(t, l, "192.0.2.1"), attemptFailure)
	expectWait(t, l, "192.0.2.1", time.Second)
	if len(events) != 1 || events[0] {
		t.Fatalf("只应在完整登录成功时解决事件：%v", events)
	}
}

func TestLimiterGlobalWindowAndThresholdEvent(t *testing.T) {
	clock := newLimiterClock()
	var events []bool
	l := newLoginLimiter(clock.Now, func(_ context.Context, event LoginFailureEvent) error {
		active := event.Active
		events = append(events, active)
		return nil
	})
	failDifferentIPs(t, l, 20)
	expectWait(t, l, "198.51.100.1", time.Minute)
	clock.Add(time.Minute - time.Nanosecond)
	expectWait(t, l, "198.51.100.1", time.Nanosecond)
	clock.Add(time.Nanosecond)
	finishAttempt(t, takeAttempt(t, l, "198.51.100.1"), attemptNeutral)
	if len(events) != 1 || !events[0] {
		t.Fatalf("只有第20次真实失败应产生保护事件：%v", events)
	}
}

func TestLimiterActiveRequestsAndReservation(t *testing.T) {
	clock := newLimiterClock()
	l := limiterForTest(clock)
	var active []*loginAttempt
	for i := range 4 {
		active = append(active, takeAttempt(t, l, fmt.Sprintf("192.0.2.%d", i)))
	}
	expectWait(t, l, "198.51.100.1", time.Second)
	finishAttempt(t, active[0], attemptNeutral)
	active[0] = takeAttempt(t, l, "198.51.100.1")
	for _, a := range active {
		finishAttempt(t, a, attemptNeutral)
	}
	failDifferentIPs(t, l, 19)
	last := takeAttempt(t, l, "198.51.100.2")
	expectWait(t, l, "198.51.100.3", time.Second)
	finishAttempt(t, last, attemptNeutral)
	last = takeAttempt(t, l, "198.51.100.3")
	finishAttempt(t, last, attemptFailure)
	expectWait(t, l, "198.51.100.4", time.Minute)
}

func TestLimiterConcurrentRequestsCannotPassRemainingBudget(t *testing.T) {
	clock := newLimiterClock()
	var events atomic.Int32
	l := newLoginLimiter(clock.Now, func(_ context.Context, event LoginFailureEvent) error {
		active := event.Active
		if active {
			events.Add(1)
		}
		return nil
	})
	failDifferentIPs(t, l, 16)
	var active []*loginAttempt
	for i := range 4 {
		active = append(active, takeAttempt(t, l, fmt.Sprintf("198.51.100.%d", i)))
	}
	var group sync.WaitGroup
	for i := range 100 {
		group.Go(func() {
			a, wait, err := l.begin(context.Background(), fmt.Sprintf("203.0.113.%d", i))
			if a != nil || wait <= 0 || err != nil {
				t.Errorf("并发绕过预留名额：attempt=%v wait=%v err=%v", a != nil, wait, err)
			}
		})
	}
	group.Wait()
	if events.Load() != 0 {
		t.Fatal("在途请求被误计为失败")
	}
	for _, a := range active {
		group.Go(func() {
			if err := a.finish(context.Background(), attemptFailure); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	expectWait(t, l, "203.0.113.200", time.Minute)
	if events.Load() != 1 {
		t.Fatalf("保护事件次数不符：%d", events.Load())
	}
}

func TestLimiterCancellationDoesNotReserveOrSkipEvent(t *testing.T) {
	clock := newLimiterClock()
	var called bool
	l := newLoginLimiter(clock.Now, func(ctx context.Context, event LoginFailureEvent) error {
		active := event.Active
		called = true
		if ctx.Err() != nil || active {
			t.Errorf("取消请求影响了成功事件：active=%v err=%v", active, ctx.Err())
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Errorf("事件上下文缺少5秒期限：%v", deadline)
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a, wait, err := l.begin(ctx, "192.0.2.1")
	if a != nil || wait != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("取消申请仍占名额：attempt=%v wait=%v err=%v", a != nil, wait, err)
	}
	a = takeAttempt(t, l, "192.0.2.1")
	if err := a.finish(ctx, attemptSuccess); err != nil || !called {
		t.Fatalf("取消后没有保存已成功的结果：called=%v err=%v", called, err)
	}
	for range 4 {
		takeAttempt(t, l, "192.0.2.2")
	}
	expectWait(t, l, "192.0.2.3", time.Second)
}

func TestLimiterFinishIsExactlyOnce(t *testing.T) {
	clock := newLimiterClock()
	l := limiterForTest(clock)
	a := takeAttempt(t, l, "192.0.2.1")
	var group sync.WaitGroup
	for range 100 {
		group.Go(func() {
			if err := a.finish(context.Background(), attemptFailure); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	finishAttempt(t, a, attemptNeutral)
	expectWait(t, l, "192.0.2.1", time.Second)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inflight != 0 || len(l.failed) != 1 {
		t.Fatalf("重复结束改变了额度：inflight=%d failed=%d", l.inflight, len(l.failed))
	}
}

func TestLimiterEventFailureBlocksAndRetriesAtMostOncePerSecond(t *testing.T) {
	clock := newLimiterClock()
	var calls atomic.Int32
	l := newLoginLimiter(clock.Now, func(_ context.Context, event LoginFailureEvent) error {
		active := event.Active
		if !active {
			t.Error("应保存未解决的保护事件")
		}
		if calls.Add(1) <= 2 {
			return errors.New("敏感数据库地址和密码")
		}
		return nil
	})
	failDifferentIPs(t, l, 19)
	last := takeAttempt(t, l, "198.51.100.1")
	err := last.finish(context.Background(), attemptFailure)
	if err != errLoginEvent || strings.Contains(err.Error(), "密码") {
		t.Fatalf("事件保存失败没有闭锁或泄露原文：%v", err)
	}
	for range 20 {
		a, wait, err := l.begin(context.Background(), "198.51.100.2")
		if a != nil || wait != 0 || err != errLoginEvent {
			t.Fatalf("未落盘事件被放过：attempt=%v wait=%v err=%v", a != nil, wait, err)
		}
	}
	clock.Add(time.Second - time.Nanosecond)
	if _, _, err := l.begin(context.Background(), "198.51.100.2"); err != errLoginEvent || calls.Load() != 1 {
		t.Fatalf("不足一秒重复写事件：calls=%d err=%v", calls.Load(), err)
	}
	clock.Add(time.Nanosecond)
	if _, _, err := l.begin(context.Background(), "198.51.100.2"); err != errLoginEvent || calls.Load() != 2 {
		t.Fatalf("一秒后没有重试事件：calls=%d err=%v", calls.Load(), err)
	}
	clock.Add(time.Second)
	expectWait(t, l, "198.51.100.2", 58*time.Second)
	if calls.Load() != 3 {
		t.Fatalf("事件补记次数不符：%d", calls.Load())
	}
	clock.Add(58 * time.Second)
	finishAttempt(t, takeAttempt(t, l, "198.51.100.2"), attemptNeutral)
}

func TestLimiterEventRetryIsSerialized(t *testing.T) {
	clock := newLimiterClock()
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	l := newLoginLimiter(clock.Now, func(context.Context, LoginFailureEvent) error {
		switch calls.Add(1) {
		case 1:
			return errors.New("失败")
		case 2:
			close(started)
			<-release
		}
		return nil
	})
	failDifferentIPs(t, l, 19)
	if err := takeAttempt(t, l, "198.51.100.1").finish(context.Background(), attemptFailure); err != errLoginEvent {
		t.Fatalf("首次事件没有失败：%v", err)
	}
	clock.Add(time.Second)
	completed := make(chan struct{})
	go func() {
		defer close(completed)
		_, _, err := l.begin(context.Background(), "198.51.100.2")
		if err != nil {
			t.Errorf("重试失败：%v", err)
		}
	}()
	<-started
	var group sync.WaitGroup
	for range 50 {
		group.Go(func() {
			a, wait, err := l.begin(context.Background(), "198.51.100.3")
			if a != nil || wait != 0 || err != errLoginEvent {
				t.Errorf("事件写入途中未闭锁：attempt=%v wait=%v err=%v", a != nil, wait, err)
			}
		})
	}
	group.Wait()
	if calls.Load() != 2 {
		t.Fatalf("事件并发重试：%d", calls.Load())
	}
	close(release)
	<-completed
}

func TestLimiterConcurrentSuccessesWaitForOrderedEvents(t *testing.T) {
	clock := newLimiterClock()
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	l := newLoginLimiter(clock.Now, func(_ context.Context, event LoginFailureEvent) error {
		active := event.Active
		if active {
			t.Error("成功登录不应新建保护事件")
		}
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil
	})
	one := takeAttempt(t, l, "192.0.2.1")
	two := takeAttempt(t, l, "192.0.2.2")
	var group sync.WaitGroup
	group.Go(func() {
		if err := one.finish(context.Background(), attemptSuccess); err != nil {
			t.Error(err)
		}
	})
	<-started
	group.Go(func() {
		if err := two.finish(context.Background(), attemptSuccess); err != nil {
			t.Error(err)
		}
	})
	close(release)
	group.Wait()
	if calls.Load() != 2 {
		t.Fatalf("成功事件没有按次保存：%d", calls.Load())
	}
}

func TestLimiterBackwardAndForwardClock(t *testing.T) {
	clock := newLimiterClock()
	l := limiterForTest(clock)
	finishAttempt(t, takeAttempt(t, l, "192.0.2.1"), attemptFailure)
	clock.Add(-time.Hour)
	expectWait(t, l, "192.0.2.1", time.Second)
	clock.Add(time.Hour + 500*time.Millisecond)
	expectWait(t, l, "192.0.2.1", 500*time.Millisecond)
	clock.Add(24 * time.Hour)
	finishAttempt(t, takeAttempt(t, l, "192.0.2.1"), attemptFailure)
	expectWait(t, l, "192.0.2.1", time.Second)
}

func TestLimiterFailureWindowStartsAtCompletion(t *testing.T) {
	clock := newLimiterClock()
	l := limiterForTest(clock)
	a := takeAttempt(t, l, "192.0.2.250")
	clock.Add(61 * time.Second)
	finishAttempt(t, a, attemptFailure)
	failDifferentIPs(t, l, 19)
	expectWait(t, l, "198.51.100.1", time.Minute)
}

func TestLimiterMissingEventCallbackFailsClosed(t *testing.T) {
	clock := newLimiterClock()
	l := newLoginLimiter(clock.Now, nil)
	if err := takeAttempt(t, l, "192.0.2.1").finish(context.Background(), attemptSuccess); err != errLoginEvent {
		t.Fatalf("事件依赖缺失却成功：%v", err)
	}
	if a, wait, err := l.begin(context.Background(), "192.0.2.1"); a != nil || wait != 0 || err != errLoginEvent {
		t.Fatalf("事件依赖缺失后仍放行：attempt=%v wait=%v err=%v", a != nil, wait, err)
	}
}

func TestLimiterThresholdFactsSurviveConcurrentCompletionAndRetry(t *testing.T) {
	clock := newLimiterClock()
	var delivered []LoginFailureEvent
	l := newLoginLimiter(clock.Now, func(_ context.Context, event LoginFailureEvent) error {
		delivered = append(delivered, event)
		if len(delivered) <= 2 {
			event.IP, event.Limit = "请求不能改写队列", 999
			return errors.New("本地测试写入失败")
		}
		return nil
	})
	failDifferentIPs(t, l, 16)
	var active []*loginAttempt
	for i := range 4 {
		active = append(active, takeAttempt(t, l, fmt.Sprintf("198.51.100.%d", i+1)))
	}
	var concurrent sync.WaitGroup
	for _, attempt := range active[:3] {
		concurrent.Go(func() { finishAttempt(t, attempt, attemptFailure) })
	}
	concurrent.Wait()
	if err := active[3].finish(t.Context(), attemptFailure); !errors.Is(err, errLoginEvent) {
		t.Fatal("第20次失败的事件写入必须失败")
	}
	want := LoginFailureEvent{Active: true, Limit: 20, IP: "198.51.100.4"}
	clock.Add(61 * time.Second)
	if attempt, _, err := l.begin(t.Context(), "203.0.113.88"); attempt != nil || !errors.Is(err, errLoginEvent) {
		t.Fatal("窗口过后也不能跳过尚未保存的事件")
	}
	clock.Add(time.Second)
	finishAttempt(t, takeAttempt(t, l, "203.0.113.99"), attemptNeutral)
	if len(delivered) != 3 {
		t.Fatalf("应重试原事件三次，实际%d次", len(delivered))
	}
	for _, event := range delivered {
		if event != want {
			t.Fatalf("重试不能用后续请求或窗口改写阈值事实：%+v", event)
		}
	}
	finishAttempt(t, takeAttempt(t, l, "203.0.113.99"), attemptSuccess)
	if len(delivered) != 4 || delivered[3] != (LoginFailureEvent{Active: false}) {
		t.Fatal("解决事件不能伪造新的阈值或来源")
	}
}
