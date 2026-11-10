package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/eventstore"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

type scheduleClock struct{ random float64 }

func (c scheduleClock) Sleep(context.Context, time.Duration) error { return nil }
func (c scheduleClock) Float64() float64                           { return c.random }

type scheduleWaitClock struct {
	requests chan time.Duration
	stopped  chan struct{}
}

func (c scheduleWaitClock) Sleep(ctx context.Context, d time.Duration) error {
	select {
	case c.requests <- d:
	case <-ctx.Done():
		return ctx.Err()
	}
	<-ctx.Done()
	select {
	case c.stopped <- struct{}{}:
	default:
	}
	return ctx.Err()
}
func (scheduleWaitClock) Float64() float64 { return 0 }

func scheduleTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestQuietUntilUsesRealTimezoneBoundaries(t *testing.T) {
	for _, test := range []struct{ name, zone, start, end, now, until string }{
		{"普通时段", "UTC", "01:00", "07:30", "2026-11-10T07:29:59Z", "2026-11-10T07:30:00Z"},
		{"结束时刻", "UTC", "01:00", "07:30", "2026-11-10T07:30:00Z", ""},
		{"时段以外", "UTC", "01:00", "07:30", "2026-11-10T12:00:00Z", ""},
		{"跨午夜前半段", "UTC", "23:00", "07:30", "2026-11-10T23:30:00Z", "2026-11-11T07:30:00Z"},
		{"跨午夜后半段", "UTC", "23:00", "07:30", "2026-11-11T00:30:00Z", "2026-11-11T07:30:00Z"},
		{"重复小时第一次", "America/New_York", "01:00", "01:30", "2026-11-01T05:15:00Z", "2026-11-01T05:30:00Z"},
		{"重复小时第二次", "America/New_York", "01:00", "01:30", "2026-11-01T06:15:00Z", "2026-11-01T06:30:00Z"},
		{"跨重复小时", "America/New_York", "00:00", "02:30", "2026-11-01T05:45:00Z", "2026-11-01T07:30:00Z"},
		{"结束时刻不存在", "America/New_York", "01:00", "02:30", "2026-03-08T06:30:00Z", "2026-03-08T07:00:00Z"},
		{"午夜被跳过", "America/Santiago", "23:00", "00:30", "2026-09-06T03:30:00Z", "2026-09-06T04:00:00Z"},
		{"重复午夜第一次", "America/Havana", "00:00", "00:30", "2025-11-02T04:15:00Z", "2025-11-02T04:30:00Z"},
		{"重复午夜第二次", "America/Havana", "00:00", "00:30", "2025-11-02T05:15:00Z", "2025-11-02T05:30:00Z"},
		{"整天被跳过", "Pacific/Apia", "23:00", "07:30", "2011-12-30T09:30:00Z", "2011-12-30T17:30:00Z"},
		{"未来闰年年末", "America/New_York", "11:00", "14:00", "2040-12-31T17:00:00Z", "2040-12-31T19:00:00Z"},
	} {
		t.Run(test.name, func(t *testing.T) {
			location, err := time.LoadLocation(test.zone)
			if err != nil {
				t.Fatal(err)
			}
			quiet := settings.QuietHours{Enabled: true, Start: test.start, End: test.end}
			got, err := quietUntil(scheduleTime(t, test.now), quiet, location)
			if err != nil {
				t.Fatal(err)
			}
			if test.until == "" {
				if got != nil {
					t.Fatalf("不在静默时段仍被阻止：%v", got)
				}
			} else if got == nil || !got.Equal(scheduleTime(t, test.until)) {
				t.Fatalf("静默结束时刻错误：got %v，want %s", got, test.until)
			}
		})
	}
}

func TestQuietUntilDisabledAndInvalidInputs(t *testing.T) {
	now := scheduleTime(t, "2026-11-10T03:00:00Z")
	for _, quiet := range []settings.QuietHours{
		{Enabled: false, Start: "01:00", End: "07:30"},
		{Enabled: true, Start: "03:00", End: "03:00"},
	} {
		if got, err := quietUntil(now, quiet, time.UTC); err != nil || got != nil {
			t.Fatalf("已关闭的静默时段仍拦住：%v %v", got, err)
		}
	}
	for _, quiet := range []settings.QuietHours{
		{Enabled: true, Start: "1:00", End: "07:30"},
		{Enabled: true, Start: "01:00", End: "24:00"},
	} {
		if _, err := quietUntil(now, quiet, time.UTC); err == nil {
			t.Fatal("非法静默时间没有报错", quiet)
		}
	}
	if _, err := quietUntil(now, settings.QuietHours{}, nil); err == nil {
		t.Fatal("缺少时区没有报错")
	}
}

