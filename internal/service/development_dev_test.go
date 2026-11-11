//go:build kakatta_dev

package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type developmentControl interface {
	SetDevelopmentSchedulerPaused(bool)
	DevelopmentSchedulerPaused() bool
	PrepareDevelopmentReload(context.Context) (func(), error)
}

func developmentController(t *testing.T, s *Service) developmentControl {
	t.Helper()
	control, ok := any(s).(developmentControl)
	if !ok {
		t.Fatal("开发编译缺少调度暂停和安全排空接口")
	}
	return control
}

func developmentStartBlockedRun(t *testing.T, s *Service, taskID int64) (*runStartFactory, *runExecution, context.Context, func()) {
	t.Helper()
	factory := &runStartFactory{opened: make(chan context.Context, 1), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(factory.release) })
	t.Cleanup(release)
	s.resources = factory
	if _, err := s.RunNow(t.Context(), taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.scheduleOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case resourceContext := <-factory.opened:
		s.mu.Lock()
		run := s.runs[taskID]
		s.mu.Unlock()
		if run == nil {
			t.Fatal("资源创建时没有已登记的运行")
		}
		return factory, run, resourceContext, release
	case <-time.After(3 * time.Second):
		t.Fatal("手动运行没有进入本地资源工厂")
		return nil, nil, nil, nil
	}
}

func TestDevelopmentSchedulerPauseStopsOnlyAutomaticRuns(t *testing.T) {
	s, _, taskID := runStartFixture(t)
	control := developmentController(t, s)
	control.SetDevelopmentSchedulerPaused(true)
	if !control.DevelopmentSchedulerPaused() {
		t.Fatal("没有保存开发调度暂停状态")
	}
	runStartExec(t, s.db, `UPDATE tasks SET enabled=1,next_run_at=0 WHERE id=?`, taskID)
	if _, err := s.scheduleOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if started, blocked, err := s.tryStartRun(t.Context(), startRequest{TaskID: taskID, Kind: SessionRun, Trigger: TriggerAuto}); err != nil || started || blocked != nil {
		t.Fatalf("暂停时自动入口没有直接停下：%t %+v %v", started, blocked, err)
	}
	if s.resources.(*runStartFactory).calls.Load() != 0 || s.models.(*runStartModels).calls.Load() != 0 ||
		s.notifier.(*runStartNotifier).calls.Load() != 0 || runStartCount(t, s.db, `SELECT count(*) FROM run_records`) != 0 ||
		runStartCount(t, s.db, `SELECT count(*) FROM quota_events`) != 0 {
		t.Fatal("开发暂停仍访问资源、模型、通知或消耗额度")
	}
	factory, run, resourceContext, release := developmentStartBlockedRun(t, s, taskID)
	if resourceContext.Err() != nil || run.Context.Err() != nil || factory.calls.Load() != 1 {
		t.Fatal("开发自动暂停阻止或取消了手动运行")
	}
	release()
	runStartWait(t, run.Done)
	if runStartCount(t, s.db, `SELECT count(*) FROM run_records WHERE trigger='manual' AND failure_kind='network' AND ended_at IS NOT NULL`) != 1 {
		t.Fatal("手动运行没有按原流程自然收尾")
	}
	control.SetDevelopmentSchedulerPaused(false)
	if control.DevelopmentSchedulerPaused() {
		t.Fatal("没有解除开发调度暂停")
	}
}

