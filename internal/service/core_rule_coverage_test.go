package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/settings"
)

func TestVerificationCooldownUsesExactDoublingAndConfiguredCap(t *testing.T) {
	for _, config := range []struct {
		name        string
		base, cap   int64
		wantByLevel []int64
	}{
		{"默认", 120, 1440, []int64{120, 240, 480, 960, 1440, 1440}},
		{"自定义非整倍上限", 7, 25, []int64{7, 14, 25, 25}},
		{"起点就是上限", 17, 17, []int64{17, 17}},
	} {
		for index, want := range config.wantByLevel {
			t.Run(fmt.Sprintf("%s/等级%d", config.name, index+1), func(t *testing.T) {
				db, values, now := accountStateDB(t)
				values["crawl.verify_cooldown_base_minutes"] = json.RawMessage(fmt.Sprint(config.base))
				values["crawl.verify_cooldown_max_minutes"] = json.RawMessage(fmt.Sprint(config.cap))
				id := insertStateAccount(t, db, AccountNeedsVerification)
				if _, err := db.ExecContext(t.Context(), `UPDATE accounts SET verify_level=? WHERE id=?`, index+1, id); err != nil {
					t.Fatal(err)
				}
				stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
					_, err := completeVerificationTx(ctx, tx, id, values, now)
					return err
				})
				account := stateAccount(t, db, id)
				deadline := now + want*int64(time.Minute/time.Millisecond)
				if account.CooldownUntil == nil || *account.CooldownUntil != deadline {
					t.Fatalf("冷却必须恰好为%d分钟，账号=%+v", want, account)
				}
			})
		}
	}
	t.Run("很高等级仍停在上限", func(t *testing.T) {
		db, values, now := accountStateDB(t)
		id := insertStateAccount(t, db, AccountNeedsVerification)
		if _, err := db.ExecContext(t.Context(), `UPDATE accounts SET verify_level=? WHERE id=?`, int64(math.MaxInt64), id); err != nil {
			t.Fatal(err)
		}
		stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
			account, err := completeVerificationTx(ctx, tx, id, values, now)
			if err == nil && (account.CooldownUntil == nil || *account.CooldownUntil != now+int64(24*time.Hour/time.Millisecond)) {
				t.Fatalf("很高等级没有准确封顶：%+v", account)
			}
			return err
		})
	})
}

func TestAutomaticTasksCompeteByActualReadyTime(t *testing.T) {
	for _, gate := range []string{"运行计划", "失败暂停", "账号间隔"} {
		t.Run(gate, func(t *testing.T) {
			now := scheduleTime(t, "2026-11-10T08:00:00Z")
			s := scheduleService(t, func() time.Time { return now }, settings.Values{"crawl.global_pages": json.RawMessage(`1`)})
			s.dataDir = t.TempDir()
			factory := &scheduleFactory{opened: make(chan int64, 2), release: make(chan struct{})}
			s.resources, s.notifier = factory, &runStartNotifier{}
			t.Cleanup(func() {
				close(factory.release)
				_ = s.Close()
			})
			scheduleExec(t, s, `INSERT INTO accounts(id,name,state) VALUES(1,'账号一','active'),(2,'账号二','active')`)
			for id := int64(1); id <= 2; id++ {
				ready := now.Add(-time.Hour)
				if id == 1 {
					ready = now.Add(-2 * time.Hour)
					if gate == "运行计划" {
						ready = now.Add(-10 * time.Minute)
					}
				}
				scheduleExec(t, s, `INSERT INTO tasks(id,name,keyword,enabled,account_id,personal_only,max_pages,interval_minutes,judge_mode,notify_policy,sort_index,next_run_at)
					VALUES(?,'自动任务','手机',1,?,0,1,30,'keyword','none',0,?)`, id, id, ready.UnixMilli())
			}
			switch gate {
			case "失败暂停":
				scheduleExec(t, s, `UPDATE tasks SET failure_paused_until=? WHERE id=1`, now.Add(-10*time.Minute).UnixMilli())
			case "账号间隔":
				scheduleExec(t, s, `UPDATE accounts SET last_run_ended_at=?,next_gap_seconds=1200 WHERE id=1`, now.Add(-30*time.Minute).UnixMilli())
			}
			if _, err := s.scheduleOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case account := <-factory.opened:
				if account != 2 {
					t.Fatalf("较早能运行的账号二没拿到唯一名额，实际账号=%d", account)
				}
			case <-time.After(time.Second):
				t.Fatal("没有启动最早能运行的任务")
			}
			if scheduleCount(t, s, `SELECT COUNT(*) FROM run_records`) != 1 || scheduleCount(t, s, `SELECT COUNT(*) FROM run_records WHERE task_id=2 AND trigger='auto'`) != 1 {
				t.Fatal("自动任务先后顺序错误，或突破了唯一页面名额")
			}
		})
	}
}

func TestExpiredFailurePausePreservesTheLaterAutomaticPlan(t *testing.T) {
	now := scheduleTime(t, "2026-11-10T08:00:00Z")
	until := now.Add(-10 * time.Minute).UnixMilli()
	for _, test := range []struct {
		name string
		plan any
		want int64
	}{
		{"没有计划", nil, until},
		{"暂停晚于计划", now.Add(-time.Hour).UnixMilli(), until},
		{"计划晚于暂停", now.Add(time.Hour).UnixMilli(), now.Add(time.Hour).UnixMilli()},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := scheduleService(t, func() time.Time { return now }, nil)
			t.Cleanup(func() { _ = s.Close() })
			scheduleExec(t, s, `INSERT INTO tasks(id,name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index,next_run_at,failure_paused_until,consecutive_failures)
				VALUES(1,'自动任务','手机',1,30,'keyword','none',0,?,?,3)`, test.plan, until)
			for range 2 {
				if _, err := s.advanceDeadlines(t.Context()); err != nil {
					t.Fatal(err)
				}
				var plan, failures int64
				var paused sql.NullInt64
				if err := s.db.QueryRow(`SELECT next_run_at,failure_paused_until,consecutive_failures FROM tasks WHERE id=1`).Scan(&plan, &paused, &failures); err != nil {
					t.Fatal(err)
				}
				if plan != test.want || paused.Valid || failures != 3 {
					t.Fatalf("暂停恢复改变了计划或清掉失败数：plan=%d paused=%v failures=%d", plan, paused, failures)
				}
			}
		})
	}
}
