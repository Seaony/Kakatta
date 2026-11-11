package service

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/pacer"
)

type runtimeStorageCode int

func (code runtimeStorageCode) Code() int     { return int(code) }
func (code runtimeStorageCode) Error() string { return "不能泄露的数据库错误原文" }

func TestRuntimeFailureStopsHeldOpeningAndQueuedWork(t *testing.T) {
	s := runtimeCoreService(t)
	held := runtimeCoreOpening(t, s)
	blocked := make(chan struct{})
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(blocked) }) })
	resource := &runtimeCoreResource{entered: make(chan struct{}), release: blocked}
	s.completeResourceOpen(held, resource)
	s.mu.Lock()
	held.View.Phase = SessionVerifyHold
	s.mu.Unlock()
	cleanupExec(t, s.db, `INSERT INTO accounts(id,name,state) VALUES(2,'正在启动','active')`)
	at := s.now().UnixMilli()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveSessionMarkerTx(t.Context(), tx, 2, sessionMarker{Version: 1, SessionID: "late", StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	life, cancel := context.WithCancelCause(s.ctx)
	late := &sessionWork{Context: life, Cancel: cancel, OpenDone: make(chan struct{}), Closed: make(chan struct{}),
		View: SessionView{ID: "late", AccountID: 2, Kind: SessionRun, Phase: SessionOpening, StartedAt: &at}}
	s.mu.Lock()
	s.sessions["late"] = late
	s.mu.Unlock()
	lateResource := &runtimeCoreResource{entered: make(chan struct{})}
	var completeLate sync.Once
	t.Cleanup(func() { completeLate.Do(func() { s.completeResourceOpen(late, lateResource) }) })
	if err := s.enqueueStart(startRequest{TaskID: 9, AccountID: 1, Kind: SessionRun, Trigger: TriggerManual}); err != nil {
		t.Fatal(err)
	}
	runContext, cancelRun := context.WithCancelCause(s.ctx)
	run := &runExecution{Ref: RunRef{TaskID: 7}, Context: runContext, Cancel: cancelRun, Done: make(chan struct{})}
	s.mu.Lock()
	s.runs[7] = run
	s.jobs.Add(1)
	s.mu.Unlock()
	go func() { <-runContext.Done(); s.releaseRun(run) }()
	insightContext, releaseInsight, ok := s.claimInsight(8, 2)
	if !ok {
		t.Fatal("未接纳故障前的行情工作")
	}
	go func() { <-insightContext.Done(); releaseInsight() }()
	cleanupExec(t, s.db, `CREATE TRIGGER stop_account_writes BEFORE UPDATE ON accounts BEGIN SELECT RAISE(ABORT,'只供测试的存储失败'); END`)
	failure := storageFailure(errors.New("写入失败，不能泄露的凭据"))
	s.setRuntimeError(failure)
	ctx, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	for name, work := range map[string]context.Context{"验证保留": held.Context, "启动": late.Context, "运行": run.Context, "行情": insightContext, "系统通知": s.systemCtx} {
		select {
		case <-work.Done():
		case <-ctx.Done():
			t.Fatalf("存储故障没有取消%s工作", name)
		}
	}
	completeLate.Do(func() { s.completeResourceOpen(late, lateResource) })
	for name, entered := range map[string]<-chan struct{}{"验证保留": resource.entered, "晚到资源": lateResource.entered} {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatalf("%s未独立开始回收，可能被另一资源关闭阻塞", name)
		}
	}
	if len(s.queuedStarts()) != 0 {
		t.Fatal("存储故障后仍保留待启动请求")
	}
	if _, _, err := s.tryStartRun(ctx, startRequest{TaskID: 9}); err == nil {
		t.Fatal("存储故障后排队任务仍能启动")
	}
	unblock.Do(func() { close(blocked) })
	for _, work := range []*sessionWork{held, late} {
		select {
		case <-work.Closed:
		case <-ctx.Done():
			t.Fatal("存储故障后物理资源未完成回收")
		}
	}
	if !cleanupHas(t, s.db, `SELECT count(*) FROM meta WHERE key='runtime.session.1'`) || !cleanupHas(t, s.db, `SELECT count(*) FROM meta WHERE key='runtime.session.2'`) {
		t.Fatal("保护事务不可写时仍删除了恢复标记")
	}
	_, browsers := s.runtimeStatusSnapshot()
	if browsers.Active != 2 || resource.calls.Load() != 1 || lateResource.calls.Load() != 1 {
		t.Fatalf("未保留保护占位或重复关闭：%+v，关闭次数 %d/%d", browsers, resource.calls.Load(), lateResource.calls.Load())
	}
}