func TestDevelopmentReloadWaitsForRunWithoutCancellingAndBlocksAdmission(t *testing.T) {
	s, _, taskID := runStartFixture(t)
	control := developmentController(t, s)
	_, run, resourceContext, release := developmentStartBlockedRun(t, s, taskID)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	checked := make(chan error, 1)
	go func() {
		defer release()
		if err := s.waitRuntime(ctx, func() bool { return s.maintenance == 1 }); err != nil {
			checked <- err
			return
		}
		if _, err := s.RunNow(ctx, taskID); err == nil {
			checked <- errors.New("排空期间仍接纳手动运行")
			return
		}
		if _, err := s.OpenLogin(ctx, 1); err == nil {
			checked <- errors.New("排空期间仍接纳新登录")
			return
		}
		if run.Context.Err() != nil || resourceContext.Err() != nil {
			checked <- errors.New("排空取消了当前运行或资源创建")
			return
		}
		checked <- nil
	}()
	resume, err := control.PrepareDevelopmentReload(ctx)
	if resume != nil {
		defer resume()
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := <-checked; err != nil {
		t.Fatal(err)
	}
	select {
	case <-run.Done:
	default:
		t.Fatal("当前运行没结束就允许切换")
	}
	if runStartCount(t, s.db, `SELECT count(*) FROM run_records WHERE failure_kind='network' AND ended_at IS NOT NULL`) != 1 || s.sessionSnapshot(1) != nil {
		t.Fatal("排空改成取消，或没有等会话收尾")
	}
	if status, _ := s.runtimeStatusSnapshot(); status.Accepting {
		t.Fatal("排空成功后没有保留切换屏障")
	}
	resume()
	resume()
	end, err := s.beginMutation()
	if err != nil {
		t.Fatal("取消切换未恢复旧服务", err)
	}
	end()
}

func TestDevelopmentReloadTimeoutRestoresAdmissionAndKeepsRunAlive(t *testing.T) {
	s, _, taskID := runStartFixture(t)
	control := developmentController(t, s)
	control.SetDevelopmentSchedulerPaused(true)
	_, run, resourceContext, release := developmentStartBlockedRun(t, s, taskID)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	resume, err := control.PrepareDevelopmentReload(ctx)
	if resume != nil {
		defer resume()
	}
	if !errors.Is(err, context.DeadlineExceeded) || resume != nil {
		t.Fatalf("排空超时没有明确失败：%v", err)
	}
	if run.Context.Err() != nil || resourceContext.Err() != nil || runStartCount(t, s.db, `SELECT count(*) FROM run_records WHERE ended_at IS NOT NULL`) != 0 {
		t.Fatal("排空超时取消了旧运行")
	}
	end, err := s.beginMutation()
	if err != nil {
		t.Fatal("超时后旧服务没有恢复接纳", err)
	}
	end()
	if !control.DevelopmentSchedulerPaused() {
		t.Fatal("超时改变了原自动暂停状态")
	}
	release()
	runStartWait(t, run.Done)
	if runStartCount(t, s.db, `SELECT count(*) FROM run_records WHERE failure_kind='network' AND ended_at IS NOT NULL`) != 1 {
		t.Fatal("超时后的旧运行未按原流程结束")
	}
}

func TestDevelopmentReloadWaitsForOpeningAndLogin(t *testing.T) {
	s := runtimeCoreService(t)
	control := developmentController(t, s)
	work := runtimeCoreOpening(t, s)
	resource := &runtimeCoreResource{}
	for _, phase := range []SessionPhase{SessionOpening, SessionWaitingLogin} {
		if phase == SessionWaitingLogin {
			s.completeResourceOpen(work, resource)
			if err := s.setSessionPhase(work.View.ID, phase, nil); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
		resume, err := control.PrepareDevelopmentReload(ctx)
		cancel()
		if resume != nil {
			resume()
		}
		if !errors.Is(err, context.DeadlineExceeded) || work.Context.Err() != nil || resource.calls.Load() != 0 {
			t.Fatalf("%s尚未结束就被取消或回收：%v", phase, err)
		}
	}
	if err := s.closeSessionWork(t.Context(), work); err != nil {
		t.Fatal(err)
	}
}

func TestDevelopmentReloadClosesHeldAndQueuedSessionsWithProtection(t *testing.T) {
	s := runtimeCoreService(t)
	control := developmentController(t, s)
	work := runtimeCoreOpening(t, s)
	resource := &runtimeCoreResource{entered: make(chan struct{}), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(resource.release) })
	t.Cleanup(release)
	s.completeResourceOpen(work, resource)
	if err := s.setSessionPhase(work.View.ID, SessionVerifyHold, nil); err != nil {
		t.Fatal(err)
	}
	cleanupExec(t, s.db, `UPDATE accounts SET state='needs_verification',verify_level=2 WHERE id=1`)
	cleanupExec(t, s.db, `INSERT INTO accounts(id,name,state) VALUES(2,'排队账号','signed_out')`)
	if _, err := s.OpenLogin(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	checked := make(chan error, 1)
	go func() {
		defer release()
		select {
		case <-resource.entered:
		case <-ctx.Done():
			checked <- ctx.Err()
			return
		}
		if !cleanupHas(t, s.db, `SELECT count(*) FROM meta WHERE key='runtime.session.1'`) {
			checked <- errors.New("资源还在关闭就删除了恢复标记")
			return
		}
		if _, view := s.runtimeStatusSnapshot(); view.Active != 1 {
			checked <- fmt.Errorf("资源还在关闭就释放了页面名额：%d", view.Active)
			return
		}
		checked <- nil
	}()
	resume, err := control.PrepareDevelopmentReload(ctx)
	if resume != nil {
		defer resume()
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := <-checked; err != nil {
		t.Fatal(err)
	}
	if len(s.queuedStarts()) != 0 || s.sessionSnapshot(1) != nil || s.sessionSnapshot(2) != nil || resource.calls.Load() != 1 {
		t.Fatal("排空后还有排队或保留资源")
	}
	if cleanupHas(t, s.db, `SELECT count(*) FROM meta WHERE key='runtime.session.1'`) ||
		!cleanupHas(t, s.db, `SELECT count(*) FROM accounts WHERE id=1 AND state='needs_verification' AND verify_level=2 AND last_run_ended_at=? AND next_gap_seconds>=180`, s.now().UnixMilli()) {
		t.Fatal("排空没有正常结算账号间隔，或清掉了风控保护")
	}
}
