package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

type runStartClock struct{}

func (runStartClock) Sleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }
func (runStartClock) Float64() float64                                 { return 0 }

type runStartModels struct{ calls atomic.Int64 }

func (m *runStartModels) Complete(context.Context, client.Request) (client.Result, error) {
	m.calls.Add(1)
	return client.Result{}, client.ErrNoProvider
}

type runStartNotifier struct {
	calls    atomic.Int64
	canceled atomic.Bool
}

func (n *runStartNotifier) FlushRun(ctx context.Context, _ RunRef, _ []int64) (NotifyResult, error) {
	n.calls.Add(1)
	n.canceled.Store(ctx.Err() != nil)
	return NotifyResult{Warnings: []locales.Message{}}, ctx.Err()
}
func (n *runStartNotifier) System(context.Context, int64) error {
	return errors.New("本回归没有系统通知目标")
}

type runStartFactory struct {
	calls   atomic.Int64
	opened  chan context.Context
	release chan struct{}
}

func (f *runStartFactory) Open(ctx context.Context, _ ResourceSpec) (SessionResource, error) {
	f.calls.Add(1)
	if f.opened != nil {
		f.opened <- ctx
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, errors.New("本地资源启动失败")
}

func runStartFixture(t *testing.T) (*Service, *time.Time, int64) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(dir, "run.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Date(2030, 11, 11, 8, 0, 0, 0, time.UTC)
	preferences, err := appsettings.New(t.Context(), db.DB(), settings.Environment{TZ: "UTC"}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{db: db.DB(), settings: preferences, dataDir: dir, now: func() time.Time { return now },
		pacer: pacer.New(runStartClock{}), resources: &runStartFactory{}, models: &runStartModels{}, notifier: &runStartNotifier{}}
	s.initRuntime()
	t.Cleanup(func() { s.cancel(errServiceClosed); s.jobs.Wait() })
	runStartExec(t, s.db, `INSERT INTO accounts(id,name,state) VALUES(1,'本地账号','active')`)
	result, err := s.db.ExecContext(t.Context(), `INSERT INTO tasks(name,keyword,enabled,account_id,max_pages,interval_minutes,judge_mode,notify_policy,sort_index) VALUES('相机','相机',0,1,1,30,'keyword','recommended',0)`)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return s, &now, id
}

func runStartExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(query, err)
	}
}

func runStartCount(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := db.QueryRowContext(t.Context(), query, args...).Scan(&count); err != nil {
		t.Fatal(query, err)
	}
	return count
}

func runStartWait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("运行没有结束")
	}
}

func TestRunNowKeepsServiceLifetimeAndCommitsBeforeOpening(t *testing.T) {
	s, now, taskID := runStartFixture(t)
	factory := &runStartFactory{opened: make(chan context.Context, 1), release: make(chan struct{})}
	s.resources = factory
	plan := now.Add(time.Hour).UnixMilli()
	runStartExec(t, s.db, `UPDATE tasks SET next_run_at=?,failure_paused_until=? WHERE id=?`, plan, plan, taskID)
	requestContext, cancel := context.WithCancel(t.Context())
	submission, err := s.RunNow(requestContext, taskID)
	if err != nil || submission.Status != SubmissionQueued || submission.RunID != nil {
		t.Fatalf("手动运行没有入队：%+v %v", submission, err)
	}
	cancel()
	queue := s.queuedStarts()
	if len(queue) != 1 || queue[0].SessionID == "" || runStartCount(t, s.db, `SELECT count(*) FROM run_records`) != 0 {
		t.Fatal("排队不应预造运行记录，或队列身份丢失")
	}
	started, blocked, err := s.tryStartRun(t.Context(), queue[0])
	if err != nil || !started || blocked != nil {
		t.Fatalf("手动运行被停用或失败暂停挡住：%t %+v %v", started, blocked, err)
	}
	var resourceContext context.Context
	select {
	case resourceContext = <-factory.opened:
	case <-time.After(3 * time.Second):
		t.Fatal("资源没有开始创建")
	}
	if resourceContext.Err() != nil {
		t.Fatal("HTTP 请求结束取消了已接纳资源")
	}
	if runStartCount(t, s.db, `SELECT count(*) FROM run_records WHERE ended_at IS NULL AND outcome='failed' AND failure_kind='restart'`) != 1 ||
		runStartCount(t, s.db, `SELECT count(*) FROM meta WHERE key='runtime.session.1'`) != 1 {
		t.Fatal("资源启动前没有提交运行占位和恢复标记")
	}
	s.mu.Lock()
	run := s.runs[taskID]
	s.mu.Unlock()
	close(factory.release)
	runStartWait(t, run.Done)
	if runStartCount(t, s.db, `SELECT count(*) FROM run_records WHERE outcome='failed' AND failure_kind='network' AND ended_at IS NOT NULL`) != 1 {
		t.Fatal("启动失败没有按普通网络失败结束")
	}
	if runStartCount(t, s.db, `SELECT count(*) FROM meta WHERE key='runtime.session.1'`) != 0 ||
		runStartCount(t, s.db, `SELECT count(*) FROM accounts WHERE last_run_ended_at=? AND next_gap_seconds=180`, now.UnixMilli()) != 1 {
		t.Fatal("启动失败没有结算账号间隔和恢复标记")
	}
	if runStartCount(t, s.db, `SELECT enabled FROM tasks WHERE id=?`, taskID) != 0 {
		t.Fatal("手动运行改变了任务启用开关")
	}
	notifier := s.notifier.(*runStartNotifier)
	if notifier.calls.Load() != 1 || notifier.canceled.Load() || s.models.(*runStartModels).calls.Load() != 0 {
		t.Fatal("资源失败未用有效收尾上下文，或仍继续调用了模型")
	}
}