func TestRuntimeCloseFailureAlsoStopsOtherHeldSessions(t *testing.T) {
	s := runtimeCoreService(t)
	first := runtimeCoreOpening(t, s)
	s.completeResourceOpen(first, &runtimeCoreResource{failure: errors.New("资源无法确认关闭")})
	life, cancel := context.WithCancelCause(s.ctx)
	secondResource := &runtimeCoreResource{entered: make(chan struct{})}
	second := &sessionWork{Context: life, Cancel: cancel, OpenDone: make(chan struct{}), Closed: make(chan struct{}),
		View: SessionView{ID: "second", AccountID: 2, Kind: SessionVerification, Phase: SessionVerifyHold}}
	s.mu.Lock()
	s.sessions["second"] = second
	s.mu.Unlock()
	s.completeResourceOpen(second, secondResource)
	if err := s.closeSessionWork(t.Context(), first); err == nil {
		t.Fatal("未传播关闭故障")
	}
	select {
	case <-secondResource.entered:
	case <-time.After(time.Second):
		t.Fatal("一个会话关闭失败后，其它验证保留浏览器仍在运行")
	}
}

func TestRuntimeFailureConcurrentReservationReleaseDoesNotDeadlock(t *testing.T) {
	s := runtimeCoreService(t)
	work := runtimeCoreOpening(t, s)
	closing := make(chan error, 1)
	go func() { closing <- s.closeSessionWork(t.Context(), work) }()
	<-work.Context.Done()
	released := make(chan struct{})
	go func() { s.releaseReservation(work.View.ID); close(released) }()
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("资源启动失败的释放与故障关闭互相等待")
	}
	if err := <-closing; err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeFailureIsSafeStickyAndIndependentOfDatabase(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code string
	}{
		{"文件系统空间", syscall.ENOSPC, "runtime.storage_full"},
		{"数据库空间", runtimeStorageCode(13), "runtime.storage_full"},
		{"数据库忙", runtimeStorageCode(5), "runtime.storage_locked"},
		{"扩展锁错误", runtimeStorageCode(262), "runtime.storage_locked"},
		{"其他存储错误", errors.New("凭据不能进入响应"), "runtime.storage_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := runtimeCoreService(t)
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			s.setRuntimeError(storageFailure(test.err))
			s.setRuntimeError(nil)
			s.setRuntimeError(errors.New("后来的错误不能改首因"))
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			failure := s.RuntimeFailure()
			if failure == nil || failure.Code != test.code || len(failure.Args) != 0 {
				t.Fatalf("原因不安全或首因改变：%+v", failure)
			}
			failure.Code = "被修改"
			failure.Args["secret"] = "污染"
			if next := s.RuntimeFailure(); next.Code != test.code || len(next.Args) != 0 {
				t.Fatal("故障响应共享了可写对象", next)
			}
			for _, err := range []error{s.Health(ctx), func() error { _, err := s.GetSystemStatus(ctx); return err }()} {
				var unavailable *ServiceError
				if !errors.As(err, &unavailable) || unavailable.Kind != ErrorUnavailable || unavailable.Reason.Code != test.code || ctx.Err() != nil {
					t.Fatalf("故障状态依赖数据库连接或泄漏原文：%v，%v", err, ctx.Err())
				}
			}
		})
	}
}