func scheduleSnapshot(t *testing.T, timezone string, patch settings.Values) settings.Snapshot {
	t.Helper()
	snapshot, err := settings.Resolve(nil, settings.Environment{TZ: timezone})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = settings.Apply(snapshot, patch)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestNextRunAtUsesCurrentIntervalAndOverride(t *testing.T) {
	snapshot := scheduleSnapshot(t, "Asia/Shanghai", settings.Values{
		"crawl.quiet_hours": json.RawMessage(`{"enabled":false,"start":"01:00","end":"07:30"}`),
	})
	ended := scheduleTime(t, "2026-11-10T08:00:00Z")
	task := Task{TaskFields: TaskFields{IntervalMinutes: 30}}
	p := pacer.New(scheduleClock{random: 0.5})
	got, err := nextRunAt(task, snapshot, ended, p)
	if err != nil || !got.Equal(ended.Add(34*time.Minute+30*time.Second)) || got.Location() != time.UTC {
		t.Fatalf("没有按当前间隔只增抖动：%v %v", got, err)
	}
	task.CrawlOverrides = settings.Values{"interval_jitter_percent": json.RawMessage(`100`)}
	got, err = nextRunAt(task, snapshot, ended, p)
	if err != nil || !got.Equal(ended.Add(45*time.Minute)) {
		t.Fatalf("没有采用任务抖动覆盖：%v %v", got, err)
	}
}

func TestNextRunAtDefersQuietHoursOnce(t *testing.T) {
	snapshot := scheduleSnapshot(t, "UTC", settings.Values{"crawl.interval_jitter_percent": json.RawMessage(`0`)})
	task := Task{TaskFields: TaskFields{IntervalMinutes: 60}}
	got, err := nextRunAt(task, snapshot, scheduleTime(t, "2026-11-10T00:00:00Z"), pacer.New(scheduleClock{random: 0.5}))
	if err != nil || !got.Equal(scheduleTime(t, "2026-11-10T07:32:30Z")) {
		t.Fatalf("未推到静默结束后再增加一次随机等待：%v %v", got, err)
	}
	snapshot = scheduleSnapshot(t, "America/New_York", settings.Values{
		"crawl.interval_jitter_percent": json.RawMessage(`0`),
		"crawl.quiet_hours":             json.RawMessage(`{"enabled":true,"start":"01:00","end":"02:30"}`),
	})
	task.IntervalMinutes = 30
	got, err = nextRunAt(task, snapshot, scheduleTime(t, "2026-03-08T06:00:00Z"), pacer.New(scheduleClock{}))
	if err != nil || !got.Equal(scheduleTime(t, "2026-03-08T07:00:00Z")) {
		t.Fatalf("静默结束不存在时没有等到实际退出时刻：%v %v", got, err)
	}
}

func TestNextRunAtRejectsInvalidConfigurationAndDoesNotOverflow(t *testing.T) {
	snapshot := scheduleSnapshot(t, "UTC", settings.Values{
		"crawl.quiet_hours":             json.RawMessage(`{"enabled":false,"start":"01:00","end":"07:30"}`),
		"crawl.interval_jitter_percent": json.RawMessage(`0`),
	})
	ended := scheduleTime(t, "2026-11-10T08:00:00Z")
	p := pacer.New(scheduleClock{random: math.Nextafter(1, 0)})
	for _, interval := range []int64{0, math.MaxInt64/int64(time.Minute) + 1} {
		if _, err := nextRunAt(Task{TaskFields: TaskFields{IntervalMinutes: interval}}, snapshot, ended, p); err == nil {
			t.Fatal("接受了非法运行间隔", interval)
		}
	}
	task := Task{TaskFields: TaskFields{IntervalMinutes: math.MaxInt64 / int64(time.Minute)}}
	got, err := nextRunAt(task, snapshot, ended, p)
	if err != nil || !got.Equal(ended.Add(time.Duration(task.IntervalMinutes)*time.Minute)) {
		t.Fatalf("合法长间隔溢出：%v %v", got, err)
	}
	task.IntervalMinutes = 1
	percent := (int64(math.MaxInt64) - int64(time.Minute)) / int64(time.Minute) * 100
	task.CrawlOverrides = settings.Values{"interval_jitter_percent": json.RawMessage(strconv.FormatInt(percent, 10))}
	got, err = nextRunAt(task, snapshot, ended, p)
	if err != nil || got.Before(ended.Add(time.Minute)) || got.Sub(ended) < 0 {
		t.Fatalf("合法大抖动出现负等待：%v %v", got, err)
	}
}

func TestAutomaticReadyAtUsesLatestApplicableGate(t *testing.T) {
	now := scheduleTime(t, "2026-11-10T08:00:00Z")
	ms := func(d time.Duration) *int64 {
		at := now.Add(d).UnixMilli()
		return &at
	}
	gap := int64(4 * time.Hour / time.Second)
	start := preflightResult{
		Task: Task{NextRunAt: ms(time.Hour), FailurePausedUntil: ms(2 * time.Hour)},
		Account: Account{State: AccountCooling, LastRunEndedAt: ms(time.Hour), NextGapSeconds: &gap,
			CooldownUntil: ms(4 * time.Hour)},
		Now: now,
	}
	if got := automaticReadyAt(start); !got.Equal(now.Add(5 * time.Hour)) {
		t.Fatal("账号间隔没有参与实际开始时间", got)
	}
	start.Blocked = &RuntimeBlocked{NextAllowedAt: ms(6 * time.Hour)}
	if got := automaticReadyAt(start); !got.Equal(now.Add(6 * time.Hour)) {
		t.Fatal("额度等后续门禁没有参与实际开始时间", got)
	}
	start.NeedsUser = true
	start.Blocked.NextAllowedAt = nil
	if got := automaticReadyAt(start); !got.Equal(now.Add(2 * time.Hour)) {
		t.Fatal("用户处理条件仍应按自动计划和失败暂停到期处理", got)
	}
}

func TestSchedulerWaitUsesOneDeadlineAndJoinsCancellation(t *testing.T) {
	now := scheduleTime(t, "2026-11-10T08:00:00Z")
	clock := scheduleWaitClock{requests: make(chan time.Duration, 1), stopped: make(chan struct{}, 1)}
	s := &Service{now: func() time.Time { return now }, pacer: pacer.New(clock)}
	s.initRuntime()
	for _, cancelWait := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		next := now.Add(2 * time.Hour)
		done := make(chan bool, 1)
		go func() { done <- s.waitScheduler(ctx, &next) }()
		select {
		case got := <-clock.requests:
			if got != 2*time.Hour {
				t.Fatal("调度没有一次等到最早时刻", got)
			}
		case <-time.After(time.Second):
			t.Fatal("没有开始可取消的等待")
		}
		status, _ := s.runtimeStatusSnapshot()
		if status.NextWakeAt == nil || *status.NextWakeAt != next.UnixMilli() {
			t.Fatal("系统状态没有保存实际唤醒时间", status.NextWakeAt)
		}
		if cancelWait {
			cancel()
		} else {
			s.wakeScheduler()
		}
		select {
		case active := <-done:
			if active == cancelWait {
				t.Fatalf("等待退出状态错误：取消%t，继续%t", cancelWait, active)
			}
		case <-time.After(time.Second):
			t.Fatal("取消或提前唤醒没有结束等待")
		}
		select {
		case <-clock.stopped:
		default:
			t.Fatal("提前返回时没有等休眠退出")
		}
		cancel()
	}
}

