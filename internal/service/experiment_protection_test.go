package service

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/experimentstate"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/quota"
)

func experimentRecord(t *testing.T, s *Service, changes map[string]any) string {
	t.Helper()
	raw, err := os.ReadFile("../experimentstate/testdata/visits-5709c5c.json")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for key, value := range changes {
		fields[key] = value
	}
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.dataDir, "experiment", "7")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "visits.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExperimentProtectionBlocksEveryAccountAndVisit(t *testing.T) {
	nowAt := time.Date(2035, 11, 10, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name    string
		changes map[string]any
		code    string
		until   time.Time
	}{
		{"冷却", nil, "run.experiment_cooldown", nowAt.Add(2 * time.Hour)},
		{"拒绝", map[string]any{"cooldown_until": time.Time{}, "denied_until": nowAt.Add(24 * time.Hour)}, "run.experiment_denied", nowAt.Add(24 * time.Hour)},
		{"间隔", map[string]any{"cooldown_until": time.Time{}}, "run.experiment_gap", nowAt.Add(180 * time.Second)},
		{"会话未结束", map[string]any{"in_progress": true}, "run.experiment_in_progress", time.Time{}},
		{"待验证", map[string]any{"verify_pending": true, "verify_url": "http://local/punish"}, "run.experiment_verification", time.Time{}},
		{"新版本", map[string]any{"version": 999}, "run.experiment_records_unavailable", time.Time{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, clock, taskID := runStartFixture(t)
			*clock = nowAt
			experimentRecord(t, s, test.changes)
			runStartExec(t, s.db, `INSERT INTO accounts(id,name,state) VALUES(2,'另一个账号','active')`)
			runStartExec(t, s.db, `INSERT INTO tasks(id,name,keyword,enabled,account_id,max_pages,interval_minutes,judge_mode,notify_policy,sort_index) VALUES(2,'另一个任务','本地',1,2,1,30,'keyword','recommended',1)`)
			runStartExec(t, s.db, `UPDATE tasks SET enabled=1,next_run_at=?`, nowAt.UnixMilli())
			check := func(err error) {
				t.Helper()
				var problem *ServiceError
				if !errors.As(err, &problem) || problem.Kind != ErrorConflict || problem.Reason.Code != test.code {
					t.Fatalf("实验保护未挡住正式访问：%v", err)
				}
				if test.until.IsZero() {
					if problem.NextAllowedAt != nil {
						t.Fatal("未解决的保护不能承诺到时放行")
					}
				} else if problem.NextAllowedAt == nil || *problem.NextAllowedAt != test.until.UnixMilli() {
					t.Fatalf("保护到期时间不对：%+v", problem)
				}
			}
			for _, id := range []int64{taskID, 2} {
				_, err := s.RunNow(t.Context(), id)
				check(err)
				runtime, err := s.GetTaskRuntime(t.Context(), id)
				if err != nil || runtime.Blocked == nil || runtime.Blocked.Reason.Code != test.code {
					t.Fatalf("网页任务状态缺少实验保护：%+v %v", runtime, err)
				}
				_, err = s.OpenLogin(t.Context(), id)
				check(err)
				_, err = s.OpenVerification(t.Context(), id)
				check(err)
				for _, manual := range []bool{false, true} {
					for _, kind := range []quota.Kind{quota.Search, quota.Detail} {
						check(s.newVisitBudget(t.Context(), id, nil, manual).Acquire(kind, "本地动作"))
					}
				}
			}
			if _, err := s.scheduleOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			status, err := s.GetSystemStatus(t.Context())
			if err != nil || status.Scheduler.Blocked == nil || status.Scheduler.Blocked.Reason.Code != test.code || status.Scheduler.Accepting {
				t.Fatalf("系统页没有显示全部账号受实验保护：%+v %v", status.Scheduler, err)
			}
			if got := s.resources.(*runStartFactory).calls.Load(); got != 0 {
				t.Fatalf("实验保护期间仍启动了%d次浏览器", got)
			}
			if got := runStartCount(t, s.db, `SELECT count(*) FROM quota_events`); got != 0 {
				t.Fatalf("实验保护期间仍消耗了%d次访问", got)
			}
		})
	}
}

