package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/settings"
)

func queuedGapFixture(t *testing.T, gap time.Duration) (*Service, *time.Time, int64, startRequest) {
	t.Helper()
	s, now, taskID := runStartFixture(t)
	occupied, ok, err := s.reserveStart(startRequest{AccountID: 1, Kind: SessionLogin}, 1, *now)
	if err != nil || !ok {
		t.Fatalf("无法占用同账号资源：%t %v", ok, err)
	}
	t.Cleanup(func() { s.releaseReservation(occupied.View.ID) })
	if _, err := s.RunNow(t.Context(), taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.scheduleOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(s.queuedStarts()) != 1 || runStartCount(t, s.db, `SELECT count(*) FROM run_records`) != 0 {
		t.Fatal("账号资源被占时没有保留已接纳的队列")
	}
	s.releaseReservation(occupied.View.ID)
	// 资源关闭和入队保持同一毫秒，不能用时间先后猜是否等过资源。
	runStartExec(t, s.db, `UPDATE accounts SET last_run_ended_at=?,next_gap_seconds=? WHERE id=1`, now.UnixMilli(), int64(gap/time.Second))
	return s, now, taskID, s.queuedStarts()[0]
}

func TestQueuedResourceWaitSurvivesNaturalGapAndWakesAtItsEnd(t *testing.T) {
	s, now, taskID, request := queuedGapFixture(t, 3*time.Minute)
	until := now.Add(3 * time.Minute)
	for _, advance := range []time.Duration{0, 3*time.Minute - time.Millisecond} {
		*now = request.QueuedAt.Add(advance)
		next, err := s.scheduleOnce(t.Context())
		if err != nil || next == nil || !next.Equal(until) {
			t.Fatalf("没有睡到队列的自然间隔结束：next=%v err=%v", next, err)
		}
		queue := s.queuedStarts()
		if len(queue) != 1 || queue[0].SessionID != request.SessionID || !queue[0].QueuedAt.Equal(request.QueuedAt) ||
			runStartCount(t, s.db, `SELECT count(*) FROM run_records`) != 0 || s.resources.(*runStartFactory).calls.Load() != 0 {
			t.Fatal("自然间隔取消了队列、重置了排队时间或提前开资源")
		}
	}
	factory := &runStartFactory{opened: make(chan context.Context, 1), release: make(chan struct{})}
	s.resources = factory
	release := sync.OnceFunc(func() { close(factory.release) })
	defer release()
	*now = until
	if _, err := s.scheduleOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-factory.opened:
	case <-time.After(time.Second):
		t.Fatal("间隔到期后没有启动原排队任务")
	}
	if len(s.queuedStarts()) != 0 || factory.calls.Load() != 1 ||
		runStartCount(t, s.db, `SELECT count(*) FROM run_records WHERE task_id=? AND trigger='manual' AND ended_at IS NULL`, taskID) != 1 ||
		runStartCount(t, s.db, `SELECT count(*) FROM run_skip_events`) != 0 {
		t.Fatal("到期后没有恰好运行一次，或留下了错误的跳过记录")
	}
}