func TestSchedulerWaitWithoutDeadlineOnlyUsesWake(t *testing.T) {
	s := &Service{pacer: pacer.New(scheduleClock{})}
	s.initRuntime()
	s.wakeScheduler()
	if !s.waitScheduler(t.Context(), nil) {
		t.Fatal("无时间门禁时未响应状态变化")
	}
	status, _ := s.runtimeStatusSnapshot()
	if status.NextWakeAt != nil {
		t.Fatal("无有限恢复时间仍编造定时唤醒")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if s.waitScheduler(ctx, nil) {
		t.Fatal("已取消的服务仍继续调度")
	}
}

type scheduleFailedClock struct {
	started chan struct{}
	err     error
}

func (c scheduleFailedClock) Sleep(ctx context.Context, _ time.Duration) error {
	close(c.started)
	<-ctx.Done()
	return c.err
}
func (scheduleFailedClock) Float64() float64 { return 0 }

func TestSchedulerWakeDoesNotHideWaitFailure(t *testing.T) {
	now := scheduleTime(t, "2026-11-10T08:00:00Z")
	want := errors.New("等待依赖失败")
	clock := scheduleFailedClock{started: make(chan struct{}), err: want}
	s := &Service{now: func() time.Time { return now }, pacer: pacer.New(clock)}
	s.initRuntime()
	next := now.Add(time.Hour)
	done := make(chan bool, 1)
	go func() { done <- s.waitScheduler(t.Context(), &next) }()
	<-clock.started
	s.wakeScheduler()
	if active := <-done; active {
		t.Fatal("等待依赖失败后仍继续调度")
	}
	s.mu.Lock()
	got := s.runtimeErr
	s.mu.Unlock()
	if !errors.Is(got, want) {
		t.Fatal("提前唤醒吞掉了等待错误", got)
	}
}

func TestDiscardedStartOnlyIncludesEntityRaces(t *testing.T) {
	for _, kind := range []ErrorKind{ErrorNotFound, ErrorConflict} {
		if !discardedStart(errors.Join(errors.New("并发动作"), &ServiceError{Kind: kind})) {
			t.Fatal("并发删除或取消被当成基础设施故障", kind)
		}
	}
	for _, err := range []error{errors.New("数据库关闭"), context.Canceled, &ServiceError{Kind: ErrorUnavailable}} {
		if discardedStart(err) {
			t.Fatal("基础设施故障被当成可忽略的请求", err)
		}
	}
}

func scheduleService(t *testing.T, now func() time.Time, patch settings.Values) *Service {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	settingStore, err := appsettings.New(t.Context(), db.DB(), settings.Environment{TZ: "UTC"}, now)
	if err != nil {
		t.Fatal(err)
	}
	defaults := settings.Values{
		"crawl.quiet_hours":             json.RawMessage(`{"enabled":false,"start":"01:00","end":"07:30"}`),
		"crawl.interval_jitter_percent": json.RawMessage(`0`),
	}
	for key, value := range patch {
		defaults[key] = value
	}
	if _, err := settingStore.Apply(t.Context(), defaults); err != nil {
		t.Fatal(err)
	}
	s := &Service{db: db.DB(), settings: settingStore, now: now, pacer: pacer.New(scheduleClock{}),
		events: eventstore.Store{DB: db.DB(), Now: now}}
	s.initRuntime()
	return s
}

func scheduleExec(t *testing.T, s *Service, query string, args ...any) {
	t.Helper()
	if _, err := s.db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(query, err)
	}
}