func TestRunNowBlockedLogsWithoutChangingAutomaticPlan(t *testing.T) {
	for _, test := range []struct{ name, mutation, reason string }{
		{"无账号", `UPDATE tasks SET account_id=NULL`, "run.no_account"},
		{"登录失效", `UPDATE accounts SET state='expired'`, "run.account_expired"},
		{"需要验证", `UPDATE accounts SET state='needs_verification'`, "run.account_needs_verification"},
		{"账号间隔", `UPDATE accounts SET last_run_ended_at=?,next_gap_seconds=180`, "run.account_gap"},
		{"直接拒绝", `UPDATE accounts SET state='denied',cooldown_until=?`, "run.account_denied"},
		{"冷却机会已用", `UPDATE accounts SET state='cooling',cooldown_until=?,cooldown_manual_used=1`, "run.account_cooling"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, now, taskID := runStartFixture(t)
			plan := now.Add(5 * time.Hour).UnixMilli()
			runStartExec(t, s.db, `UPDATE tasks SET next_run_at=?,consecutive_failures=2 WHERE id=?`, plan, taskID)
			if strings.Contains(test.mutation, "?") {
				at := now.Add(time.Hour).UnixMilli()
				if test.reason == "run.account_gap" {
					at = now.UnixMilli()
				}
				runStartExec(t, s.db, test.mutation, at)
			} else {
				runStartExec(t, s.db, test.mutation)
			}
			_, err := s.RunNow(t.Context(), taskID)
			var blocked *ServiceError
			if !errors.As(err, &blocked) || blocked.Reason.Code != test.reason {
				t.Fatalf("错误的前置拒绝：%v", err)
			}
			if len(s.queuedStarts()) != 0 || runStartCount(t, s.db, `SELECT count(*) FROM run_records WHERE outcome='skipped'`) != 1 {
				t.Fatal("非资源门禁仍然入队，或没有写跳过日志")
			}
			if runStartCount(t, s.db, `SELECT next_run_at FROM tasks WHERE id=?`, taskID) != plan ||
				runStartCount(t, s.db, `SELECT consecutive_failures FROM tasks WHERE id=?`, taskID) != 2 {
				t.Fatal("未开始的手动运行改变了自动计划或失败次数")
			}
		})
	}
}

func TestQueuedRunRechecksQuotaBeforeStarting(t *testing.T) {
	s, now, taskID := runStartFixture(t)
	if _, err := s.RunNow(t.Context(), taskID); err != nil {
		t.Fatal(err)
	}
	request := s.queuedStarts()[0]
	for i := 0; i < 30; i++ {
		runStartExec(t, s.db, `INSERT INTO quota_events(account_id,kind,at) VALUES(1,'detail',?)`, now.UnixMilli())
	}
	started, blocked, err := s.tryStartRun(t.Context(), request)
	if err != nil || started || blocked == nil || blocked.Reason.Code != "run.hourly_quota" {
		t.Fatalf("排队后额度已满仍然开始：%t %+v %v", started, blocked, err)
	}
	if s.resources.(*runStartFactory).calls.Load() != 0 || runStartCount(t, s.db, `SELECT count(*) FROM meta WHERE key LIKE 'runtime.session.%'`) != 0 {
		t.Fatal("重检拒绝仍创建资源或恢复标记")
	}
	if runStartCount(t, s.db, `SELECT count(*) FROM run_skip_events`) != 1 {
		t.Fatal("排队请求失效没有记逐次跳过时间")
	}
}