func TestQueuedNaturalGapStillRejectsEveryOtherGate(t *testing.T) {
	for _, test := range []struct {
		name  string
		code  string
		apply func(*testing.T, *Service, *time.Time)
	}{
		{"账号失效", "run.account_expired", func(t *testing.T, s *Service, _ *time.Time) {
			runStartExec(t, s.db, `UPDATE accounts SET state='expired'`)
		}},
		{"账号待验证", "run.account_needs_verification", func(t *testing.T, s *Service, _ *time.Time) {
			runStartExec(t, s.db, `UPDATE accounts SET state='needs_verification'`)
		}},
		{"拒绝访问", "run.account_denied", func(t *testing.T, s *Service, now *time.Time) {
			runStartExec(t, s.db, `UPDATE accounts SET state='denied',cooldown_until=?`, now.Add(time.Hour).UnixMilli())
		}},
		{"冷却机会已用", "run.account_cooling", func(t *testing.T, s *Service, now *time.Time) {
			runStartExec(t, s.db, `UPDATE accounts SET state='cooling',cooldown_until=?,cooldown_manual_used=1`, now.Add(time.Hour).UnixMilli())
		}},
		{"额度比间隔更早恢复", "run.hourly_quota", func(t *testing.T, s *Service, now *time.Time) {
			for range 30 {
				runStartExec(t, s.db, `INSERT INTO quota_events(account_id,kind,at) VALUES(1,'detail',?)`, now.Add(-59*time.Minute).UnixMilli())
			}
		}},
		{"静默比间隔更早结束", "run.quiet_hours", func(t *testing.T, s *Service, _ *time.Time) {
			if _, err := s.settings.Apply(t.Context(), settings.Values{"crawl.quiet_hours": json.RawMessage(`{"enabled":true,"start":"08:00","end":"08:01"}`)}); err != nil {
				t.Fatal(err)
			}
		}},
		{"模型每日用量到限", "run.ai_daily_limit", func(t *testing.T, s *Service, now *time.Time) {
			runStartExec(t, s.db, `UPDATE tasks SET judge_mode='ai',requirement='自用',profile_json='{"specs":[{"name":"型号","options":["A"],"values":["A"],"source":"user","edited":true}],"allowed_kinds":["sale"],"next_no":1}'`)
			runStartExec(t, s.db, `INSERT INTO ai_providers(id,uuid,name,base_url,api_key_enc,vision_model,text_model,sort_index) VALUES(1,'local','本地服务','http://models.invalid',X'00','vision','text',0)`)
			if _, err := s.settings.Apply(t.Context(), settings.Values{"ai.daily_token_limit": json.RawMessage(`1`)}); err != nil {
				t.Fatal(err)
			}
			runStartExec(t, s.db, `INSERT INTO ai_calls(at,input_tokens,output_tokens) VALUES(?,1,0)`, now.UnixMilli())
		}},
		{"模型服务商不可用", "run.no_ai_provider", func(t *testing.T, s *Service, _ *time.Time) {
			runStartExec(t, s.db, `UPDATE tasks SET judge_mode='ai',requirement='自用',profile_json='{"specs":[{"name":"型号","options":["A"],"values":["A"],"source":"user","edited":true}],"allowed_kinds":["sale"],"next_no":1}'`)
		}},
		{"需求清单被清空", "run.empty_profile", func(t *testing.T, s *Service, _ *time.Time) {
			runStartExec(t, s.db, `UPDATE tasks SET judge_mode='ai',requirement='自用',profile_json='{}'`)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, now, taskID, _ := queuedGapFixture(t, 2*time.Hour)
			test.apply(t, s, now)
			if _, err := s.scheduleOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(s.queuedStarts()) != 0 || s.sessionSnapshot(1) != nil || s.resources.(*runStartFactory).calls.Load() != 0 ||
				runStartCount(t, s.db, `SELECT count(*) FROM run_records WHERE task_id=? AND outcome='skipped' AND json_extract(message,'$.code')=?`, taskID, test.code) != 1 ||
				runStartCount(t, s.db, `SELECT count(*) FROM run_skip_events`) != 1 ||
				runStartCount(t, s.db, `SELECT count(*) FROM meta WHERE key LIKE 'runtime.session.%'`) != 0 {
				t.Fatalf("自然间隔掩盖了其它闸门或留下资源：%s", test.code)
			}
		})
	}
}

