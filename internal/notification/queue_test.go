package notification

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Seaony/Kakatta/internal/notify"
)

type queueTestClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

func (c *queueTestClock) current() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *queueTestClock) advance(delay time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delay)
	c.mu.Unlock()
}

func (c *queueTestClock) wait(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.waits = append(c.waits, delay)
	c.now = c.now.Add(delay)
	c.mu.Unlock()
	return ctx.Err()
}

func newQueueTestClock() *queueTestClock {
	return &queueTestClock{now: time.Now()}
}

func (c *queueTestClock) queues() *channelQueues {
	return &channelQueues{startedAt: c.current().Add(-time.Hour), now: c.current, waitFor: c.wait}
}

func mustQueueTurn(t *testing.T, queues *channelQueues, identity channelIdentity) *channelTurn {
	t.Helper()
	turn, err := queues.acquire(t.Context(), identity)
	if err != nil {
		t.Fatal(err)
	}
	return turn
}

func TestChannelQueuesIntervals(t *testing.T) {
	for _, test := range []struct {
		kind notify.Type
		gap  time.Duration
	}{
		{notify.WeCom, 3 * time.Second},
		{notify.DingTalk, 3 * time.Second},
		{notify.Feishu, time.Second},
		{notify.Bark, 0},
		{notify.Telegram, 0},
		{notify.Ntfy, 0},
		{notify.Gotify, 0},
		{notify.PushPlus, 0},
		{notify.ServerChan, 0},
		{notify.Webhook, 0},
		{notify.Email, 0},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			clock := newQueueTestClock()
			queues := clock.queues()
			identity := channelIdentity{ID: 1, UUID: "first"}
			first := mustQueueTurn(t, queues, identity)
			if err := first.wait(t.Context(), test.kind); err != nil {
				t.Fatal(err)
			}
			endedAt := clock.current()
			first.finish(test.kind, true, endedAt)
			second := mustQueueTurn(t, queues, identity)
			defer second.finish(test.kind, false, time.Time{})
			if err := second.wait(t.Context(), test.kind); err != nil {
				t.Fatal(err)
			}
			if elapsed := clock.current().Sub(endedAt); elapsed != test.gap {
				t.Fatalf("间隔 = %v，期望 %v", elapsed, test.gap)
			}
			wantTimers := 0
			if test.gap != 0 {
				wantTimers = 1
			}
			if len(clock.waits) != wantTimers {
				t.Fatalf("等待次数 = %d，期望 %d", len(clock.waits), wantTimers)
			}
		})
	}
}

func TestChannelQueuesGapStartsAtReturnAndIncludesPersistence(t *testing.T) {
	for _, persistedFor := range []time.Duration{0, 2 * time.Second, 4 * time.Second} {
		t.Run(persistedFor.String(), func(t *testing.T) {
			clock := newQueueTestClock()
			queues := clock.queues()
			identity := channelIdentity{ID: 1, UUID: "first"}
			first := mustQueueTurn(t, queues, identity)
			clock.advance(10 * time.Second)
			endedAt := clock.current()
			clock.advance(persistedFor)
			first.finish(notify.WeCom, true, endedAt)
			second := mustQueueTurn(t, queues, identity)
			defer second.finish(notify.WeCom, false, time.Time{})
			if err := second.wait(t.Context(), notify.WeCom); err != nil {
				t.Fatal(err)
			}
			want := max(3*time.Second, persistedFor)
			if elapsed := clock.current().Sub(endedAt); elapsed != want {
				t.Fatalf("返回后间隔 = %v，期望 %v", elapsed, want)
			}
		})
	}
}