func TestRunStartRechecksPageLimitBeforeCreatingRun(t *testing.T) {
	for _, trigger := range []RunTrigger{TriggerManual, TriggerAuto} {
		t.Run(string(trigger), func(t *testing.T) {
			s, now, taskID := runStartFixture(t)
			if _, err := s.settings.Apply(t.Context(), settings.Values{"crawl.global_pages": json.RawMessage(`2`)}); err != nil {
				t.Fatal(err)
			}
			runStartExec(t, s.db, `INSERT INTO accounts(id,name,state) VALUES(2,'另一个账号','active')`)
			occupied, reserved, err := s.reserveStart(startRequest{AccountID: 2, Kind: SessionLogin}, 2, *now)
			if err != nil || !reserved {
				t.Fatalf("不能准备已有页面占用：%t %v", reserved, err)
			}
			defer s.releaseReservation(occupied.View.ID)
			request := startRequest{TaskID: taskID, Kind: SessionRun, Trigger: trigger}
			if trigger == TriggerManual {
				runStartExec(t, s.db, `UPDATE accounts SET state='cooling',cooldown_until=? WHERE id=1`, now.Add(time.Hour).UnixMilli())
				if _, err := s.RunNow(t.Context(), taskID); err != nil {
					t.Fatal(err)
				}
				request = s.queuedStarts()[0]
				defer s.releaseReservation(request.SessionID)
			} else {
				runStartExec(t, s.db, `UPDATE tasks SET enabled=1`)
			}
			observed := *now
			firstNow, releaseNow := make(chan struct{}), make(chan struct{})
			var clockCalls atomic.Int64
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			s.now = func() time.Time {
				if clockCalls.Add(1) == 1 {
					close(firstNow)
					select {
					case <-releaseNow:
					case <-ctx.Done():
					}
				}
				return observed
			}
			type startResult struct {
				started bool
				blocked *RuntimeBlocked
				err     error
			}
			finished := make(chan startResult, 1)
			go func() {
				started, blocked, err := s.tryStartRun(ctx, request)
				finished <- startResult{started, blocked, err}
			}()
			runStartWait(t, firstNow)
			s.mu.Lock()
			func() {
				defer s.mu.Unlock()
				close(releaseNow)
				// 唯一连接归还后，首个事务已提交；预留此时仍被锁挡住。
				var one int
				if err := s.db.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.ExecContext(ctx, `UPDATE settings SET value_json='1' WHERE key='crawl.global_pages'`); err != nil {
					t.Fatal(err)
				}
			}()
			select {
			case result := <-finished:
				if result.started || result.blocked != nil || result.err != nil {
					t.Errorf("收紧上限应继续等资源：%t %+v %v", result.started, result.blocked, result.err)
				}
			case <-ctx.Done():
				t.Fatal("页面上限重检没有结束", ctx.Err())
			}
			if count := runStartCount(t, s.db, `SELECT count(*) FROM run_records`); count != 0 {
				t.Errorf("尚无页面名额却创建了运行记录：%d", count)
			}
			if count := runStartCount(t, s.db, `SELECT count(*) FROM meta WHERE key LIKE 'runtime.session.%'`); count != 0 {
				t.Errorf("尚无页面名额却创建了恢复标记：%d", count)
			}
			if count := runStartCount(t, s.db, `SELECT count(*) FROM accounts WHERE id=1 AND (last_used_at IS NOT NULL OR last_run_ended_at IS NOT NULL OR next_gap_seconds IS NOT NULL OR cooldown_manual_used<>0)`); count != 0 {
				t.Error("未开始运行却消耗了手动机会或生成账号间隔")
			}
			if s.resources.(*runStartFactory).calls.Load() != 0 {
				t.Error("页面上限已满仍启动了资源")
			}
			queue := s.queuedStarts()
			if trigger == TriggerManual {
				if len(queue) != 1 || queue[0] != request {
					t.Errorf("资源忙丢掉或替换了原排队请求：%+v", queue)
				}
				s.mu.Lock()
				session := s.sessions[request.SessionID]
				valid := session != nil && session.View.Phase == SessionQueued && session.View.StartedAt == nil && session.Context.Err() == nil
				s.mu.Unlock()
				if !valid {
					t.Error("排队会话未恢复，或等待上下文已被取消")
				}
			} else if len(queue) != 0 || s.sessionSnapshot(1) != nil {
				t.Error("自动运行忙时没有释放临时名额")
			}
		})
	}
}