func scheduleCount(t *testing.T, s *Service, query string) int64 {
	t.Helper()
	var count int64
	if err := s.db.QueryRowContext(t.Context(), query).Scan(&count); err != nil {
		t.Fatal(query, err)
	}
	return count
}

func scheduleTask(t *testing.T, s *Service, accountID *int64, enabled bool, next int64) {
	t.Helper()
	scheduleExec(t, s, `INSERT INTO tasks(id,name,keyword,enabled,account_id,personal_only,max_pages,interval_minutes,judge_mode,notify_policy,sort_index,next_run_at)
		VALUES(1,'任务','手机',?,?,0,1,30,'keyword','none',0,?)`, enabled, accountID, next)
}

func TestScheduleOnceWaitsUntilEnoughEntryQuota(t *testing.T) {
	now := scheduleTime(t, "2026-11-10T08:00:00Z")
	s := scheduleService(t, func() time.Time { return now }, settings.Values{"crawl.account_hourly_visits": json.RawMessage(`3`)})
	scheduleExec(t, s, `INSERT INTO accounts(id,name,state) VALUES(1,'账号','active')`)
	accountID := int64(1)
	scheduleTask(t, s, &accountID, true, now.Add(20*time.Minute).UnixMilli())
	scheduleExec(t, s, `INSERT INTO quota_events(account_id,kind,at) VALUES(1,'search',?)`, now.UnixMilli())
	next, err := s.scheduleOnce(t.Context())
	if err != nil || next == nil || !next.Equal(now.Add(time.Hour)) {
		t.Fatalf("只按任务到期或一个空名额提前唤醒：%v %v", next, err)
	}
	if count := scheduleCount(t, s, `SELECT COUNT(*) FROM run_records`); count != 0 {
		t.Fatal("自动时间门禁写了运行记录", count)
	}
}

