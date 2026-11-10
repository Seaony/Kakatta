package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/quota"
)

type lifecycleCache struct {
	ImageCache
	calls atomic.Int64
	prune func(context.Context, int64) (int64, error)
}

func (c *lifecycleCache) Prune(ctx context.Context, limit int64) (int64, error) {
	c.calls.Add(1)
	if c.prune != nil {
		return c.prune(ctx, limit)
	}
	return c.ImageCache.Prune(ctx, limit)
}

func lifecycleDeps(t *testing.T) (Dependencies, *lifecycleCache, *atomic.Int64) {
	t.Helper()
	deps := runtimeCoreDeps(t)
	cache := &lifecycleCache{ImageCache: deps.ImageCache}
	deps.ImageCache = cache
	var published atomic.Int64
	deps.Publish = func(string, *locales.Message, json.RawMessage) error { published.Add(1); return nil }
	return deps, cache, &published
}

func lifecycleWait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("生命周期操作没有退出")
	}
}

func lifecycleResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("生命周期操作没有返回")
		return nil
	}
}

func lifecycleBlocked(t *testing.T, s *Service) {
	t.Helper()
	status, _ := s.runtimeStatusSnapshot()
	if status.Running || status.Accepting {
		t.Fatal("尚未启动就开放了调度或接纳", status)
	}
	for _, action := range []func() error{
		func() error { _, err := s.RunNow(t.Context(), 1); return err },
		func() error { _, err := s.OpenLogin(t.Context(), 1); return err },
	} {
		var blocked *ServiceError
		if err := action(); !errors.As(err, &blocked) || blocked.Kind != ErrorUnavailable || blocked.Reason.Code != "runtime.closed" {
			t.Fatal("尚未启动仍接纳手动外发", err)
		}
	}
}

func lifecycleNoExternal(t *testing.T, deps Dependencies, published *atomic.Int64) {
	t.Helper()
	if deps.Resources.(*runtimeCoreFactory).calls.Load() != 0 || deps.Models.(*runtimeCoreModels).calls.Load() != 0 || deps.Notifier.(*runtimeCoreNotifier).calls.Load() != 0 || published.Load() != 0 {
		t.Fatal("准备期间调用了浏览器、模型、通知或发布")
	}
}