func TestRuntimeHealthTracksPreparationAndDatabaseWithoutStarting(t *testing.T) {
	s := runtimeCoreService(t)
	if err := s.Health(t.Context()); err != nil || s.RuntimeFailure() != nil {
		t.Fatal("尚未启动被误报故障", err)
	}
	s.mu.Lock()
	s.prepareErr = storageFailure(runtimeStorageCode(13))
	s.mu.Unlock()
	var unavailable *ServiceError
	if err := s.Health(t.Context()); !errors.As(err, &unavailable) || unavailable.Reason.Code != "runtime.storage_full" {
		t.Fatal("准备写失败仍报告健康", err)
	}
	s.mu.Lock()
	s.prepareErr = nil
	s.mu.Unlock()
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Health(t.Context()); err == nil {
		t.Fatal("没有检查数据库连接")
	}
}

func TestRuntimeCloseJoinsFailureCleanup(t *testing.T) {
	s := runtimeCoreService(t)
	work := runtimeCoreOpening(t, s)
	release := make(chan struct{})
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(release) }) })
	resource := &runtimeCoreResource{entered: make(chan struct{}), release: release}
	s.completeResourceOpen(work, resource)
	s.setRuntimeError(storageFailure(runtimeStorageCode(13)))
	<-resource.entered
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case err := <-closed:
		t.Fatal("故障资源没关完就返回关闭", err)
	default:
	}
	unblock.Do(func() { close(release) })
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("故障回收和服务关闭互相等待")
	}
	if resource.calls.Load() != 1 {
		t.Fatal("服务关闭与故障回收重复关闭资源")
	}
}

func TestRuntimeFailureClosesAdmissionBeforePublishingSessionClosed(t *testing.T) {
	var admitted atomic.Int64
	for attempt := 0; attempt < 256; attempt++ {
		s := &Service{}
		s.initRuntime()
		life, cancel := context.WithCancelCause(s.ctx)
		work := &sessionWork{Context: life, Cancel: cancel, OpenDone: make(chan struct{}), Closed: make(chan struct{}),
			View: SessionView{ID: "failed", AccountID: 1, Kind: SessionLogin, Phase: SessionOpening}}
		s.sessions[work.View.ID] = work
		s.completeResourceOpen(work, &runtimeCoreResource{failure: errors.New("关闭失败")})
		var observers sync.WaitGroup
		for i := 0; i < 32; i++ {
			observers.Add(1)
			go func() {
				defer observers.Done()
				<-work.Closed
				if release, err := s.beginMutation(); err == nil {
					admitted.Add(1)
					release()
				}
			}()
		}
		_ = s.closeSessionWork(t.Context(), work)
		observers.Wait()
		_ = s.Close()
		if admitted.Load() != 0 {
			t.Fatalf("第%d次关闭失败发布后仍接纳了%d次写入", attempt+1, admitted.Load())
		}
	}
}