func TestScheduleOnceCapacityWaitHasNoInventedTimer(t *testing.T) {
	now := scheduleTime(t, "2026-11-10T08:00:00Z")
	s := scheduleService(t, func() time.Time { return now }, settings.Values{"crawl.account_hourly_visits": json.RawMessage(`2`)})
	scheduleExec(t, s, `INSERT INTO accounts(id,name,state) VALUES(1,'账号','active')`)
	accountID := int64(1)
	scheduleTask(t, s, &accountID, true, now.Add(20*time.Minute).UnixMilli())
	next, err := s.scheduleOnce(t.Context())
	if err != nil || next != nil {
		t.Fatalf("配置永远不够却编造了恢复时间：%v %v", next, err)
	}
	if count := scheduleCount(t, s, `SELECT COUNT(*) FROM run_records`); count != 0 {
		t.Fatal("自动容量等待被记成失败或跳过", count)
	}
}

func TestScheduleOnceUserSkipAdvancesPlanAndPreservesOccurrences(t *testing.T) {
	now := scheduleTime(t, "2026-11-10T08:00:00Z")
	s := scheduleService(t, func() time.Time { return now }, nil)
	scheduleTask(t, s, nil, true, now.Add(-time.Minute).UnixMilli())
	if _, err := s.scheduleOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	next, err := s.scheduleOnce(t.Context())
	if err != nil || next == nil || !next.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("用户处理跳过后没有使用新计划：%v %v", next, err)
	}
	if count := scheduleCount(t, s, `SELECT COUNT(*) FROM run_records`); count != 1 {
		t.Fatal("未来计划仍在重复写跳过", count)
	}
	now = now.Add(70 * time.Minute)
	if _, err := s.scheduleOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if count := scheduleCount(t, s, `SELECT COUNT(*) FROM run_records WHERE repeat_count=2`); count != 1 {
		t.Fatal("同一原因没有折叠为一条日志", count)
	}
	if count := scheduleCount(t, s, `SELECT COUNT(DISTINCT CAST(at/3600000 AS INTEGER)) FROM run_skip_events`); count != 2 {
		t.Fatal("折叠跳过丢失了逐次发生小时", count)
	}
}