func TestChannelQueuesTypeChangesUseLargerGap(t *testing.T) {
	for _, test := range []struct {
		previous, current notify.Type
		gap               time.Duration
	}{
		{notify.WeCom, notify.Webhook, 3 * time.Second},
		{notify.WeCom, notify.Feishu, 3 * time.Second},
		{notify.Feishu, notify.DingTalk, 3 * time.Second},
		{notify.Webhook, notify.Feishu, time.Second},
	} {
		t.Run(string(test.previous)+"_"+string(test.current), func(t *testing.T) {
			clock := newQueueTestClock()
			queues := clock.queues()
			identity := channelIdentity{ID: 1, UUID: "first"}
			first := mustQueueTurn(t, queues, identity)
			endedAt := clock.current()
			first.finish(test.previous, true, endedAt)
			second := mustQueueTurn(t, queues, identity)
			defer second.finish(test.current, false, time.Time{})
			if err := second.wait(t.Context(), test.current); err != nil {
				t.Fatal(err)
			}
			if elapsed := clock.current().Sub(endedAt); elapsed != test.gap {
				t.Fatalf("类型变化后的间隔 = %v，期望 %v", elapsed, test.gap)
			}
		})
	}
}

func TestChannelQueuesWaitCanRecheckChangedType(t *testing.T) {
	clock := newQueueTestClock()
	queues := clock.queues()
	identity := channelIdentity{ID: 1, UUID: "first"}
	first := mustQueueTurn(t, queues, identity)
	endedAt := clock.current()
	first.finish(notify.Webhook, true, endedAt)
	second := mustQueueTurn(t, queues, identity)
	defer second.finish(notify.WeCom, false, time.Time{})
	for _, kind := range []notify.Type{notify.Feishu, notify.WeCom, notify.WeCom} {
		if err := second.wait(t.Context(), kind); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := clock.current().Sub(endedAt); elapsed != 3*time.Second {
		t.Fatalf("重查类型后的间隔 = %v，期望 3s", elapsed)
	}
	if len(clock.waits) != 2 || clock.waits[0] != time.Second || clock.waits[1] != 2*time.Second {
		t.Fatalf("等待记录 = %v，期望 [1s 2s]", clock.waits)
	}
}

func TestChannelQueuesNoAttemptKeepsPreviousAnchor(t *testing.T) {
	clock := newQueueTestClock()
	queues := clock.queues()
	identity := channelIdentity{ID: 1, UUID: "first"}
	first := mustQueueTurn(t, queues, identity)
	endedAt := clock.current()
	first.finish(notify.WeCom, true, endedAt)
	clock.advance(time.Second)
	skipped := mustQueueTurn(t, queues, identity)
	skipped.finish(notify.Webhook, false, clock.current())
	last := mustQueueTurn(t, queues, identity)
	defer last.finish(notify.Webhook, false, time.Time{})
	if err := last.wait(t.Context(), notify.Webhook); err != nil {
		t.Fatal(err)
	}
	if elapsed := clock.current().Sub(endedAt); elapsed != 3*time.Second {
		t.Fatalf("未调用发送器改变了间隔：%v", elapsed)
	}
}

func TestChannelQueuesStartupGraceIsSharedAndOnlyOnce(t *testing.T) {
	for _, test := range []struct {
		kind notify.Type
		gap  time.Duration
	}{
		{notify.WeCom, 3 * time.Second},
		{notify.DingTalk, 3 * time.Second},
		{notify.Feishu, time.Second},
		{notify.Webhook, 0},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			clock := newQueueTestClock()
			startedAt := clock.current()
			queues := &channelQueues{now: clock.current, waitFor: clock.wait}
			first := mustQueueTurn(t, queues, channelIdentity{ID: 1, UUID: "first"})
			if err := first.wait(t.Context(), test.kind); err != nil {
				t.Fatal(err)
			}
			first.finish(test.kind, false, time.Time{})
			if elapsed := clock.current().Sub(startedAt); elapsed != test.gap {
				t.Fatalf("启动保护 = %v，期望 %v", elapsed, test.gap)
			}
			waitCount := len(clock.waits)
			second := mustQueueTurn(t, queues, channelIdentity{ID: 2, UUID: "second"})
			defer second.finish(test.kind, false, time.Time{})
			if err := second.wait(t.Context(), test.kind); err != nil {
				t.Fatal(err)
			}
			if queues.startedAt != startedAt || len(clock.waits) != waitCount {
				t.Fatalf("新渠道重复初始化启动保护：%v，等待 %v", queues.startedAt, clock.waits)
			}
		})
	}
}