func TestCancelRunWaitsForOpeningAndCannotTreatZeroAsAllTasks(t *testing.T) {
	s, _, taskID := runStartFixture(t)
	if _, err := s.RunNow(t.Context(), taskID); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []int64{0, -1, taskID + 100} {
		var failure *ServiceError
		if err := s.CancelRun(t.Context(), invalid); !errors.As(err, &failure) || failure.Kind != ErrorNotFound {
			t.Fatalf("未知任务没有明确拒绝：%d %v", invalid, err)
		}
		if len(s.queuedStarts()) != 1 {
			t.Fatal("无效任务编号取消了其他任务")
		}
	}
	factory := &runStartFactory{opened: make(chan context.Context, 1), release: make(chan struct{})}
	s.resources = factory
	runStartExec(t, s.db, `UPDATE tasks SET consecutive_failures=2`)
	if started, blocked, err := s.tryStartRun(t.Context(), s.queuedStarts()[0]); err != nil || !started || blocked != nil {
		t.Fatalf("不能启动本地资源回归：%t %+v %v", started, blocked, err)
	}
	select {
	case <-factory.opened:
	case <-time.After(3 * time.Second):
		t.Fatal("资源没有开始")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := s.CancelRun(ctx, taskID); err != nil {
		t.Fatal("取消没有等待资源与持久收尾", err)
	}
	if runStartCount(t, s.db, `SELECT count(*) FROM run_records WHERE failure_kind='cancelled' AND ended_at IS NOT NULL`) != 1 ||
		runStartCount(t, s.db, `SELECT consecutive_failures FROM tasks WHERE id=?`, taskID) != 2 ||
		runStartCount(t, s.db, `SELECT count(*) FROM meta WHERE key LIKE 'runtime.session.%'`) != 0 {
		t.Fatal("取消被算作普通失败，或返回时资源标记尚未结算")
	}
	if len(s.queuedStarts()) != 0 || s.sessionSnapshot(1) != nil || s.taskRuntimeSnapshot(taskID).Phase != TaskIdle {
		t.Fatal("取消返回时仍残留队列、资源或执行者")
	}
}

func TestQuietAndImpossibleCapacityNeverBecomeDelayedManualRuns(t *testing.T) {
	for _, capacity := range []bool{false, true} {
		t.Run(map[bool]string{false: "静默时段", true: "容量不足"}[capacity], func(t *testing.T) {
			s, now, taskID := runStartFixture(t)
			code := "run.quiet_hours"
			if capacity {
				code = "run.quota_capacity"
				if _, err := s.settings.Apply(t.Context(), settings.Values{"crawl.account_hourly_visits": json.RawMessage(`3`)}); err != nil {
					t.Fatal(err)
				}
			} else {
				*now = now.AddDate(0, 0, 1).Add(-6 * time.Hour)
			}
			_, err := s.RunNow(t.Context(), taskID)
			var failure *ServiceError
			if !errors.As(err, &failure) || failure.Reason.Code != code || (failure.NextAllowedAt == nil) != capacity {
				t.Fatalf("错误的手动运行门禁：%v", err)
			}
			runStartExec(t, s.db, `UPDATE tasks SET enabled=1,next_run_at=0`)
			started, blocked, err := s.tryStartRun(t.Context(), startRequest{TaskID: taskID, Kind: SessionRun, Trigger: TriggerAuto})
			if err != nil || started || blocked == nil || blocked.Reason.Code != code ||
				runStartCount(t, s.db, `SELECT count(*) FROM run_skip_events`) != 1 || len(s.queuedStarts()) != 0 {
				t.Fatalf("自动等待误记新skip，或手动请求留下延迟执行：%t %+v %v", started, blocked, err)
			}
		})
	}
}

func TestRunAdmissionStorageFailureRollsBackSkippedRecord(t *testing.T) {
	s, now, taskID := runStartFixture(t)
	plan := now.Add(time.Hour).UnixMilli()
	runStartExec(t, s.db, `UPDATE tasks SET account_id=NULL,next_run_at=?`, plan)
	runStartExec(t, s.db, `CREATE TRIGGER reject_clock BEFORE UPDATE ON meta WHEN NEW.key='quota.clock' BEGIN SELECT RAISE(FAIL,'blocked'); END`)
	_, err := s.RunNow(t.Context(), taskID)
	var failure *ServiceError
	if !errors.As(err, &failure) || failure.Kind != ErrorUnavailable {
		t.Fatalf("存储错误被当作普通前置跳过：%v", err)
	}
	if runStartCount(t, s.db, `SELECT count(*) FROM run_records`) != 0 || runStartCount(t, s.db, `SELECT count(*) FROM run_skip_events`) != 0 ||
		runStartCount(t, s.db, `SELECT next_run_at FROM tasks WHERE id=?`, taskID) != plan || len(s.queuedStarts()) != 0 {
		t.Fatal("时钟提交失败仍保存了部分跳过记录或计划")
	}
}

func TestRunStartTransactionFailureRollsBackManualOpportunity(t *testing.T) {
	for _, trigger := range []string{
		`CREATE TRIGGER reject_marker BEFORE INSERT ON meta WHEN NEW.key LIKE 'runtime.session.%' BEGIN SELECT RAISE(FAIL,'blocked'); END`,
		`CREATE TRIGGER reject_manual BEFORE UPDATE OF cooldown_manual_used ON accounts BEGIN SELECT RAISE(FAIL,'blocked'); END`,
	} {
		t.Run(trigger, func(t *testing.T) {
			s, now, taskID := runStartFixture(t)
			runStartExec(t, s.db, `UPDATE accounts SET state='cooling',cooldown_until=?`, now.Add(time.Hour).UnixMilli())
			if _, err := s.RunNow(t.Context(), taskID); err != nil {
				t.Fatal(err)
			}
			runStartExec(t, s.db, trigger)
			started, blocked, err := s.tryStartRun(t.Context(), s.queuedStarts()[0])
			var failure *ServiceError
			if started || blocked != nil || !errors.As(err, &failure) || failure.Kind != ErrorUnavailable {
				t.Fatalf("开始事务失败没有拦住：%t %+v %v", started, blocked, err)
			}
			if runStartCount(t, s.db, `SELECT cooldown_manual_used FROM accounts WHERE id=1`) != 0 ||
				runStartCount(t, s.db, `SELECT count(*) FROM run_records`) != 0 ||
				runStartCount(t, s.db, `SELECT count(*) FROM meta WHERE key LIKE 'runtime.session.%'`) != 0 ||
				s.resources.(*runStartFactory).calls.Load() != 0 {
				t.Fatal("失败事务部分提交，或仍然访问了外部资源")
			}
			if view := s.sessionSnapshot(1); view != nil {
				t.Fatalf("失败事务没有释放临时名额：%+v", view)
			}
		})
	}
}

func TestCoolingManualOpportunityIsNotReturnedAfterStartupFailure(t *testing.T) {
	s, now, taskID := runStartFixture(t)
	factory := &runStartFactory{opened: make(chan context.Context, 1), release: make(chan struct{})}
	s.resources = factory
	runStartExec(t, s.db, `UPDATE accounts SET state='cooling',cooldown_until=?`, now.Add(time.Hour).UnixMilli())
	if _, err := s.RunNow(t.Context(), taskID); err != nil {
		t.Fatal(err)
	}
	if started, blocked, err := s.tryStartRun(t.Context(), s.queuedStarts()[0]); err != nil || !started || blocked != nil {
		t.Fatalf("冷却手动机会没有开始：%t %+v %v", started, blocked, err)
	}
	select {
	case <-factory.opened:
	case <-time.After(3 * time.Second):
		t.Fatal("资源没有开始")
	}
	if runStartCount(t, s.db, `SELECT cooldown_manual_used FROM accounts WHERE id=1`) != 1 {
		t.Fatal("资源已开始却没有持久消耗手动机会")
	}
	s.mu.Lock()
	run := s.runs[taskID]
	s.mu.Unlock()
	close(factory.release)
	runStartWait(t, run.Done)
	_, err := s.RunNow(t.Context(), taskID)
	var blocked *ServiceError
	if !errors.As(err, &blocked) || blocked.Reason.Code != "run.account_cooling" ||
		runStartCount(t, s.db, `SELECT cooldown_manual_used FROM accounts WHERE id=1`) != 1 {
		t.Fatalf("启动失败退还了冷却手动机会：%v", err)
	}
}

func TestAIEntryBudgetUsesProtectedOldDayBeforeProviderCheck(t *testing.T) {
	s, now, taskID := runStartFixture(t)
	*now = time.Date(2030, 11, 11, 17, 0, 0, 0, time.UTC)
	if _, err := s.settings.Apply(t.Context(), settings.Values{"general.timezone": json.RawMessage(`"Asia/Shanghai"`), "ai.daily_token_limit": json.RawMessage(`10`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.settings.Apply(t.Context(), settings.Values{"general.timezone": json.RawMessage(`"UTC"`)}); err != nil {
		t.Fatal(err)
	}
	runStartExec(t, s.db, `INSERT INTO ai_calls(at,input_tokens,output_tokens,purpose) VALUES(?,10,0,'analysis')`, now.UnixMilli())
	runStartExec(t, s.db, `UPDATE tasks SET judge_mode='ai',profile_json='{"musts":[{"id":"m1","text":"正常使用","source":"user","rev":1}],"next_no":2}'`)
	runStartExec(t, s.db, `INSERT INTO ai_providers(uuid,name,base_url,vision_model,sort_index) VALUES('local-provider','本地模型','http://127.0.0.1','local',0)`)
	*now = time.Date(2030, 11, 12, 0, 30, 0, 0, time.UTC)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	task, err := loadTaskTx(t.Context(), tx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := s.preflightTx(t.Context(), tx, task, TriggerManual)
	want := time.Date(2030, 11, 12, 16, 0, 0, 0, time.UTC).UnixMilli()
	if err != nil || preflight.Blocked == nil || preflight.Blocked.Reason.Code != "run.ai_daily_limit" || preflight.NeedsUser ||
		preflight.Blocked.NextAllowedAt == nil || *preflight.Blocked.NextAllowedAt != want {
		t.Fatalf("改时区提前恢复AI用量，或把每日上限记为无服务商跳过：%+v %v", preflight, err)
	}
}

func TestRunLoggerKeepsStructuredSafeDataAndStopsOnWriteFailure(t *testing.T) {
	s, now, taskID := runStartFixture(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	at := now.UnixMilli()
	record, err := beginRunTx(t.Context(), tx, RunRecord{TaskID: &taskID, TaskName: "相机", Trigger: TriggerManual, StartedAt: &at})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(s.ctx)
	run := &runExecution{Ref: RunRef{RunID: record.ID, TaskID: taskID}, Context: ctx, Cancel: cancel}
	logger := s.runLogger(run)
	logger("进入搜索结果页", map[string]any{"网址": "https://local.invalid/?token=private", "正文": "private"})
	var message string
	if err := s.db.QueryRowContext(t.Context(), `SELECT message FROM run_steps WHERE run_id=?`, record.ID).Scan(&message); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "run.browser_search_entered") || strings.Contains(message, "private") || strings.Contains(message, "进入搜索") {
		t.Fatalf("日志保存了翻译句子或敏感数据：%s", message)
	}
	runStartExec(t, s.db, `CREATE TRIGGER reject_step BEFORE INSERT ON run_steps BEGIN SELECT RAISE(FAIL,'blocked'); END`)
	logger("等待", map[string]any{"毫秒": int64(5)})
	var failure *ServiceError
	if !errors.As(context.Cause(ctx), &failure) || failure.Kind != ErrorUnavailable {
		t.Fatalf("日志写入失败后没有停止运行：%v", context.Cause(ctx))
	}
	budget := s.newVisitBudget(ctx, 1, &record.ID, false)
	if err := budget.Acquire(quota.Search, "之后的访问"); !errors.Is(err, context.Canceled) || errors.Is(err, web.ErrQuota) {
		t.Fatalf("存储失败被降级为额度不足：%v", err)
	}
	if runStartCount(t, s.db, `SELECT count(*) FROM quota_events`) != 0 {
		t.Fatal("失败停止后仍计费并准备访问")
	}
	if _, err := s.RunNow(t.Context(), taskID); !errors.As(err, &failure) || failure.Kind != ErrorUnavailable || len(s.queuedStarts()) != 0 {
		t.Fatalf("日志存储失败后仍接纳新的外部运行：%v", err)
	}
}

func TestExpiredAccountIsNotPresentedAsRecoveringAfterGap(t *testing.T) {
	for _, state := range []AccountState{AccountExpired, AccountSignedOut, AccountNeedsVerification} {
		t.Run(string(state), func(t *testing.T) {
			s, now, taskID := runStartFixture(t)
			runStartExec(t, s.db, `UPDATE accounts SET state=?,last_run_ended_at=?,next_gap_seconds=180`, state, now.UnixMilli())
			_, err := s.RunNow(t.Context(), taskID)
			var rejected *ServiceError
			code := map[AccountState]string{AccountExpired: "run.account_expired", AccountSignedOut: "run.account_signed_out", AccountNeedsVerification: "run.account_needs_verification"}[state]
			if !errors.As(err, &rejected) || rejected.Reason.Code != code || rejected.NextAllowedAt != nil {
				t.Fatalf("需要用户处理却编出了间隔后可运行：%v", err)
			}
		})
	}
}

func TestAIUserConditionIsNotHiddenByDailyLimit(t *testing.T) {
	for _, emptyProfile := range []bool{false, true} {
		t.Run(map[bool]string{false: "无服务商", true: "清单为空"}[emptyProfile], func(t *testing.T) {
			s, now, taskID := runStartFixture(t)
			if _, err := s.settings.Apply(t.Context(), settings.Values{"ai.daily_token_limit": json.RawMessage(`10`)}); err != nil {
				t.Fatal(err)
			}
			profile := `{"musts":[{"id":"m1","text":"正常使用","source":"user","rev":1}],"next_no":2}`
			code := "run.no_ai_provider"
			if emptyProfile {
				profile, code = `{}`, "run.empty_profile"
			}
			runStartExec(t, s.db, `UPDATE tasks SET judge_mode='ai',profile_json=?`, profile)
			runStartExec(t, s.db, `INSERT INTO ai_calls(at,input_tokens,output_tokens,purpose) VALUES(?,10,0,'analysis')`, now.UnixMilli())
			_, err := s.RunNow(t.Context(), taskID)
			var rejected *ServiceError
			if !errors.As(err, &rejected) || rejected.Reason.Code != code || rejected.NextAllowedAt != nil {
				t.Fatalf("需要用户处理却编出了明天可运行：%v", err)
			}
		})
	}
}

func TestAutomaticUserConditionKeepsFuturePlanWithoutEarlySkip(t *testing.T) {
	cases := []struct {
		name       string
		state      AccountState
		timeCode   string
		userCode   string
		withoutID  bool
		failPause  bool
		accountGap bool
	}{
		{name: "未来计划与失效", state: AccountExpired, timeCode: "run.not_due", userCode: "run.account_expired"},
		{name: "失败保护与未登录", state: AccountSignedOut, timeCode: "run.failure_paused", userCode: "run.account_signed_out", failPause: true},
		{name: "账号间隔与失效", state: AccountExpired, timeCode: "run.account_gap", userCode: "run.account_expired", accountGap: true},
		{name: "账号间隔与待验证", state: AccountNeedsVerification, timeCode: "run.account_gap", userCode: "run.account_needs_verification", accountGap: true},
		{name: "未来计划与无账号", timeCode: "run.not_due", userCode: "run.no_account", withoutID: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			s, now, taskID := runStartFixture(t)
			ready := now.Add(10 * time.Minute).UnixMilli()
			runStartExec(t, s.db, `UPDATE tasks SET enabled=1`)
			if test.withoutID {
				runStartExec(t, s.db, `UPDATE tasks SET account_id=NULL,next_run_at=?`, ready)
			} else {
				runStartExec(t, s.db, `UPDATE accounts SET state=?`, test.state)
				switch {
				case test.accountGap:
					ready = now.Add(3 * time.Minute).UnixMilli()
					runStartExec(t, s.db, `UPDATE accounts SET last_run_ended_at=?,next_gap_seconds=180`, now.UnixMilli())
				case test.failPause:
					runStartExec(t, s.db, `UPDATE tasks SET failure_paused_until=?`, ready)
				default:
					runStartExec(t, s.db, `UPDATE tasks SET next_run_at=?`, ready)
				}
			}
			start := readRunStartPreflight(t, s, taskID, TriggerAuto)
			if !start.NeedsUser || start.UserReason == nil || start.UserReason.Code != test.userCode || start.Blocked == nil ||
				start.Blocked.Reason.Code != test.timeCode || start.Blocked.NextAllowedAt == nil || *start.Blocked.NextAllowedAt != ready {
				t.Fatalf("用户原因丢失，或自动计划被提前改变：%+v", start)
			}
			if shown := displayPreflightBlock(start); shown == nil || shown.Reason.Code != test.userCode || shown.NextAllowedAt != nil {
				t.Fatalf("展示编造了到时即可运行：%+v", shown)
			}
			request := startRequest{TaskID: taskID, Kind: SessionRun, Trigger: TriggerAuto}
			started, blocked, err := s.tryStartRun(t.Context(), request)
			if err != nil || started || blocked == nil || runStartCount(t, s.db, `SELECT count(*) FROM run_records`) != 0 {
				t.Fatalf("尚未到自动检查时间却记了skip：%t %+v %v", started, blocked, err)
			}
			*now = time.UnixMilli(ready + 1000).UTC()
			started, blocked, err = s.tryStartRun(t.Context(), request)
			if err != nil || started || blocked == nil || blocked.Reason.Code != test.userCode || blocked.NextAllowedAt != nil ||
				runStartCount(t, s.db, `SELECT count(*) FROM run_skip_events`) != 1 {
				t.Fatalf("到检查时间没有记真实用户原因：%t %+v %v", started, blocked, err)
			}
			if s.resources.(*runStartFactory).calls.Load() != 0 {
				t.Fatal("用户处理门禁打开了浏览器")
			}
		})
	}
}

func TestAutomaticDailyLimitKeepsProviderReasonForDisplay(t *testing.T) {
	s, now, taskID := runStartFixture(t)
	if _, err := s.settings.Apply(t.Context(), settings.Values{"ai.daily_token_limit": json.RawMessage(`10`)}); err != nil {
		t.Fatal(err)
	}
	runStartExec(t, s.db, `UPDATE tasks SET enabled=1,judge_mode='ai',profile_json='{"musts":[{"id":"m1","text":"正常使用","source":"user","rev":1}],"next_no":2}'`)
	runStartExec(t, s.db, `INSERT INTO ai_calls(at,input_tokens,output_tokens,purpose) VALUES(?,10,0,'analysis')`, now.UnixMilli())
	start := readRunStartPreflight(t, s, taskID, TriggerAuto)
	if !start.NeedsUser || start.UserReason == nil || start.UserReason.Code != "run.no_ai_provider" || start.Blocked == nil ||
		start.Blocked.Reason.Code != "run.ai_daily_limit" || start.Blocked.NextAllowedAt == nil {
		t.Fatalf("日限覆盖了无服务商事实：%+v", start)
	}
	if shown := displayPreflightBlock(start); shown == nil || shown.Reason.Code != "run.no_ai_provider" || shown.NextAllowedAt != nil {
		t.Fatalf("无服务商被展示成明天可跑：%+v", shown)
	}
	started, blocked, err := s.tryStartRun(t.Context(), startRequest{TaskID: taskID, Kind: SessionRun, Trigger: TriggerAuto})
	if err != nil || started || blocked == nil || runStartCount(t, s.db, `SELECT count(*) FROM run_records`) != 0 {
		t.Fatalf("到日限恢复前不应提前记无服务商skip：%t %+v %v", started, blocked, err)
	}
}

func readRunStartPreflight(t *testing.T, s *Service, taskID int64, trigger RunTrigger) preflightResult {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	task, err := loadTaskTx(t.Context(), tx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	start, err := s.preflightTx(t.Context(), tx, task, trigger)
	if err != nil {
		t.Fatal(err)
	}
	return start
}

func TestBrowserRunMessageCoversCurrentM4SuccessEvents(t *testing.T) {
	for _, event := range []string{
		"首页已打开", "登录首页已打开", "找到搜索框", "清空了搜索框", "进入搜索结果页",
		"关闭了广告", "选了发布时间", "等待", "查找页面元素", "接口返回", "打开了详情页",
		"找到地区筛选", "找到价格筛选", "找到筛选元素", "登录框已出现",
	} {
		t.Run(event, func(t *testing.T) {
			message, status := browserRunMessage(event, nil)
			if status != StepOK || message.Code == "run.browser_operation" || message.Args == nil {
				t.Fatalf("正常页面事件误记为未知警告：%+v %s", message, status)
			}
		})
	}
	// 卖家没有独立新事件，成功路径仍使用等待和接口返回。
	for _, test := range []struct {
		event, code string
		fields      map[string]any
	}{
		{"等待", "run.browser_wait", map[string]any{"步骤": "卖家主页停留", "毫秒": int64(7)}},
		{"接口返回", "run.browser_response", map[string]any{"步骤": "卖家评价", "ret": []string{"SUCCESS::调用成功"}, "耗时毫秒": int64(7)}},
	} {
		message, status := browserRunMessage(test.event, test.fields)
		if message.Code != test.code || status != StepOK || !reflect.DeepEqual(message.Args, map[string]any{"duration_ms": int64(7)}) {
			t.Fatalf("卖家正常操作没有沿用已有成功映射：%+v %s", message, status)
		}
	}
}

func TestBrowserRunMessageDistinguishesRetryAndMissingElement(t *testing.T) {
	for _, event := range []string{"发布时间筛选没有反应，重做一次", "翻页没有反应，重做一次"} {
		message, status := browserRunMessage(event, nil)
		if status != StepWarn || message.Code != "run.browser_retry" || !reflect.DeepEqual(message.Args, map[string]any{}) {
			t.Errorf("实际重试日志没有稳定的警告原因：%+v %s", message, status)
		}
	}
	for _, found := range []bool{true, false} {
		message, status := browserRunMessage("查找页面元素", map[string]any{"文字": "最新", "找到": found, "规则版本": 2})
		want := StepOK
		if !found {
			want = StepWarn
		}
		if status != want || message.Code != "run.browser_element_lookup" || !reflect.DeepEqual(message.Args, map[string]any{"found": found, "rule_version": 2}) {
			t.Errorf("元素查询结果与日志状态不一致：%+v %s", message, status)
		}
	}
	message, status := browserRunMessage("本地登录预检警告", map[string]any{"原因": "没有 unb Cookie"})
	if status != StepWarn || message.Code != "run.browser_login_cookie_warning" || len(message.Args) != 0 {
		t.Fatalf("原有登录预警被改成成功或存入原句：%+v %s", message, status)
	}
}

func TestM4LoginHomeLogIsStoredAsSuccessfulHomeOpen(t *testing.T) {
	s, now, taskID := runStartFixture(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	at := now.UnixMilli()
	record, err := beginRunTx(t.Context(), tx, RunRecord{TaskID: &taskID, TaskName: "相机", Trigger: TriggerManual, StartedAt: &at})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(s.ctx)
	defer cancel(nil)
	logger := s.runLogger(&runExecution{Ref: RunRef{RunID: record.ID, TaskID: taskID}, Context: ctx, Cancel: cancel})
	logger("登录首页已打开", map[string]any{"耗时毫秒": int64(12)})
	var status StepStatus
	var raw string
	if err := s.db.QueryRowContext(t.Context(), `SELECT status,message FROM run_steps WHERE run_id=?`, record.ID).Scan(&status, &raw); err != nil {
		t.Fatal(err)
	}
	var message locales.Message
	if err := json.Unmarshal([]byte(raw), &message); err != nil {
		t.Fatal(err)
	}
	if status != StepOK || message.Code != "run.browser_home_opened" || message.Args["duration_ms"] != float64(12) {
		t.Fatalf("真实登录首页事件落盘成了普通警告：%s %+v", status, message)
	}
}