func TestRuntimeFailureBeforeCachedRegistrationCannotStartModel(t *testing.T) {
	ctx, s, findingID := reanalysisTestService(t)
	var calls atomic.Int64
	s.models = reanalysisModelFunc(func(context.Context, client.Request) (client.Result, error) {
		calls.Add(1)
		return client.Result{}, client.ErrNoProvider
	})
	_, work, err := s.prepareReanalysis(ctx, []int64{findingID})
	if err != nil || len(work) != 1 {
		t.Fatal(work, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	var stop sync.Once
	s.now = func() time.Time {
		stop.Do(func() { s.setRuntimeError(storageFailure(errors.New("登记前写入失败"))) })
		return time.UnixMilli(1000)
	}
	var unavailable *ServiceError
	err = s.reanalyzeCached(ctx, work[0])
	if !errors.As(err, &unavailable) || unavailable.Kind != ErrorUnavailable || calls.Load() != 0 {
		t.Fatalf("缓存登记前闭锁仍发模型：calls=%d err=%v", calls.Load(), err)
	}
}

func TestRuntimeFailureFromRawCloseErrorCannotFlushAnotherRun(t *testing.T) {
	for _, cause := range []error{errors.New("关闭错误中的私密信息"), context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			s, run, notifier, _ := finishFixture(t)
			run.Context, run.Cancel = context.WithCancelCause(s.ctx)
			s.mu.Lock()
			s.runs[run.Ref.TaskID] = run
			s.mu.Unlock()
			life, cancel := context.WithCancelCause(s.ctx)
			work := &sessionWork{Context: life, Cancel: cancel, OpenDone: make(chan struct{}), Closed: make(chan struct{}),
				View: SessionView{ID: "failed", AccountID: 2, Kind: SessionLogin, Phase: SessionOpening}}
			s.mu.Lock()
			s.sessions[work.View.ID] = work
			s.mu.Unlock()
			s.completeResourceOpen(work, &runtimeCoreResource{failure: cause})
			_ = s.closeSessionWork(t.Context(), work)
			failure := context.Cause(run.Context)
			err := s.finishRun(t.Context(), run, failure)
			if !runInfrastructureFailure(failure) || err == nil || notifier.calls != 0 || len(notifier.events) != 0 {
				t.Errorf("关闭故障取消被当成普通页面错误或仍推送：cause=%v err=%v calls=%d events=%v", failure, err, notifier.calls, notifier.events)
			}
			if got := finishScalar[string](t, s.db, `SELECT failure_kind FROM run_records WHERE id=1`); got != "config" {
				t.Errorf("关闭故障联动取消记成%s", got)
			}
			s.mu.Lock()
			delete(s.runs, run.Ref.TaskID)
			stopped := s.runtimeStopDone
			s.mu.Unlock()
			<-stopped
		})
	}
}

func TestRuntimeFailureOverridesEarlierCancellationBeforeFinishNotification(t *testing.T) {
	for _, timing := range []string{"用户已取消", "正常流程已取消上下文", "收尾事务中闭锁"} {
		t.Run(timing, func(t *testing.T) {
			s, run, notifier, now := finishFixture(t)
			run.Context, run.Cancel = context.WithCancelCause(s.ctx)
			s.mu.Lock()
			s.runs[run.Ref.TaskID] = run
			s.mu.Unlock()
			var stopCause error
			failure := storageFailure(errors.New("其他会话的存储故障"))
			switch timing {
			case "用户已取消":
				run.Cancel(errUserCancelled)
				stopCause = errUserCancelled
				s.setRuntimeError(failure)
			case "正常流程已取消上下文":
				run.Cancel(nil)
				s.setRuntimeError(failure)
			case "收尾事务中闭锁":
				var once sync.Once
				s.now = func() time.Time { once.Do(func() { s.setRuntimeError(failure) }); return now }
			}
			err := s.finishRun(t.Context(), run, stopCause)
			if err == nil || notifier.calls != 0 || len(notifier.events) != 0 {
				t.Errorf("既有取消或收尾事务掩盖全局闭锁：calls=%d events=%v err=%v", notifier.calls, notifier.events, err)
			}
			if got := finishScalar[string](t, s.db, `SELECT failure_kind FROM run_records WHERE id=1`); got != "config" {
				t.Errorf("全局故障仍记成%s", got)
			}
			s.mu.Lock()
			delete(s.runs, run.Ref.TaskID)
			stopped := s.runtimeStopDone
			s.mu.Unlock()
			<-stopped
		})
	}
}

func TestRuntimeContextSurvivesMaintenanceButStopsOnFailureAndClose(t *testing.T) {
	s := runtimeCoreService(t)
	life := s.RuntimeContext()
	resume, err := s.quiesceScope(t.Context(), 0, 0, 0, errUserCancelled)
	if err != nil {
		t.Fatal(err)
	}
	resume()
	if life != s.RuntimeContext() || life.Err() != nil {
		t.Fatal("普通维护取消或替换了全局停机上下文")
	}
	s.setRuntimeError(errors.New("存储不可用"))
	if life.Err() == nil || !runInfrastructureFailure(context.Cause(life)) {
		t.Fatal("闭锁未取消运行寿命或丢失基础设施类别", context.Cause(life))
	}
	s.setRuntimeError(nil)
	if s.RuntimeContext() != life || life.Err() == nil {
		t.Fatal("闭锁的寿命被重新打开")
	}
	other := runtimeCoreService(t)
	closed := other.RuntimeContext()
	if err := other.Close(); err != nil || closed.Err() == nil {
		t.Fatal("关闭没有取消运行寿命", err)
	}
}

