package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/imagecache"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

type runtimeCoreFactory struct{ calls atomic.Int64 }

func (f *runtimeCoreFactory) Open(context.Context, ResourceSpec) (SessionResource, error) {
	f.calls.Add(1)
	return nil, errors.New("本测试不应启动浏览器")
}

type runtimeCoreModels struct{ calls atomic.Int64 }

func (m *runtimeCoreModels) Complete(context.Context, client.Request) (client.Result, error) {
	m.calls.Add(1)
	return client.Result{}, errors.New("本测试不应请求模型")
}

type runtimeCoreNotifier struct{ calls atomic.Int64 }

func (n *runtimeCoreNotifier) FlushRun(context.Context, RunRef, []int64) (NotifyResult, error) {
	n.calls.Add(1)
	return NotifyResult{}, errors.New("本测试不应通知商品")
}
func (n *runtimeCoreNotifier) System(context.Context, int64) error {
	n.calls.Add(1)
	return errors.New("本测试不应推送事件")
}

func runtimeCoreDeps(t *testing.T) Dependencies {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(dir, "kakatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) }
	prefs, err := appsettings.New(t.Context(), db.DB(), settings.Environment{TZ: "UTC"}, now)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	cache, err := imagecache.New(filepath.Join(dir, "cache", "images"))
	if err != nil {
		t.Fatal(err)
	}
	return Dependencies{DB: db.DB(), Settings: prefs, Cipher: cipher, DataDir: dir, Resources: &runtimeCoreFactory{}, Models: &runtimeCoreModels{}, Notifier: &runtimeCoreNotifier{}, ImageCache: cache, Now: now, Publish: func(string, *locales.Message, json.RawMessage) error { return nil }}
}

func TestServiceNewRejectsAbsentAndTypedNilDependencies(t *testing.T) {
	base := runtimeCoreDeps(t)
	for _, test := range []struct {
		name   string
		remove func(*Dependencies)
	}{
		{"factory", func(d *Dependencies) { d.Resources = nil }},
		{"models", func(d *Dependencies) { d.Models = nil }},
		{"typed_models", func(d *Dependencies) { var model *runtimeCoreModels; d.Models = model }},
		{"notifier", func(d *Dependencies) { d.Notifier = nil }},
		{"cache", func(d *Dependencies) { d.ImageCache = nil }},
		{"typed_cache", func(d *Dependencies) { var cache *imagecache.Cache; d.ImageCache = cache }},
		{"publish", func(d *Dependencies) { d.Publish = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			deps := base
			test.remove(&deps)
			s, err := New(t.Context(), deps)
			if s != nil {
				_ = s.Close()
			}
			if err == nil {
				t.Fatal("没有真实依赖仍成功启动")
			}
		})
	}
}

func TestServiceNewRecoversBeforeAcceptingAndCloseJoins(t *testing.T) {
	deps := runtimeCoreDeps(t)
	cleanupExec(t, deps.DB, `INSERT INTO accounts(id,name,state,verify_level,cooldown_manual_used,next_gap_seconds) VALUES(1,'账号','needs_verification',2,1,240)`)
	tx, err := deps.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = saveSessionMarkerTx(t.Context(), tx, 1, sessionMarker{Version: 1, SessionID: "crashed", StartedAt: deps.Now().Add(-time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(t.Context())
	s, err := New(parent, deps)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = s.Close() })
	if cleanupHas(t, deps.DB, `SELECT count(*) FROM meta WHERE key='runtime.session.1'`) {
		t.Fatal("开放接纳前未完成崩溃恢复")
	}
	if !cleanupHas(t, deps.DB, `SELECT count(*) FROM accounts WHERE state='needs_verification' AND verify_level=2 AND cooldown_manual_used=1 AND last_run_ended_at=? AND next_gap_seconds=240`, deps.Now().UnixMilli()) {
		t.Fatal("恢复清了风险或缩短间隔")
	}
	if !cleanupHas(t, deps.DB, `SELECT count(*) FROM meta WHERE key='stats.started_at' AND CAST(value AS INTEGER)>0`) {
		t.Fatal("没初始化统计起点")
	}
	cancel()
	select {
	case <-s.closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("服务寿命取消未关闭并join")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if deps.Resources.(*runtimeCoreFactory).calls.Load() != 0 || deps.Models.(*runtimeCoreModels).calls.Load() != 0 || deps.Notifier.(*runtimeCoreNotifier).calls.Load() != 0 {
		t.Fatal("空任务启动访问了外部依赖")
	}
	if status, _ := s.runtimeStatusSnapshot(); status.Running || status.Accepting {
		t.Fatal("Close后仍显示运行/接纳")
	}
}

type runtimeSystemNotifier struct {
	system func(context.Context, int64) error
	quiet  func(context.Context) (func(), error)
}

func (*runtimeSystemNotifier) FlushRun(context.Context, RunRef, []int64) (NotifyResult, error) {
	return NotifyResult{}, errors.New("本测试不发送商品通知")
}
func (n *runtimeSystemNotifier) System(ctx context.Context, id int64) error {
	return n.system(ctx, id)
}
func (n *runtimeSystemNotifier) Quiesce(ctx context.Context) (func(), error) {
	if n.quiet != nil {
		return n.quiet(ctx)
	}
	return quiesceTestNotifier(ctx)
}

func TestRuntimeSystemRetryFiltersAndDoesNotWakeScheduler(t *testing.T) {
	s := runtimeCoreService(t)
	now := s.now().UnixMilli()
	day := int64(24 * time.Hour / time.Millisecond)
	for _, row := range []struct {
		id, count                 int64
		first, resolved, notified any
	}{
		{1, 0, nil, nil, nil},
		{2, 1, now - day, nil, nil},
		{3, 2, now - 1, nil, nil},
		{4, 3, now - 1, nil, nil},
		{5, 1, now - day - 1, nil, nil},
		{6, 0, nil, now, nil},
		{7, 0, nil, nil, now},
	} {
		cleanupExec(t, s.db, `INSERT INTO system_events(id,kind,subject_id,message,created_at,failure_count,first_failed_at,resolved_at,notified_at) VALUES(?,'account_verify',?,'{"code":"run.no_account","args":{}}',?,?,?,?,?)`, row.id, row.id, now, row.count, row.first, row.resolved, row.notified)
	}
	calls := make(chan int64, 10)
	s.notifier = &runtimeSystemNotifier{system: func(_ context.Context, id int64) error { calls <- id; return nil }}
	s.retrySystemEventsAsync(now)
	s.jobs.Wait()
	close(calls)
	got := []int64{}
	for id := range calls {
		got = append(got, id)
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	if !reflect.DeepEqual(got, []int64{1, 2, 3}) {
		t.Fatal("通知重试窗口或次数筛选错误", got)
	}
	select {
	case <-s.wake:
		t.Fatal("通知重试完成自行唤醒调度，可能形成重试空转")
	default:
	}
}

func TestRuntimeGlobalMaintenanceCancelsAndJoinsSystemRetry(t *testing.T) {
	s := runtimeCoreService(t)
	cleanupExec(t, s.db, `INSERT INTO system_events(kind,subject_id,message,created_at) VALUES('account_verify',1,'{"code":"run.no_account","args":{}}',?)`, s.now().UnixMilli())
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var settled, quietCalled atomic.Bool
	s.notifier = &runtimeSystemNotifier{
		system: func(ctx context.Context, _ int64) error {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
			settled.Store(true)
			return ctx.Err()
		},
		quiet: func(ctx context.Context) (func(), error) {
			if !settled.Load() {
				t.Error("运行通知回执未结算就进入通知维护")
			}
			quietCalled.Store(true)
			return quiesceTestNotifier(ctx)
		},
	}
	returned := make(chan struct{})
	go func() { s.retrySystemEventsAsync(s.now().UnixMilli()); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("调度被通知网络等待阻塞")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("没有开始后台重试")
	}
	type result struct {
		resume func()
		err    error
	}
	done := make(chan result, 1)
	go func() { resume, err := s.quiesce(t.Context()); done <- result{resume, err} }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("全局维护没有取消后台通知")
	}
	select {
	case <-done:
		close(release)
		t.Fatal("后台回执没结算就返回维护成功")
	default:
	}
	close(release)
	got := <-done
	if got.err != nil || got.resume == nil || !settled.Load() || !quietCalled.Load() {
		t.Fatal("没有完整等待并接入通知维护", got.err)
	}
	got.resume()
	got.resume()
	if releaseMutation, err := s.beginMutation(); err != nil {
		t.Fatal("恢复后仍不接纳", err)
	} else {
		releaseMutation()
	}
	if s.runtimeErr != nil {
		t.Fatal("维护取消被错误地记成存储失败", s.runtimeErr)
	}
}

func TestRuntimeSystemRetryStorageErrorClosesAdmission(t *testing.T) {
	s := runtimeCoreService(t)
	cleanupExec(t, s.db, `INSERT INTO system_events(kind,subject_id,message,created_at) VALUES('account_verify',1,'{"code":"run.no_account","args":{}}',?)`, s.now().UnixMilli())
	cause := errors.New("回执无法持久保存")
	s.notifier = &runtimeSystemNotifier{system: func(context.Context, int64) error { return cause }}
	s.retrySystemEventsAsync(s.now().UnixMilli())
	s.jobs.Wait()
	if !errors.Is(s.runtimeErr, cause) {
		t.Fatal("丢失后台通知失败原因", s.runtimeErr)
	}
	if _, err := s.beginMutation(); err == nil {
		t.Fatal("通知持久化错误仍继续接纳业务")
	}
}