func TestServiceStartupAndRestartRespectExperimentCooldown(t *testing.T) {
	deps := runtimeCoreDeps(t)
	at := time.Date(2035, 11, 10, 12, 0, 0, 0, time.UTC)
	deps.Now = func() time.Time { return at }
	path := experimentRecord(t, &Service{dataDir: deps.DataDir}, nil)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cleanupExec(t, deps.DB, `INSERT INTO accounts(id,name,state) VALUES(1,'正式账号','active')`)
	cleanupExec(t, deps.DB, `INSERT INTO tasks(id,name,keyword,enabled,account_id,max_pages,interval_minutes,judge_mode,notify_policy,sort_index,next_run_at) VALUES(1,'本地任务','本地',1,1,1,30,'keyword','recommended',0,?)`, at.UnixMilli())
	for range 2 {
		waits := make(chan time.Duration, 8)
		deps.Pacer = pacer.New(scheduleWaitClock{requests: waits})
		s, err := New(t.Context(), deps)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		timeout := time.NewTimer(3 * time.Second)
		ready := false
		for !ready {
			select {
			case wait := <-waits:
				ready = wait == 2*time.Hour
			case <-timeout.C:
				t.Fatal("正式调度没有等待实验冷却到期")
			}
		}
		timeout.Stop()
		status, err := s.GetSystemStatus(t.Context())
		if err != nil || status.Scheduler.Blocked == nil || status.Scheduler.Blocked.Reason.Code != "run.experiment_cooldown" || !status.Scheduler.Running || status.Scheduler.Accepting {
			t.Fatalf("正式启动没有保留保护说明：%+v %v", status.Scheduler, err)
		}
		if deps.Resources.(*runtimeCoreFactory).calls.Load() != 0 {
			t.Fatal("正式启动在实验冷却内打开了浏览器")
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(raw) {
		t.Fatal("正式服务重启修改或清掉了实验记录")
	}
}

func TestExperimentProtectionExpiresAndKeepsObservedClock(t *testing.T) {
	s, now, taskID := runStartFixture(t)
	*now = time.Date(2035, 11, 10, 12, 0, 0, 0, time.UTC)
	experimentRecord(t, s, map[string]any{"cooldown_until": time.Time{}})
	for _, offset := range []time.Duration{2 * time.Minute, -24 * time.Hour} {
		*now = time.Date(2035, 11, 10, 12, 0, 0, 0, time.UTC).Add(offset)
		_, err := s.RunNow(t.Context(), taskID)
		var problem *ServiceError
		if !errors.As(err, &problem) || problem.Reason.Code != "run.experiment_gap" {
			t.Fatalf("账号间隔或倒退时钟保护失效：%v", err)
		}
	}
	*now = time.Date(2035, 11, 10, 12, 3, 0, 0, time.UTC)
	if submission, err := s.RunNow(t.Context(), taskID); err != nil || submission.Status != SubmissionQueued {
		t.Fatalf("实验间隔结束后仍挡住正式任务：%+v %v", submission, err)
	}
}

func TestDeletingFormalAccountKeepsSeparateExperimentProfile(t *testing.T) {
	s, _, _ := runStartFixture(t)
	formal := filepath.Join(s.dataDir, "browser", "1")
	experiment := experimentstate.ProfileDir(s.dataDir, 1, false)
	for _, dir := range []string{formal, experiment} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Local State"), []byte("本地浏览器样例"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteAccount(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(formal); !os.IsNotExist(err) {
		t.Fatal("正式账号删除没有清理自己的目录")
	}
	if raw, err := os.ReadFile(filepath.Join(experiment, "Local State")); err != nil || string(raw) != "本地浏览器样例" {
		t.Fatal("正式账号删除误删了实验浏览器配置")
	}
}

func TestExperimentProtectionRechecksAcceptedQueue(t *testing.T) {
	for _, login := range []bool{false, true} {
		t.Run(map[bool]string{false: "手动运行", true: "扫码会话"}[login], func(t *testing.T) {
			s, now, taskID := runStartFixture(t)
			*now = time.Date(2035, 11, 10, 12, 0, 0, 0, time.UTC)
			if login {
				if view, err := s.OpenLogin(t.Context(), 1); err != nil || view.Phase != SessionQueued {
					t.Fatalf("未能先排入真实扫码队列：%+v %v", view, err)
				}
			} else if view, err := s.RunNow(t.Context(), taskID); err != nil || view.Status != SubmissionQueued {
				t.Fatalf("未能先排入真实运行队列：%+v %v", view, err)
			}
			experimentRecord(t, s, nil)
			if _, err := s.scheduleOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := s.resources.(*runStartFactory).calls.Load(); got != 0 {
				t.Fatal("旧队列绕过了后来出现的实验冷却")
			}
			if len(s.queuedStarts()) != 0 || runStartCount(t, s.db, `SELECT count(*) FROM quota_events`) != 0 {
				t.Fatal("受保护的旧队列没有结束，或发生了闲鱼访问")
			}
		})
	}
}