func TestRuntimeFailureCancelsOutboundFinishButKeepsLocalSettlement(t *testing.T) {
	s, run, notifier, _ := finishFixture(t)
	entered := make(chan context.Context, 1)
	notifier.flush = func(ctx context.Context, _ RunRef, _ []int64) (NotifyResult, error) {
		entered <- ctx
		<-ctx.Done()
		return NotifyResult{}, ctx.Err()
	}
	finished := make(chan error, 1)
	go func() { finished <- s.finishRun(t.Context(), run, nil) }()
	notifyContext := <-entered
	s.setRuntimeError(errors.New("通知前后出现存储故障"))
	select {
	case <-notifyContext.Done():
	case <-time.After(time.Second):
		t.Fatal("收尾对外请求未随闭锁取消")
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("未返回运行故障")
		}
	case <-time.After(time.Second):
		t.Fatal("对外取消后收尾没有结束")
	}
	if got := finishScalar[string](t, s.db, `SELECT failure_kind FROM run_records WHERE id=1`); got != "config" {
		t.Fatal("收尾故障记录不正确", got)
	}
	if got := finishScalar[int64](t, s.db, `SELECT COUNT(*) FROM run_records WHERE id=1 AND ended_at IS NOT NULL`); got != 1 {
		t.Fatal("对外取消也取消了本地结算")
	}
	<-s.runtimeStopDone
}

func TestRuntimeFailureSynchronouslyCancelsFinishNotification(t *testing.T) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)
	s, run, notifier, _ := finishFixture(t)
	var cancelled bool
	notifier.flush = func(ctx context.Context, _ RunRef, _ []int64) (NotifyResult, error) {
		s.setRuntimeError(errors.New("派发前写入失败"))
		cancelled = ctx.Err() != nil
		return NotifyResult{}, ctx.Err()
	}
	_ = s.finishRun(t.Context(), run, nil)
	if !cancelled {
		t.Fatal("闭锁返回后通知上下文仍可外发，取消依赖异步回调")
	}
	<-s.runtimeStopDone
}

func TestRuntimeFailureStopsAutomaticCleanupStartedByService(t *testing.T) {
	deps, cache, _ := lifecycleDeps(t)
	var observed atomic.Int64
	observed.Store(deps.Now().UnixMilli())
	deps.Now = func() time.Time { return time.UnixMilli(observed.Load()) }
	clock := cleanupScheduleClock{waits: make(chan cleanupScheduledWait, 4)}
	deps.Pacer = pacer.New(clock)
	s, err := NewStopped(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	cleanupListing(t, deps.DB, "stopped-cleanup", 1, false, false)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	wait := cleanupScheduleWait(t, clock, 24*time.Hour)
	s.setRuntimeError(errors.New("停止自动维护"))
	select {
	case <-wait.stopped:
	case <-time.After(time.Second):
		t.Fatal("存储闭锁没有取消正式启动的清理等待")
	}
	stopped := make(chan struct{})
	go func() { s.background.Wait(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("取消等待后自动清理仍未退出")
	}
	pruned := cache.calls.Load()
	before := reanalysisClock(t, t.Context(), deps.DB)
	observed.Add(int64(25 * time.Hour / time.Millisecond))
	close(wait.elapsed)
	s.SettingsChanged()
	if cache.calls.Load() != pruned || !cleanupHas(t, deps.DB, `SELECT COUNT(*) FROM listings WHERE item_id='stopped-cleanup'`) {
		t.Fatal("停机后设置通知或到期计时仍触发了清理")
	}
	if after := reanalysisClock(t, t.Context(), deps.DB); after != before {
		t.Fatal("停机后清理写入了额度时钟")
	}
}