func TestScheduleOnceRejectsChangedManualRequestWithoutChangingAutomaticPlan(t *testing.T) {
	now := scheduleTime(t, "2026-11-10T08:00:00Z")
	s := scheduleService(t, func() time.Time { return now }, nil)
	scheduleExec(t, s, `INSERT INTO accounts(id,name,state) VALUES(1,'账号','active')`)
	accountID := int64(1)
	plan := now.Add(time.Hour).UnixMilli()
	scheduleTask(t, s, &accountID, false, plan)
	if _, err := s.RunNow(t.Context(), 1); err != nil {
		t.Fatal("停用任务不能手动排队", err)
	}
	if _, err := s.settings.Apply(t.Context(), settings.Values{"crawl.account_hourly_visits": json.RawMessage(`2`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.scheduleOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(s.queuedStarts()) != 0 {
		t.Fatal("额度改变后仍把立即运行留在队列里")
	}
	if next := scheduleCount(t, s, `SELECT next_run_at FROM tasks WHERE id=1`); next != plan {
		t.Fatal("未开始的手动请求改变了自动计划", next, plan)
	}
	if count := scheduleCount(t, s, `SELECT COUNT(*) FROM run_records WHERE trigger='manual' AND outcome='skipped'`); count != 1 {
		t.Fatal("手动拒绝没有记录跳过", count)
	}
}

type scheduleFactory struct {
	opened  chan int64
	release chan struct{}
}

func (f *scheduleFactory) Open(ctx context.Context, spec ResourceSpec) (SessionResource, error) {
	f.opened <- spec.AccountID
	select {
	case <-f.release:
		return nil, errors.New("本地资源启动失败")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type scheduleNotifier struct{ flushed chan RunRef }

func (n scheduleNotifier) FlushRun(_ context.Context, ref RunRef, _ []int64) (NotifyResult, error) {
	n.flushed <- ref
	return NotifyResult{}, nil
}
func (scheduleNotifier) System(context.Context, int64) error {
	return errors.New("本测试不应发送系统通知")
}

func TestScheduleOnceManualPriorityAndBusyAccountDoNotBlockAnotherAccount(t *testing.T) {
	now := scheduleTime(t, "2026-11-10T08:00:00Z")
	s := scheduleService(t, func() time.Time { return now }, settings.Values{"crawl.global_pages": json.RawMessage(`2`)})
	s.dataDir = t.TempDir()
	factory := &scheduleFactory{opened: make(chan int64, 2), release: make(chan struct{})}
	flushed := make(chan RunRef, 2)
	s.resources, s.notifier = factory, scheduleNotifier{flushed: flushed}
	var release sync.Once
	unblock := func() { release.Do(func() { close(factory.release) }) }
	t.Cleanup(func() {
		unblock()
		s.jobs.Wait()
	})
	scheduleExec(t, s, `INSERT INTO accounts(id,name,state) VALUES(1,'账号一','active'),(2,'账号二','active')`)
	accountID := int64(1)
	scheduleTask(t, s, &accountID, false, now.Add(time.Hour).UnixMilli())
	for _, task := range []struct{ id, account int64 }{{2, 1}, {3, 2}} {
		scheduleExec(t, s, `INSERT INTO tasks(id,name,keyword,enabled,account_id,personal_only,max_pages,interval_minutes,judge_mode,notify_policy,sort_index,next_run_at)
			VALUES(?,'自动任务','手机',1,?,0,1,30,'keyword','none',0,?)`, task.id, task.account, now.Add(-time.Hour).UnixMilli())
	}
	if _, err := s.RunNow(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.scheduleOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	accounts := map[int64]bool{}
	for range 2 {
		select {
		case account := <-factory.opened:
			accounts[account] = true
		case <-time.After(time.Second):
			t.Fatal("忙账号堵住了后面的其他账号")
		}
	}
	if !accounts[1] || !accounts[2] {
		t.Fatal("没有按两个独立账号使用页面名额", accounts)
	}
	if count := scheduleCount(t, s, `SELECT COUNT(*) FROM run_records WHERE id=1 AND task_id=1 AND trigger='manual'`); count != 1 {
		t.Fatal("手动请求没有先于自动任务创建运行记录")
	}
	if count := scheduleCount(t, s, `SELECT COUNT(*) FROM run_records WHERE task_id=2`); count != 0 {
		t.Fatal("同账号已经有手动运行时又启动自动任务")
	}
	status, browsers := s.runtimeStatusSnapshot()
	if status.RunningCount != 2 || browsers.Active != 2 || len(s.queuedStarts()) != 0 {
		t.Fatal("资源占用或手动队列没有正确结算", status, browsers)
	}
	unblock()
	for range 2 {
		select {
		case <-flushed:
		case <-time.After(time.Second):
			t.Fatal("资源启动失败后没有进入真实收尾接点")
		}
	}
}

type scheduleAdvanceClock struct {
	requests chan time.Duration
	advance  chan struct{}
}

func (c scheduleAdvanceClock) Sleep(ctx context.Context, d time.Duration) error {
	c.requests <- d
	select {
	case <-c.advance:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (scheduleAdvanceClock) Float64() float64 { return 0 }

func TestSchedulerLoopExpiresAccountEvenWithoutTasks(t *testing.T) {
	initial := scheduleTime(t, "2026-11-10T08:00:00Z")
	var observed atomic.Int64
	observed.Store(initial.UnixMilli())
	now := func() time.Time { return time.UnixMilli(observed.Load()).UTC() }
	s := scheduleService(t, now, nil)
	until := initial.Add(90 * time.Minute)
	scheduleExec(t, s, `INSERT INTO accounts(id,name,state,cooldown_until,verify_level,cooldown_manual_used)
		VALUES(1,'账号','cooling',?,1,1)`, until.UnixMilli())
	var eventID int64
	if err := s.db.QueryRowContext(t.Context(), `INSERT INTO system_events(kind,subject_id,created_at)
		VALUES('account_verify',1,?) RETURNING id`, initial.UnixMilli()).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	received := make(chan int64, 1)
	var notified atomic.Int64
	s.notifier = &runtimeSystemNotifier{system: func(ctx context.Context, id int64) error {
		if notified.Add(1) != 1 || id != eventID {
			return errors.New("没有只消费本次账号验证事件")
		}
		var consumedID, subjectID int64
		var kind string
		if err := s.db.QueryRowContext(ctx, `UPDATE system_events SET notified_at=?
			WHERE id=? AND kind='account_verify' AND subject_id=1 AND resolved_at IS NULL AND notified_at IS NULL
			RETURNING id,kind,subject_id`, initial.UnixMilli(), id).Scan(&consumedID, &kind, &subjectID); err != nil {
			return err
		}
		if consumedID != eventID || kind != "account_verify" || subjectID != 1 {
			return errors.New("消费的待通知事件不正确")
		}
		received <- id
		return nil
	}}
	clock := scheduleAdvanceClock{requests: make(chan time.Duration, 1), advance: make(chan struct{}, 1)}
	s.pacer = pacer.New(clock)
	changed := make(chan struct{}, 1)
	s.publish = func(kind string, _ *locales.Message, _ json.RawMessage) error {
		if kind == "account.changed" {
			changed <- struct{}{}
		}
		return nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	s.schedulerStarted = true
	go func() { s.schedulerLoop(ctx); close(done) }()
	var stopped sync.Once
	stop := func() { stopped.Do(func() { cancel(); <-done; s.jobs.Wait() }) }
	t.Cleanup(stop)
	select {
	case delay := <-clock.requests:
		if delay != 90*time.Minute {
			t.Fatal("无任务时没有等到账号保护结束", delay)
		}
	case <-time.After(time.Second):
		t.Fatal("无任务时遗漏了账号到期唤醒")
	}
	select {
	case id := <-received:
		if id != eventID {
			t.Fatal("后台通知消费了错误事件", id, eventID)
		}
	case <-time.After(time.Second):
		t.Fatal("没有在解决事件之前消费真实待通知事件")
	}
	observed.Store(until.Add(time.Minute).UnixMilli())
	clock.advance <- struct{}{}
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("账号保护结束没有提交状态并发布变化")
	}
	stop()
	if got := notified.Load(); got != 1 {
		t.Fatal("系统事件没有恰好消费一次", got)
	}
	if count := scheduleCount(t, s, `SELECT COUNT(*) FROM system_events WHERE id=`+strconv.FormatInt(eventID, 10)+` AND notified_at=`+strconv.FormatInt(initial.UnixMilli(), 10)+` AND resolved_at IS NOT NULL`); count != 1 {
		t.Fatal("消费回执和解决状态没有落在同一真实事件上")
	}
	if count := scheduleCount(t, s, `SELECT COUNT(*) FROM accounts WHERE state='active' AND cooldown_manual_used=0 AND last_cooldown_end_at=`+strconv.FormatInt(until.UnixMilli(), 10)); count != 1 {
		t.Fatal("冷却结束时间或状态不正确")
	}
	if count := scheduleCount(t, s, `SELECT COUNT(*) FROM system_events WHERE resolved_at IS NOT NULL`); count != 1 {
		t.Fatal("保护结束没有解决原系统事件")
	}
}

func TestSchedulerLoopDoesNotWakeItselfForBusyResources(t *testing.T) {
	now := scheduleTime(t, "2026-11-10T08:00:00Z")
	s := scheduleService(t, func() time.Time { return now }, nil)
	scheduleExec(t, s, `INSERT INTO accounts(id,name,state) VALUES(1,'账号','active')`)
	accountID := int64(1)
	scheduleTask(t, s, &accountID, false, now.Add(time.Hour).UnixMilli())
	scheduleExec(t, s, `INSERT INTO tasks(id,name,keyword,enabled,personal_only,max_pages,interval_minutes,judge_mode,notify_policy,sort_index,next_run_at)
		VALUES(2,'未来任务','相机',1,0,1,30,'keyword','none',0,?)`, now.Add(time.Hour).UnixMilli())
	if _, err := s.RunNow(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.sessions["occupied"] = &sessionWork{View: SessionView{ID: "occupied", AccountID: 1, Kind: SessionRun, Phase: SessionRunning}}
	s.mu.Unlock()
	select {
	case <-s.wake:
	default:
	}
	clock := scheduleWaitClock{requests: make(chan time.Duration, 1), stopped: make(chan struct{}, 1)}
	s.pacer = pacer.New(clock)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	s.schedulerStarted = true
	go func() { s.schedulerLoop(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case delay := <-clock.requests:
		if delay != time.Hour {
			t.Fatal("没有等待真正的下一次时间门禁", delay)
		}
	case <-time.After(time.Second):
		t.Fatal("没有进入稳定等待")
	}
	select {
	case <-clock.stopped:
		t.Fatal("只因资源忙就自行唤醒，调度会持续空转")
	case <-time.After(100 * time.Millisecond):
	}
	if len(s.queuedStarts()) != 1 {
		t.Fatal("资源忙时丢掉了手动请求")
	}
}