func TestUnqueuedOrReboundGapNeverBecomesAcceptedResourceWait(t *testing.T) {
	for _, mode := range []string{"首次点击", "未等资源的已接纳请求", "改绑另一个账号"} {
		t.Run(mode, func(t *testing.T) {
			s, now, taskID := runStartFixture(t)
			if mode == "改绑另一个账号" {
				var request startRequest
				s, now, taskID, request = queuedGapFixture(t, 3*time.Minute)
				runStartExec(t, s.db, `INSERT INTO accounts(id,name,state,last_run_ended_at,next_gap_seconds) VALUES(2,'新账号','active',?,180)`, now.UnixMilli())
				runStartExec(t, s.db, `UPDATE tasks SET account_id=2 WHERE id=?`, taskID)
				if _, err := s.scheduleOnce(t.Context()); err != nil {
					t.Fatal(err)
				}
				if len(s.queuedStarts()) != 0 || s.resources.(*runStartFactory).calls.Load() != 0 || runStartCount(t, s.db, `SELECT count(*) FROM run_records WHERE task_id=? AND account_id=2 AND outcome='skipped'`, request.TaskID) != 1 {
					t.Fatal("改绑继承了旧账号的自然间隔等待资格")
				}
				return
			}
			if mode != "首次点击" {
				if _, err := s.RunNow(t.Context(), taskID); err != nil {
					t.Fatal(err)
				}
			}
			runStartExec(t, s.db, `UPDATE accounts SET last_run_ended_at=?,next_gap_seconds=180`, now.UnixMilli())
			if mode == "首次点击" {
				_, err := s.RunNow(t.Context(), taskID)
				var rejected *ServiceError
				if !errors.As(err, &rejected) || rejected.Reason.Code != "run.account_gap" || rejected.NextAllowedAt == nil || *rejected.NextAllowedAt != now.Add(3*time.Minute).UnixMilli() {
					t.Fatalf("初次间隔门禁没有立即拒绝及给出时间：%v", err)
				}
			} else if _, err := s.scheduleOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(s.queuedStarts()) != 0 || s.resources.(*runStartFactory).calls.Load() != 0 || runStartCount(t, s.db, `SELECT count(*) FROM run_skip_events`) != 1 {
				t.Fatal("不是资源等待却保留了间隔队列")
			}
		})
	}
}

func TestQueuedGapRecheckAfterReservationRestoresTheSameQueue(t *testing.T) {
	s, now, taskID, request := queuedGapFixture(t, 3*time.Minute)
	*now = now.Add(3 * time.Minute)
	firstCheck, releaseCheck := make(chan struct{}), make(chan struct{})
	var checks atomic.Int64
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	s.now = func() time.Time {
		if checks.Add(1) == 1 {
			close(firstCheck)
			select {
			case <-releaseCheck:
			case <-ctx.Done():
			}
		}
		return *now
	}
	done := make(chan error, 1)
	go func() {
		started, blocked, err := s.tryStartRun(ctx, request)
		if started || blocked != nil {
			err = errors.Join(err, errors.New("二次自然间隔没有保留队列"))
		}
		done <- err
	}()
	runStartWait(t, firstCheck)
	s.mu.Lock()
	close(releaseCheck)
	_, err := s.db.ExecContext(ctx, `UPDATE accounts SET last_run_ended_at=?,next_gap_seconds=180 WHERE id=1`, now.UnixMilli())
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	queue := s.queuedStarts()
	view := s.sessionSnapshot(1)
	if len(queue) != 1 || queue[0].SessionID != request.SessionID || !queue[0].QueuedAt.Equal(request.QueuedAt) || view == nil || view.Phase != SessionQueued || view.StartedAt != nil ||
		s.resources.(*runStartFactory).calls.Load() != 0 || runStartCount(t, s.db, `SELECT count(*) FROM run_records WHERE task_id=?`, taskID) != 0 {
		t.Fatal("二次重检丢了排队身份、占着页面或提前创建了运行")
	}
	next, err := s.scheduleOnce(ctx)
	if err != nil || next == nil || !next.Equal(now.Add(3*time.Minute)) {
		t.Fatalf("二次间隔没有加入下次唤醒：%v %v", next, err)
	}
}

func TestQueuedGapClockCommitFailureCannotStartOrCancelTheRequest(t *testing.T) {
	s, _, _, request := queuedGapFixture(t, 3*time.Minute)
	runStartExec(t, s.db, `CREATE TRIGGER reject_clock BEFORE UPDATE ON meta WHEN NEW.key='quota.clock' BEGIN SELECT RAISE(FAIL,'blocked'); END`)
	started, blocked, err := s.tryStartRun(t.Context(), request)
	if err == nil || started || blocked != nil || s.resources.(*runStartFactory).calls.Load() != 0 || runStartCount(t, s.db, `SELECT count(*) FROM run_records`) != 0 || len(s.queuedStarts()) != 1 {
		t.Fatalf("时钟提交失败仍开资源、吞了队列或部分记跳过：%t %+v %v", started, blocked, err)
	}
}

