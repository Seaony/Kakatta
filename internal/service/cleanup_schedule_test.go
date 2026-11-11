package service

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/settings"
)

type cleanupScheduledWait struct {
	delay   time.Duration
	elapsed chan struct{}
	stopped chan struct{}
}

type cleanupScheduleClock struct {
	waits         chan cleanupScheduledWait
	cancelRelease <-chan struct{}
}

func (c cleanupScheduleClock) Sleep(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	wait := cleanupScheduledWait{delay: delay, elapsed: make(chan struct{}), stopped: make(chan struct{})}
	select {
	case c.waits <- wait:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-wait.elapsed:
		return nil
	case <-ctx.Done():
		close(wait.stopped)
		if c.cancelRelease != nil {
			<-c.cancelRelease
		}
		return ctx.Err()
	}
}

func (cleanupScheduleClock) Float64() float64 { return 0 }

type cacheCleanupStub struct{}

func (cacheCleanupStub) Get(context.Context, string) ([]byte, error)      { return nil, os.ErrInvalid }
func (cacheCleanupStub) Put(context.Context, string, []byte, int64) error { return os.ErrInvalid }

type cleanupScheduleCache struct {
	cacheCleanupStub
	pruned chan struct{}
}

func (c cleanupScheduleCache) Prune(ctx context.Context, _ int64) (int64, error) {
	select {
	case c.pruned <- struct{}{}:
		return 0, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (cleanupScheduleCache) Clear(context.Context) (int64, error) { return 0, nil }

func cleanupScheduleFixture(t *testing.T, clock cleanupScheduleClock) (*Service, func(time.Duration), <-chan struct{}) {
	t.Helper()
	db, _, base := cleanupDB(t)
	var nowMS atomic.Int64
	nowMS.Store(base)
	now := func() time.Time { return time.UnixMilli(nowMS.Load()) }
	prefs, err := appsettings.New(t.Context(), db, settings.Environment{TZ: "UTC"}, now)
	if err != nil {
		t.Fatal(err)
	}
	pruned := make(chan struct{}, 1)
	s := &Service{db: db, settings: prefs, now: now, pacer: pacer.New(clock), imageCache: cleanupScheduleCache{pruned: pruned}}
	s.initRuntime()
	return s, func(elapsed time.Duration) { nowMS.Store(base + elapsed.Milliseconds()) }, pruned
}

func cleanupScheduleSetHours(t *testing.T, s *Service, hours int64) {
	t.Helper()
	raw := json.RawMessage(strconv.FormatInt(hours, 10))
	snapshot, err := s.settings.Apply(t.Context(), settings.Values{"cleanup.interval_hours": raw})
	if err != nil || string(snapshot.Values["cleanup.interval_hours"]) != string(raw) {
		t.Fatal("清理间隔未保存", err)
	}
}

func cleanupScheduleWait(t *testing.T, clock cleanupScheduleClock, want time.Duration) cleanupScheduledWait {
	t.Helper()
	select {
	case wait := <-clock.waits:
		if wait.delay != want {
			t.Fatalf("等待时间为 %v，预期 %v", wait.delay, want)
		}
		return wait
	case <-time.After(time.Second):
		t.Fatal("清理循环未开始等待")
		return cleanupScheduledWait{}
	}
}

func TestCleanupScheduleShorterSettingRunsWhenDue(t *testing.T) {
	clock := cleanupScheduleClock{waits: make(chan cleanupScheduledWait)}
	s, setElapsed, pruned := cleanupScheduleFixture(t, clock)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); s.cleanupLoop(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	oldWait := cleanupScheduleWait(t, clock, 24*time.Hour)
	setElapsed(3 * time.Hour)
	cleanupScheduleSetHours(t, s, 1)
	s.SettingsChanged()
	select {
	case <-oldWait.stopped:
	case <-time.After(time.Second):
		t.Fatal("间隔已从 24 小时保存为 1 小时，旧等待仍未取消")
	}
	select {
	case <-pruned:
	case <-time.After(time.Second):
		t.Fatal("已到期的清理没有执行")
	}
	cleanupScheduleWait(t, clock, time.Hour)
}

func TestCleanupScheduleLongerSettingPreservesCompletionAnchor(t *testing.T) {
	clock := cleanupScheduleClock{waits: make(chan cleanupScheduledWait)}
	s, setElapsed, pruned := cleanupScheduleFixture(t, clock)
	cleanupScheduleSetHours(t, s, 1)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); s.cleanupLoop(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	oldWait := cleanupScheduleWait(t, clock, time.Hour)
	setElapsed(30 * time.Minute)
	cleanupScheduleSetHours(t, s, 3)
	s.SettingsChanged()
	newWait := cleanupScheduleWait(t, clock, 2*time.Hour+30*time.Minute)
	select {
	case <-oldWait.stopped:
	default:
		t.Fatal("新等待启动前没有结束旧等待")
	}

	// 模拟旧计时先于设置通知到达，清理仍要读取已保存的间隔。
	setElapsed(3 * time.Hour)
	cleanupScheduleSetHours(t, s, 4)
	close(newWait.elapsed)
	nextWait := cleanupScheduleWait(t, clock, time.Hour)
	select {
	case <-pruned:
		t.Fatal("间隔延长后仍在旧时刻清理")
	default:
	}
	setElapsed(4 * time.Hour)
	close(nextWait.elapsed)
	select {
	case <-pruned:
	case <-time.After(time.Second):
		t.Fatal("延长后的时刻到期却没有清理")
	}
	cleanupScheduleWait(t, clock, 4*time.Hour)
}

func TestCleanupScheduleCloseJoinsCancelledWait(t *testing.T) {
	release := make(chan struct{})
	releaseWait := sync.OnceFunc(func() { close(release) })
	clock := cleanupScheduleClock{waits: make(chan cleanupScheduledWait), cancelRelease: release}
	s, _, _ := cleanupScheduleFixture(t, clock)
	t.Cleanup(func() { releaseWait(); _ = s.Close() })
	s.background.Add(1)
	go func() { defer s.background.Done(); s.cleanupLoop(s.ctx) }()
	wait := cleanupScheduleWait(t, clock, 24*time.Hour)
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case <-wait.stopped:
	case <-time.After(time.Second):
		t.Fatal("关闭服务未取消清理等待")
	}
	select {
	case <-closed:
		t.Fatal("清理等待退出前关闭已返回")
	default:
	}
	releaseWait()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("等待已退出，关闭仍未结束")
	}
}

func TestCleanupScheduleWakeChannelsAreIndependent(t *testing.T) {
	s := &Service{}
	s.Wake()
	if len(s.wake) != 1 || len(s.cleanupWake) != 0 {
		t.Fatal("普通唤醒没有只通知调度器")
	}
	<-s.wake
	s.SettingsChanged()
	s.SettingsChanged()
	if len(s.wake) != 1 || len(s.cleanupWake) != 1 {
		t.Fatal("设置通知没有独立唤醒调度和清理，或没有合并重复通知")
	}
}