func TestLifecycleNewStoppedHasNoIOAndClosesWithoutStarting(t *testing.T) {
	deps, cache, published := lifecycleDeps(t)
	profile := filepath.Join(deps.DataDir, "browser", "1")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(profile, "kept")
	if err := os.WriteFile(kept, []byte("原目录"), 0600); err != nil {
		t.Fatal(err)
	}
	cleanupExec(t, deps.DB, `INSERT INTO meta(key,value) VALUES('account.cleanup.1','1')`)
	if err := deps.DB.Close(); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(deps.DataDir, "kakatta.db")
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewStopped(t.Context(), deps)
	if err != nil {
		t.Fatal("只构造内存不应访问已关闭的数据库", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	lifecycleBlocked(t, s)
	if err := s.Start(); err == nil {
		t.Fatal("没有准备就启动成功")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(dbPath)
	if err != nil || string(before) != string(after) {
		t.Fatal("构造或未启动关闭修改了数据库", err)
	}
	if _, err := os.Stat(kept); err != nil || cache.calls.Load() != 0 {
		t.Fatal("构造或未启动关闭清理了目录或图片", err)
	}
	lifecycleNoExternal(t, deps, published)
}

func TestLifecyclePrepareRestoresOnceWithoutActivating(t *testing.T) {
	deps, cache, published := lifecycleDeps(t)
	initial := deps.Now()
	protected := initial.Add(2 * time.Hour).UnixMilli()
	var observed atomic.Int64
	observed.Store(initial.UnixMilli())
	deps.Now = func() time.Time { return time.UnixMilli(observed.Load()) }
	var raw string
	if err := deps.DB.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var clock quota.ClockState
	if err := json.Unmarshal([]byte(raw), &clock); err != nil {
		t.Fatal(err)
	}
	clock.HighestUTC = protected
	encoded, _ := json.Marshal(clock)
	cleanupExec(t, deps.DB, `UPDATE meta SET value=? WHERE key=?`, string(encoded), appsettings.ClockKey)
	cleanupExec(t, deps.DB, `INSERT INTO accounts(id,name,state,verify_level,cooldown_manual_used,next_gap_seconds) VALUES(1,'保留风险','needs_verification',2,1,240)`)
	cleanupExec(t, deps.DB, `INSERT INTO run_records(account_id,task_name,trigger,started_at,outcome,failure_kind) VALUES(1,'未完成','manual',?,'failed','restart')`, initial.UnixMilli())
	tx, err := deps.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveSessionMarkerTx(t.Context(), tx, 1, sessionMarker{Version: 1, SessionID: "crashed", StartedAt: initial.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	cleanupExec(t, deps.DB, `INSERT INTO meta(key,value) VALUES('account.cleanup.2','1')`)
	profile := filepath.Join(deps.DataDir, "browser", "2")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	parked := filepath.Join(t.TempDir(), "old-data")
	if err := os.WriteFile(parked, []byte("停放的旧目录"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewStopped(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	results := make(chan error, 8)
	for range 8 {
		go func() { results <- s.Prepare(t.Context()) }()
	}
	for range 8 {
		if err := lifecycleResult(t, results); err != nil {
			t.Fatal(err)
		}
	}
	if cache.calls.Load() != 1 {
		t.Fatal("并发准备重复执行了本地清理", cache.calls.Load())
	}
	observed.Store(protected + time.Hour.Milliseconds())
	if err := s.Prepare(t.Context()); err != nil || cache.calls.Load() != 1 {
		t.Fatal("重复准备没有复用第一次结果", err)
	}
	if err := deps.DB.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey).Scan(&raw); err != nil || json.Unmarshal([]byte(raw), &clock) != nil || clock.HighestUTC != protected {
		t.Fatal("重复准备推进了已保护的时钟", err)
	}
	if !cleanupHas(t, deps.DB, `SELECT count(*) FROM accounts WHERE id=1 AND state='needs_verification' AND verify_level=2 AND cooldown_manual_used=1 AND last_run_ended_at=? AND next_gap_seconds=240`, protected) ||
		!cleanupHas(t, deps.DB, `SELECT count(*) FROM run_records WHERE failure_kind='restart' AND ended_at=?`, protected) ||
		!cleanupHas(t, deps.DB, `SELECT count(*) FROM meta WHERE key='stats.started_at' AND value=?`, strconv.FormatInt(protected, 10)) ||
		cleanupHas(t, deps.DB, `SELECT count(*) FROM meta WHERE key IN ('runtime.session.1','account.cleanup.2')`) {
		t.Fatal("准备没有保留保护时钟、风险或原间隔规则")
	}
	if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("准备遗漏了账号目录恢复清理", err)
	}
	if data, err := os.ReadFile(parked); err != nil || string(data) != "停放的旧目录" {
		t.Fatal("准备触碰了停放的旧目录", err)
	}
	lifecycleBlocked(t, s)
	lifecycleNoExternal(t, deps, published)
}

func TestLifecyclePrepareKeepsWarningsWithoutPublishing(t *testing.T) {
	deps, cache, published := lifecycleDeps(t)
	cache.prune = func(context.Context, int64) (int64, error) { return 0, errors.New("本地图片删除失败") }
	s, err := NewStopped(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	warnings := s.maintenanceWarningsSnapshot()
	if len(warnings) != 1 || warnings[0].Code != "maintenance.image_cleanup_failed" {
		t.Fatal("准备丢了非致命的本地清理警告", warnings)
	}
	lifecycleBlocked(t, s)
	lifecycleNoExternal(t, deps, published)
}

func TestLifecyclePrepareFailureIsStickyAndCannotStart(t *testing.T) {
	for _, failure := range []string{
		`CREATE TRIGGER lifecycle_clock_fail BEFORE UPDATE ON meta WHEN OLD.key='quota.clock' BEGIN SELECT RAISE(ABORT,'准备失败'); END`,
		`CREATE TRIGGER lifecycle_profile_marker_fail BEFORE DELETE ON meta WHEN OLD.key='account.cleanup.2' BEGIN SELECT RAISE(ABORT,'准备失败'); END`,
		`CREATE TRIGGER lifecycle_cleanup_fail BEFORE DELETE ON listings BEGIN SELECT RAISE(ABORT,'准备失败'); END`,
	} {
		t.Run(failure, func(t *testing.T) {
			deps, cache, published := lifecycleDeps(t)
			cleanupExec(t, deps.DB, `INSERT INTO meta(key,value) VALUES('account.cleanup.2','1')`)
			cleanupListing(t, deps.DB, "expired", 1, false, false)
			cleanupExec(t, deps.DB, failure)
			s, err := NewStopped(t.Context(), deps)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			first := s.Prepare(t.Context())
			if first == nil || s.Prepare(t.Context()) != first || s.Start() == nil || cache.calls.Load() != 0 {
				t.Fatal("失败准备重做了、假成功或开放了启动", first)
			}
			lifecycleBlocked(t, s)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			lifecycleNoExternal(t, deps, published)
		})
	}
}

func TestLifecycleCanceledPrepareDoesNotRetry(t *testing.T) {
	deps, cache, published := lifecycleDeps(t)
	s, err := NewStopped(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Prepare(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := s.Prepare(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatal("取消后同一对象又重做准备", err)
	}
	if cache.calls.Load() != 0 || cleanupHas(t, deps.DB, `SELECT count(*) FROM meta WHERE key='stats.started_at'`) || s.Start() == nil {
		t.Fatal("已取消的准备仍做了本地初始化或启动")
	}
	lifecycleNoExternal(t, deps, published)
}

func TestLifecycleCloseCancelsAndJoinsPreparation(t *testing.T) {
	for _, cancelGeneration := range []bool{false, true} {
		t.Run(map[bool]string{false: "调用取消", true: "运行代取消"}[cancelGeneration], func(t *testing.T) {
			deps, cache, published := lifecycleDeps(t)
			life, cancelLife := context.WithCancel(t.Context())
			defer cancelLife()
			call, cancelCall := context.WithCancel(t.Context())
			defer cancelCall()
			entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			cache.prune = func(ctx context.Context, _ int64) (int64, error) {
				close(entered)
				<-ctx.Done()
				close(canceled)
				<-release
				return 0, ctx.Err()
			}
			s, err := NewStopped(life, deps)
			if err != nil {
				t.Fatal(err)
			}
			prepared, preparationReturned := make(chan error, 1), make(chan struct{})
			go func() { defer close(preparationReturned); prepared <- s.Prepare(call) }()
			t.Cleanup(func() {
				cancelCall()
				cancelLife()
				releaseOnce.Do(func() { close(release) })
				lifecycleWait(t, preparationReturned)
				_ = s.Close()
			})
			lifecycleWait(t, entered)
			waiter, cancelWaiter := context.WithCancel(t.Context())
			cancelWaiter()
			if err := s.Prepare(waiter); !errors.Is(err, context.Canceled) {
				t.Fatal("准备等待者取消没有及时返回", err)
			}
			select {
			case <-canceled:
				t.Fatal("等待者取消了真正的准备工作")
			default:
			}
			if cancelGeneration {
				cancelLife()
			} else {
				cancelCall()
			}
			lifecycleWait(t, canceled)
			closed := make(chan error, 1)
			go func() { closed <- s.Close() }()
			waiting, stopWaiting := context.WithTimeout(t.Context(), time.Second)
			defer stopWaiting()
			if err := s.waitRuntime(waiting, func() bool { return s.closing }); err != nil {
				t.Fatal("关闭没有开始", err)
			}
			select {
			case err := <-closed:
				t.Fatal("准备还没退出就结束关闭", err)
			case <-time.After(20 * time.Millisecond):
			}
			releaseOnce.Do(func() { close(release) })
			if err := lifecycleResult(t, prepared); !errors.Is(err, context.Canceled) {
				t.Fatal("本地清理的警告掩盖了准备取消", err)
			}
			if err := lifecycleResult(t, closed); err != nil || s.Start() == nil || cache.calls.Load() != 1 {
				t.Fatal("关闭没有完整等待准备，或关闭后又启动", err)
			}
			lifecycleNoExternal(t, deps, published)
		})
	}
}

func TestLifecycleConcurrentStartAndParentCancellation(t *testing.T) {
	deps, cache, published := lifecycleDeps(t)
	life, cancel := context.WithCancel(t.Context())
	defer cancel()
	s, err := NewStopped(life, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = s.Close() })
	prepareCall, cancelPrepare := context.WithCancel(t.Context())
	if err := s.Prepare(prepareCall); err != nil {
		t.Fatal(err)
	}
	cancelPrepare()
	lifecycleBlocked(t, s)
	started := make(chan error, 16)
	for range 16 {
		go func() { started <- s.Start() }()
	}
	for range 16 {
		if err := lifecycleResult(t, started); err != nil {
			t.Fatal(err)
		}
	}
	status, _ := s.runtimeStatusSnapshot()
	if !status.Running || !status.Accepting || cache.calls.Load() != 1 {
		t.Fatal("启动没有开放门禁或重做了准备", status, cache.calls.Load())
	}
	if err := s.emit("lifecycle.test", map[string]any{}); err != nil || published.Load() != 1 {
		t.Fatal("启动后仍未开放发布", err)
	}
	cancel()
	lifecycleWait(t, s.closeDone)
	if err := s.Close(); err != nil || s.Start() == nil {
		t.Fatal("父取消没有关闭或关闭后仍可重启", err)
	}
}

func TestLifecycleClosePreparedWithoutStart(t *testing.T) {
	deps, cache, published := lifecycleDeps(t)
	s, err := NewStopped(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	if err := lifecycleResult(t, closed); err != nil || cache.calls.Load() != 1 {
		t.Fatal("未启动关闭等待了不存在的循环或重做清理", err)
	}
	lifecycleNoExternal(t, deps, published)
}

func TestLifecycleStartAndCloseRaceDoesNotLeaveLoops(t *testing.T) {
	for range 8 {
		deps, cache, _ := lifecycleDeps(t)
		s, err := NewStopped(t.Context(), deps)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		if err := s.Prepare(t.Context()); err != nil {
			t.Fatal(err)
		}
		gate, started, closed := make(chan struct{}), make(chan error, 1), make(chan error, 1)
		go func() { <-gate; started <- s.Start() }()
		go func() { <-gate; closed <- s.Close() }()
		close(gate)
		if err := lifecycleResult(t, started); err != nil && !errors.Is(err, errServiceClosed) && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if err := lifecycleResult(t, closed); err != nil || cache.calls.Load() != 1 {
			t.Fatal("启动关闭竞争没有完成收尾", err)
		}
		status, _ := s.runtimeStatusSnapshot()
		if status.Running || status.Accepting || s.Start() == nil {
			t.Fatal("关闭返回后仍有运行循环或重新启动")
		}
	}
}

func TestLifecycleNewFailureDoesNotWaitForItself(t *testing.T) {
	deps, cache, published := lifecycleDeps(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	cache.prune = func(ctx context.Context, _ int64) (int64, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return 0, ctx.Err()
	}
	returned := make(chan error, 1)
	go func() {
		s, err := New(ctx, deps)
		if s != nil {
			_ = s.Close()
			returned <- errors.New("取消的准备仍返回了服务")
			return
		}
		returned <- err
	}()
	lifecycleWait(t, entered)
	cancel()
	lifecycleWait(t, canceled)
	releaseOnce.Do(func() { close(release) })
	if err := lifecycleResult(t, returned); !errors.Is(err, context.Canceled) {
		t.Fatal("三步包装失败没有关闭返回", err)
	}
	lifecycleNoExternal(t, deps, published)
}