func TestChannelQueuesExpiredStartupDoesNotDelayNewChannel(t *testing.T) {
	clock := newQueueTestClock()
	queues := clock.queues()
	before := clock.current()
	turn := mustQueueTurn(t, queues, channelIdentity{ID: 1, UUID: "first"})
	defer turn.finish(notify.WeCom, false, time.Time{})
	if err := turn.wait(t.Context(), notify.WeCom); err != nil {
		t.Fatal(err)
	}
	if clock.current() != before || len(clock.waits) != 0 {
		t.Fatalf("已过启动保护仍有等待：%v", clock.waits)
	}
}

func TestChannelQueuesPermitHeldThroughPersistence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		queues := &channelQueues{}
		identity := channelIdentity{ID: 1, UUID: "first"}
		first := mustQueueTurn(t, queues, identity)
		acquired := make(chan *channelTurn, 1)
		go func() {
			turn, err := queues.acquire(t.Context(), identity)
			if err != nil {
				t.Error(err)
				return
			}
			acquired <- turn
		}()
		synctest.Wait()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		select {
		case <-acquired:
			t.Fatal("保存结果前释放了许可")
		default:
		}
		first.finish(notify.Webhook, true, time.Now().Add(-2*time.Second))
		second := <-acquired
		second.finish(notify.Webhook, false, time.Time{})
		if !queues.forget(identity) {
			t.Fatal("完成后仍有许可持有者")
		}
	})
}

func TestChannelQueuesDifferentIdentitiesCanProceedTogether(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		queues := &channelQueues{}
		first := mustQueueTurn(t, queues, channelIdentity{ID: 1, UUID: "old"})
		otherID := mustQueueTurn(t, queues, channelIdentity{ID: 2, UUID: "old"})
		otherUUID := mustQueueTurn(t, queues, channelIdentity{ID: 1, UUID: "new"})
		if first.lane == otherID.lane || first.lane == otherUUID.lane {
			t.Fatal("不同渠道身份共用了一条队列")
		}
		otherID.finish(notify.Webhook, false, time.Time{})
		otherUUID.finish(notify.Webhook, false, time.Time{})
		first.finish(notify.Webhook, false, time.Time{})
	})
}

func TestChannelQueuesConcurrentAcquireSerializes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		queues := &channelQueues{}
		identity := channelIdentity{ID: 1, UUID: "first"}
		var active, completed atomic.Int64
		var work sync.WaitGroup
		for range 64 {
			work.Go(func() {
				turn, err := queues.acquire(t.Context(), identity)
				if err != nil {
					t.Error(err)
					return
				}
				if count := active.Add(1); count != 1 {
					t.Errorf("同时持有许可：%d", count)
				}
				time.Sleep(time.Millisecond)
				active.Add(-1)
				completed.Add(1)
				turn.finish(notify.Webhook, true, time.Now())
			})
		}
		work.Wait()
		if completed.Load() != 64 || !queues.forget(identity) {
			t.Fatalf("完成数量 = %d，或仍有等待者", completed.Load())
		}
	})
}

func TestChannelQueuesCancelPermitAndProtectWaitingUsers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		queues := &channelQueues{}
		identity := channelIdentity{ID: 1, UUID: "first"}
		first := mustQueueTurn(t, queues, identity)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			turn, err := queues.acquire(ctx, identity)
			if turn != nil {
				turn.finish(notify.Webhook, false, time.Time{})
			}
			done <- err
		}()
		synctest.Wait()
		if first.lane.users != 2 || queues.forget(identity) {
			t.Fatal("持有者或等待者没能保护队列")
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("取消错误 = %v", err)
		}
		if first.lane.users != 1 || queues.forget(identity) {
			t.Fatal("取消等待影响了活跃持有者")
		}
		first.finish(notify.Webhook, false, time.Time{})
		if !queues.forget(identity) || len(queues.lanes) != 0 {
			t.Fatal("空闲队列未被回收")
		}
		if !queues.forget(identity) {
			t.Fatal("忘记不存在的队列应当成功")
		}
	})
}

