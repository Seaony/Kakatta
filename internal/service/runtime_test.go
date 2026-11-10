package service

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/remote"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func runtimeCoreService(t *testing.T) *Service {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	prefs, err := appsettings.New(t.Context(), db.DB(), settings.Environment{TZ: "UTC"}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{db: db.DB(), settings: prefs, now: func() time.Time { return now }, pacer: pacer.New(nil), notifier: &runtimeCoreNotifier{}}
	s.initRuntime()
	t.Cleanup(func() { _ = s.Close() })
	return s
}

type runtimeCoreResource struct {
	entered chan struct{}
	release chan struct{}
	failure error
	calls   atomic.Int64
}

func (r *runtimeCoreResource) Runner() *web.Runner      { return nil }
func (r *runtimeCoreResource) Chrome() *browser.Session { return nil }
func (r *runtimeCoreResource) OpenRemote(context.Context) (*remote.Session, error) {
	return nil, errors.New("本测试不打开远程")
}
func (r *runtimeCoreResource) Close() error {
	r.calls.Add(1)
	if r.entered != nil {
		close(r.entered)
	}
	if r.release != nil {
		<-r.release
	}
	return r.failure
}

func runtimeCoreOpening(t *testing.T, s *Service) *sessionWork {
	t.Helper()
	cleanupExec(t, s.db, `INSERT INTO accounts(id,name,state) VALUES(1,'账号','active')`)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	at := s.now().UnixMilli()
	if err = saveSessionMarkerTx(t.Context(), tx, 1, sessionMarker{Version: 1, SessionID: "opening", StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	life, cancel := context.WithCancelCause(s.ctx)
	work := &sessionWork{View: SessionView{ID: "opening", AccountID: 1, Kind: SessionLogin, Phase: SessionOpening, StartedAt: &at}, Context: life, Cancel: cancel, OpenDone: make(chan struct{}), Closed: make(chan struct{})}
	s.mu.Lock()
	s.sessions[work.View.ID] = work
	s.mu.Unlock()
	return work
}

func TestRuntimeCloseWaitsForOpeningAndResourceCleanup(t *testing.T) {
	s := runtimeCoreService(t)
	work := runtimeCoreOpening(t, s)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.closeSessionWork(ctx, work) }()
	select {
	case <-work.Context.Done():
	case <-ctx.Done():
		t.Fatal("关闭没有先取消资源启动")
	}
	select {
	case err := <-done:
		t.Fatal("启动没返回就释放了资源", err)
	default:
	}
	resource := &runtimeCoreResource{entered: make(chan struct{}), release: make(chan struct{})}
	s.completeResourceOpen(work, resource)
	select {
	case <-resource.entered:
	case <-ctx.Done():
		t.Fatal("晚到资源没有被关闭")
	}
	if !cleanupHas(t, s.db, `SELECT count(*) FROM meta WHERE key='runtime.session.1'`) {
		t.Fatal("资源还没关完就删了保护标记")
	}
	if _, view := s.runtimeStatusSnapshot(); view.Active != 1 {
		t.Fatal("关闭过程中提前释放页面名额")
	}
	close(resource.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if cleanupHas(t, s.db, `SELECT count(*) FROM meta WHERE key='runtime.session.1'`) {
		t.Fatal("关闭完成未清保护标记")
	}
	if !cleanupHas(t, s.db, `SELECT count(*) FROM accounts WHERE last_run_ended_at=? AND next_gap_seconds>=180`, s.now().UnixMilli()) {
		t.Fatal("没有持久结算账号间隔")
	}
	if _, view := s.runtimeStatusSnapshot(); view.Active != 0 {
		t.Fatal("关闭完成未释放名额")
	}
	if resource.calls.Load() != 1 {
		t.Fatal("重复关闭资源")
	}
}

func TestRuntimeCloseFailureKeepsProtection(t *testing.T) {
	s := runtimeCoreService(t)
	work := runtimeCoreOpening(t, s)
	failure := errors.New("关闭未确认")
	s.completeResourceOpen(work, &runtimeCoreResource{failure: failure})
	if err := s.closeSessionWork(t.Context(), work); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if !cleanupHas(t, s.db, `SELECT count(*) FROM meta WHERE key='runtime.session.1'`) {
		t.Fatal("关闭失败清了保护")
	}
	if _, view := s.runtimeStatusSnapshot(); view.Active != 1 {
		t.Fatal("关闭失败仍释放名额")
	}
	if _, err := s.beginMutation(); err == nil {
		t.Fatal("关闭失败后继续接纳")
	}
}

func TestRuntimeMaintenanceWaitsAndSerializesScopes(t *testing.T) {
	s := runtimeCoreService(t)
	release, err := s.beginMutation()
	if err != nil {
		t.Fatal(err)
	}
	type barrierResult struct {
		resume func()
		err    error
	}
	ready := make(chan barrierResult, 1)
	go func() { resume, err := s.quiesce(t.Context()); ready <- barrierResult{resume, err} }()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := s.waitRuntime(ctx, func() bool { return s.maintenance == 1 }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.beginMutation(); err == nil {
		t.Fatal("维护期间仍接新写入")
	}
	select {
	case <-ready:
		t.Fatal("未等原写入结束")
	default:
	}
	release()
	result := <-ready
	if result.err != nil {
		t.Fatal(result.err)
	}
	blocked, cancelBlocked := context.WithCancel(t.Context())
	cancelBlocked()
	if _, err := s.quiesceAccount(blocked, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("局部维护绕过全局维护", err)
	}
	result.resume()
	result.resume()
	end, err := s.beginMutation()
	if err != nil {
		t.Fatal("幂等恢复失败", err)
	}
	end()
}

func TestRuntimeQueueRejectsStaleAndAlreadyRunning(t *testing.T) {
	s := runtimeCoreService(t)
	request := startRequest{TaskID: 1, AccountID: 1, Kind: SessionRun, Trigger: TriggerManual}
	if err := s.enqueueStart(request); err != nil {
		t.Fatal(err)
	}
	old := s.queuedStarts()[0]
	s.removeQueued(old)
	if err := s.enqueueStart(request); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.reserveStart(old, 1, s.now()); err == nil || ok {
		t.Fatal("旧队列快照重新启动", ok, err)
	}
	s.removeQueued(old)
	if len(s.queuedStarts()) != 1 {
		t.Fatal("旧请求移除了新请求")
	}
	s.removeQueued(s.queuedStarts()[0])
	s.mu.Lock()
	s.runs[1] = &runExecution{}
	s.mu.Unlock()
	err := s.enqueueStart(request)
	s.mu.Lock()
	delete(s.runs, 1)
	s.mu.Unlock()
	var business *ServiceError
	if !errors.As(err, &business) || business.Kind != ErrorConflict {
		t.Fatal("活动任务重复排队", err)
	}
}

func TestRuntimeSnapshotsAndInvalidScope(t *testing.T) {
	s := runtimeCoreService(t)
	at := s.now().UnixMilli()
	runID := int64(3)
	path := "/remote/original"
	s.mu.Lock()
	s.sessions["copy"] = &sessionWork{View: SessionView{ID: "copy", AccountID: 1, Phase: SessionQueued, StartedAt: &at, RunID: &runID, RemotePath: &path, Viewport: &remote.Viewport{Width: 20, Height: 30}}}
	s.mu.Unlock()
	_, first := s.runtimeStatusSnapshot()
	*first.Sessions[0].RunID = 99
	*first.Sessions[0].RemotePath = "changed"
	first.Sessions[0].Viewport.Width = 1
	second := s.sessionSnapshot(1)
	if *second.RunID != 3 || *second.RemotePath != "/remote/original" || second.Viewport.Width != 20 {
		t.Fatal("调用者改坏owner快照")
	}
	s.mu.Lock()
	delete(s.sessions, "copy")
	s.mu.Unlock()
	for _, id := range []int64{0, -1, 12768} {
		if _, err := s.quiesceAccount(t.Context(), id); err == nil {
			t.Fatal("非法账号被当全局范围", id)
		}
	}
}

func TestRuntimeMarkerCodecAndStorageCause(t *testing.T) {
	valid := sessionMarker{Version: 1, SessionID: "session", StartedAt: 1}
	raw, err := encodeSessionMarker(valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSessionMarker(raw); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{}`, `{"version":2,"session_id":"s","started_at":1}`, `{"version":1,"session_id":"s","started_at":1,"run_id":0}`, raw + ` {}`, strings.TrimSuffix(raw, "}") + `,"unknown":true}`} {
		if _, err := decodeSessionMarker(raw); err == nil {
			t.Fatal("损坏marker被接受", raw)
		}
	}
	cause := errors.New("敏感地址和密码")
	err = storageFailure(cause)
	var business *ServiceError
	if !errors.Is(err, cause) || !errors.As(err, &business) || business.Kind != ErrorUnavailable || strings.Contains(err.Error(), "密码") {
		t.Fatal("错误丢cause或泄漏内容", err)
	}
	if storageFailure(context.Canceled) != context.Canceled {
		t.Fatal("取消原因被覆盖")
	}
}

func TestRuntimeReadDoesNotAdvancePersistedClock(t *testing.T) {
	s, now, taskID := runStartFixture(t)
	plan := now.Add(4 * time.Hour).UnixMilli()
	runStartExec(t, s.db, `UPDATE tasks SET enabled=1,next_run_at=? WHERE id=?`, plan, taskID)
	runStartExec(t, s.db, `UPDATE accounts SET state='expired',last_run_ended_at=?,next_gap_seconds=180`, now.UnixMilli())
	readMeta := func() map[string]string {
		t.Helper()
		rows, err := s.db.QueryContext(t.Context(), `SELECT key,value FROM meta ORDER BY key`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		result := map[string]string{}
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				t.Fatal(err)
			}
			result[key] = value
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := readMeta()
	runStartExec(t, s.db, `CREATE TRIGGER runtime_clock_read_only BEFORE UPDATE ON meta WHEN OLD.key IN ('quota.clock','quota.clock_initialized') BEGIN SELECT RAISE(ABORT,'状态读取不能写时钟'); END`)
	*now = now.Add(2 * time.Hour)
	for i := 0; i < 2; i++ {
		view, err := s.GetTaskRuntime(t.Context(), taskID)
		if err != nil {
			t.Fatal(err)
		}
		if view.Phase != TaskIdle || view.Blocked == nil || view.Blocked.Reason.Code != "run.account_expired" || view.Blocked.NextAllowedAt != nil {
			t.Fatalf("需人处理的账号被显示为定时可运行：%+v", view)
		}
	}
	if after := readMeta(); !reflect.DeepEqual(before, after) {
		t.Fatalf("只读状态页修改meta：before=%v after=%v", before, after)
	}
	if runStartCount(t, s.db, `SELECT count(*) FROM run_records`) != 0 || len(s.queuedStarts()) != 0 {
		t.Fatal("只读状态页启动或记录了运行")
	}
}

func TestRuntimeRegistrationRechecksReducedPageLimit(t *testing.T) {
	s := runtimeCoreService(t)
	first, ok, err := s.reserveStart(startRequest{AccountID: 1, TaskID: 1, Kind: SessionRun}, 2, s.now())
	if err != nil || !ok {
		t.Fatal(first, ok, err)
	}
	second, ok, err := s.reserveStart(startRequest{AccountID: 2, TaskID: 2, Kind: SessionRun}, 2, s.now())
	if err != nil || !ok {
		t.Fatal(second, ok, err)
	}
	t.Cleanup(func() { s.releaseReservation(first.View.ID); s.releaseReservation(second.View.ID) })
	run := &runExecution{Ref: RunRef{TaskID: 2, RunID: 2}, Snapshot: settings.Snapshot{Values: settings.Values{"crawl.global_pages": []byte(`1`)}}}
	err = s.registerRun(run, second)
	var blocked *ServiceError
	if !errors.As(err, &blocked) || blocked.Kind != ErrorConflict || blocked.Reason.Code != "runtime.pages_busy" {
		t.Fatal("收紧页面上限后仍开始第二个资源", err)
	}
	if len(s.runs) != 0 || second.View.RunID != nil {
		t.Fatal("拒绝登记仍产生执行者")
	}
}

func TestRuntimeVerificationHeldUntilRequiresLiveResource(t *testing.T) {
	s := runtimeCoreService(t)
	at := s.now().Add(time.Minute).UnixMilli()
	work := &sessionWork{View: SessionView{ID: "held", AccountID: 1, Phase: SessionVerifyHold, ExpiresAt: &at}, Resource: &runtimeCoreResource{}}
	s.mu.Lock()
	s.sessions["held"] = work
	s.mu.Unlock()
	t.Cleanup(func() { s.mu.Lock(); delete(s.sessions, "held"); s.mu.Unlock() })
	for _, phase := range []SessionPhase{SessionVerifyHold, SessionVerifying} {
		work.View.Phase = phase
		got := s.VerificationHeldUntil(1, s.now().UnixMilli())
		if got == nil || *got != at {
			t.Fatal("真实验证资源未能证明可接手期限", phase, got)
		}
		*got = 1
		if *work.View.ExpiresAt != at {
			t.Fatal("返回值修改了owner期限")
		}
	}
	for _, change := range []func(){
		func() { work.View.Phase = SessionQueued },
		func() { work.Resource = nil },
		func() { work.Closing = true },
		func() { at = s.now().UnixMilli() },
	} {
		work.View.Phase, work.Resource, work.Closing = SessionVerifyHold, &runtimeCoreResource{}, false
		at = s.now().Add(time.Minute).UnixMilli()
		change()
		if got := s.VerificationHeldUntil(1, s.now().UnixMilli()); got != nil {
			t.Fatal("失效或无资源仍宣称可接手", got)
		}
	}
}

func TestRuntimeRiskURLTimeoutCannotExhaustPersistenceBudget(t *testing.T) {
	s := runtimeCoreService(t)
	work := runtimeCoreOpening(t, s)
	s.completeResourceOpen(work, &runtimeCoreResource{})
	ids, err := s.persistSessionRisk(work, web.ErrNeedVerify, func(ctx context.Context) (string, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Fatal("网址读取没有独立短预算")
		}
		<-ctx.Done()
		return "读取失败不能保存这个地址", ctx.Err()
	})
	if err != nil || len(ids) != 1 {
		t.Fatal("读取耗尽预算后未能持久保存风险", ids, err)
	}
	if !cleanupHas(t, s.db, `SELECT count(*) FROM accounts WHERE id=1 AND state='needs_verification' AND verify_url IS NULL`) {
		t.Fatal("缺少网址阻止了风险落盘，或保留了失败读取结果")
	}
	if s.runtimeErr != nil {
		t.Fatal("辅助网址读取失败被当成存储失败", s.runtimeErr)
	}
}

func TestRuntimeRiskStorageFailureCannotBeHiddenByResourceClose(t *testing.T) {
	for _, trigger := range []string{
		`CREATE TRIGGER risk_account_fail BEFORE UPDATE ON accounts BEGIN SELECT RAISE(ABORT,'账号保存失败'); END`,
		`CREATE TRIGGER risk_clock_fail BEFORE UPDATE ON meta WHEN OLD.key='quota.clock' BEGIN SELECT RAISE(ABORT,'时钟保存失败'); END`,
	} {
		t.Run(trigger, func(t *testing.T) {
			s := runtimeCoreService(t)
			work := runtimeCoreOpening(t, s)
			resource := &runtimeCoreResource{}
			s.completeResourceOpen(work, resource)
			cleanupExec(t, s.db, trigger)
			ids, err := s.persistSessionRisk(work, web.ErrDenied, func(context.Context) (string, error) { return "", context.Canceled })
			var failure *ServiceError
			if !errors.As(err, &failure) || failure.Kind != ErrorUnavailable || len(ids) != 0 {
				t.Fatal("风险保存失败未明确闭锁", ids, err)
			}
			if err = s.closeSessionWork(t.Context(), work); err == nil || resource.calls.Load() != 1 {
				t.Fatal("物理关闭没有执行，或掩盖风险保存失败", err, resource.calls.Load())
			}
			if !cleanupHas(t, s.db, `SELECT count(*) FROM meta WHERE key='runtime.session.1'`) || !cleanupHas(t, s.db, `SELECT count(*) FROM accounts WHERE id=1 AND state='active' AND last_run_ended_at IS NULL`) {
				t.Fatal("风险写入失败后仍结算并删除恢复保护，或未回滚状态")
			}
			if _, err := s.beginMutation(); err == nil {
				t.Fatal("风险未保存仍接纳下一次运行")
			}
		})
	}
}

func TestRuntimeCloseWaitsForKnownRiskPersistence(t *testing.T) {
	s := runtimeCoreService(t)
	work := runtimeCoreOpening(t, s)
	resource := &runtimeCoreResource{entered: make(chan struct{})}
	s.completeResourceOpen(work, resource)
	cleanupExec(t, s.db, `CREATE TRIGGER concurrent_risk_failure BEFORE UPDATE ON accounts BEGIN SELECT RAISE(ABORT,'账号保存失败'); END`)
	reading, release := make(chan struct{}), make(chan struct{})
	riskDone := make(chan error, 1)
	go func() {
		_, err := s.persistSessionRisk(work, web.ErrDenied, func(context.Context) (string, error) {
			close(reading)
			<-release
			return "", context.DeadlineExceeded
		})
		riskDone <- err
	}()
	<-reading
	closed := make(chan error, 1)
	go func() { closed <- s.closeSessionWork(t.Context(), work) }()
	<-resource.entered
	select {
	case err := <-closed:
		close(release)
		t.Fatal("风险保存未结束就结算了资源保护", err)
	default:
	}
	if !cleanupHas(t, s.db, `SELECT count(*) FROM meta WHERE key='runtime.session.1'`) {
		close(release)
		t.Fatal("风险保存期间删除了恢复标记")
	}
	close(release)
	if err := <-riskDone; err == nil {
		t.Fatal("注入的风险存储失败没有传播")
	}
	if err := <-closed; err == nil || !cleanupHas(t, s.db, `SELECT count(*) FROM meta WHERE key='runtime.session.1'`) {
		t.Fatal("关闭掩盖风险存储失败或清掉保护", err)
	}
}