func TestQueuedRebindingCannotCarryOldResourceWaitIntoSecondCheck(t *testing.T) {
	s, now, taskID, request := queuedGapFixture(t, 3*time.Minute)
	*now = now.Add(3 * time.Minute)
	runStartExec(t, s.db, `INSERT INTO accounts(id,name,state) VALUES(2,'新账号','active')`)
	runStartExec(t, s.db, `UPDATE tasks SET account_id=2 WHERE id=?`, taskID)
	firstCheck, releaseCheck := make(chan struct{}), make(chan struct{})
	var checks atomic.Int64
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	s.now = func() time.Time {
		if checks.Add(1) == 1 {
			close(firstCheck)
			select {
			case <-releaseCheck:
			case <-ctx.Done():
			}
		}
		return *now
	}
	type startResult struct {
		started bool
		blocked *RuntimeBlocked
		err     error
	}
	done := make(chan startResult, 1)
	go func() {
		started, blocked, err := s.tryStartRun(ctx, request)
		done <- startResult{started, blocked, err}
	}()
	runStartWait(t, firstCheck)
	s.mu.Lock()
	close(releaseCheck)
	_, err := s.db.ExecContext(ctx, `UPDATE accounts SET last_run_ended_at=?,next_gap_seconds=180 WHERE id=2`, now.UnixMilli())
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil || result.started || result.blocked == nil || result.blocked.Reason.Code != "run.account_gap" {
			t.Fatalf("第二次检查继承了旧账号的资源等待资格：%t %+v %v", result.started, result.blocked, result.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if len(s.queuedStarts()) != 0 || s.sessionSnapshot(1) != nil || s.sessionSnapshot(2) != nil || s.resources.(*runStartFactory).calls.Load() != 0 ||
		runStartCount(t, s.db, `SELECT count(*) FROM run_records WHERE task_id=? AND account_id=2 AND outcome='skipped' AND json_extract(message,'$.code')='run.account_gap'`, taskID) != 1 {
		t.Fatal("改绑后的第二次间隔没有移队、记跳过并释放资源名额")
	}
}

func TestCancelQueuedNaturalGapRemovesItsWakeDeadline(t *testing.T) {
	s, _, taskID, _ := queuedGapFixture(t, 3*time.Minute)
	if _, err := s.scheduleOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelRun(t.Context(), taskID); err != nil {
		t.Fatal(err)
	}
	next, err := s.scheduleOnce(t.Context())
	if err != nil || next != nil || len(s.queuedStarts()) != 0 || s.resources.(*runStartFactory).calls.Load() != 0 {
		t.Fatalf("取消后仍保留间隔唤醒或队列：next=%v err=%v", next, err)
	}
}

func TestRebindingResourceQueueClearsOldAccountWaitFact(t *testing.T) {
	s, now, taskID, _ := queuedGapFixture(t, 3*time.Minute)
	*now = now.Add(3 * time.Minute)
	runStartExec(t, s.db, `INSERT INTO accounts(id,name,state) VALUES(2,'新账号','active'),(3,'占用名额','active')`)
	runStartExec(t, s.db, `UPDATE tasks SET account_id=2 WHERE id=?`, taskID)
	occupied, ok, err := s.reserveStart(startRequest{AccountID: 3, Kind: SessionLogin}, 1, *now)
	if err != nil || !ok {
		t.Fatalf("无法准备全局页面占用：%t %v", ok, err)
	}
	defer s.releaseReservation(occupied.View.ID)
	if _, err := s.scheduleOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	queue := s.queuedStarts()
	if len(queue) != 1 || queue[0].AccountID != 2 || queue[0].WaitedForAccount || !queue[0].GapUntil.IsZero() {
		t.Fatal("改绑新账号保留了旧账号的自然间隔等待资格")
	}
	s.releaseReservation(occupied.View.ID)
	runStartExec(t, s.db, `UPDATE accounts SET last_run_ended_at=?,next_gap_seconds=180 WHERE id=2`, now.UnixMilli())
	if _, err := s.scheduleOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(s.queuedStarts()) != 0 || s.resources.(*runStartFactory).calls.Load() != 0 || runStartCount(t, s.db, `SELECT count(*) FROM run_records WHERE task_id=? AND account_id=2 AND outcome='skipped'`, taskID) != 1 {
		t.Fatal("没有等过新账号的资源，却等待了新账号间隔")
	}
}