func TestChannelQueuesForgetCannotSplitLane(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		queues := &channelQueues{}
		identity := channelIdentity{ID: 1, UUID: "first"}
		first := mustQueueTurn(t, queues, identity)
		original := first.lane
		acquired := make(chan *channelTurn, 2)
		for range 2 {
			go func() {
				turn, err := queues.acquire(t.Context(), identity)
				if err != nil {
					t.Error(err)
					return
				}
				acquired <- turn
			}()
		}
		synctest.Wait()
		if queues.forget(identity) {
			t.Fatal("删除了仍在使用的队列")
		}
		first.finish(notify.Webhook, false, time.Time{})
		for range 2 {
			next := <-acquired
			if next.lane != original || queues.forget(identity) {
				t.Fatal("等待者进入了第二条队列，或活跃队列被删除")
			}
			next.finish(notify.Webhook, false, time.Time{})
		}
		if !queues.forget(identity) {
			t.Fatal("等待者完成后仍不能回收")
		}
	})
}

func TestChannelQueuesForgetUsesCompleteIdentity(t *testing.T) {
	queues := &channelQueues{}
	oldIdentity := channelIdentity{ID: 1, UUID: "old"}
	newIdentity := channelIdentity{ID: 1, UUID: "new"}
	old := mustQueueTurn(t, queues, oldIdentity)
	old.finish(notify.Webhook, false, time.Time{})
	current := mustQueueTurn(t, queues, newIdentity)
	defer current.finish(notify.Webhook, false, time.Time{})
	if !queues.forget(oldIdentity) || queues.lanes[newIdentity] != current.lane {
		t.Fatal("回收旧身份影响了复用编号的新渠道")
	}
}

func TestChannelQueuesAlreadyCancelledDoesNotRegister(t *testing.T) {
	queues := &channelQueues{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	turn, err := queues.acquire(ctx, channelIdentity{ID: 1, UUID: "first"})
	if turn != nil || !errors.Is(err, context.Canceled) || len(queues.lanes) != 0 {
		t.Fatalf("已取消请求仍进入队列：turn=%v，err=%v", turn, err)
	}
}

func TestChannelQueuesCancelIntervalStopsTimerAndReleases(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		queues := &channelQueues{}
		identity := channelIdentity{ID: 1, UUID: "first"}
		turn := mustQueueTurn(t, queues, identity)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		var sends atomic.Int64
		go func() {
			defer turn.finish(notify.WeCom, false, time.Time{})
			err := turn.wait(ctx, notify.WeCom)
			if err == nil {
				sends.Add(1)
			}
			done <- err
		}()
		synctest.Wait()
		startedAt := time.Now()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("取消错误 = %v", err)
		}
		synctest.Wait()
		if time.Now() != startedAt || sends.Load() != 0 {
			t.Fatal("取消后仍等到了原定时刻，或调用了发送")
		}
		if !turn.lane.lastFinishedAt.IsZero() || turn.lane.lastGap != 0 {
			t.Fatal("取消等待被当作一次发送")
		}
		next := mustQueueTurn(t, queues, identity)
		next.finish(notify.Webhook, false, time.Time{})
		if !queues.forget(identity) {
			t.Fatal("取消等待后仍持有队列")
		}
	})
}

func TestChannelQueuesTimerHonorsExactBoundary(t *testing.T) {
	for _, test := range []struct {
		kind notify.Type
		gap  time.Duration
	}{
		{notify.WeCom, 3 * time.Second},
		{notify.DingTalk, 3 * time.Second},
		{notify.Feishu, time.Second},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				queues := &channelQueues{}
				turn := mustQueueTurn(t, queues, channelIdentity{ID: 1, UUID: "first"})
				defer turn.finish(test.kind, false, time.Time{})
				done := make(chan error, 1)
				go func() { done <- turn.wait(t.Context(), test.kind) }()
				synctest.Wait()
				time.Sleep(test.gap - time.Nanosecond)
				synctest.Wait()
				select {
				case err := <-done:
					t.Fatalf("间隔不足时提前放行：%v", err)
				default:
				}
				time.Sleep(time.Nanosecond)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestChannelQueuesCanceledBeforeTimerDoesNotWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		startedAt := time.Now()
		if err := waitChannelInterval(ctx, time.Hour); !errors.Is(err, context.Canceled) {
			t.Fatalf("取消错误 = %v", err)
		}
		if time.Now() != startedAt {
			t.Fatal("已取消的等待仍启动了定时器")
		}
	})
}
