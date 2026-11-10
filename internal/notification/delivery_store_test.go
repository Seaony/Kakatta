package notification

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/notify"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/store"
)

func deliveryDB(t *testing.T) *sql.DB {
	t.Helper()
	stored, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "delivery.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stored.Close() })
	return stored.DB()
}

func deliverySeed(t *testing.T, db *sql.DB, count int, kind intentKind) messageGroup {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), `INSERT OR IGNORE INTO tasks(id,name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index) VALUES(1,'任务','相机',1,60,'keyword','recommended',0)`); err != nil {
		t.Fatal(err)
	}
	group := messageGroup{Summary: count > 1}
	for i := 1; i <= count; i++ {
		item := "商品" + string(rune('A'+i-1))
		if _, err := db.ExecContext(t.Context(), `INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at,price) VALUES(?,?,?,1,2,9000)`, item, item, "https://example.invalid/item"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), `INSERT INTO findings(id,task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read,notify_kind,recommended_price) VALUES(?,1,?,1,2,0,'matched','done',0,'recommended',10000)`, i, item); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), `INSERT INTO notification_intents(id,finding_id,task_id,kind,state,created_at,updated_at,price,previous_price) VALUES(?,?,1,?,'pending',1,1,9000,10000)`, i, i, kind); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		projection, err := service.NotificationFindingTx(t.Context(), tx, int64(i))
		if err != nil {
			t.Fatal(err)
		}
		value, err := loadDeliveryIntentTx(t.Context(), tx, int64(i))
		if err != nil {
			t.Fatal(err)
		}
		tx.Rollback()
		group.Members = append(group.Members, candidate{Intent: value, Finding: projection.Finding, Listing: projection.Listing})
	}
	return group
}

func deliveryCount(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var result int64
	if err := db.QueryRowContext(t.Context(), query, args...).Scan(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func deliveryTx(t *testing.T, db *sql.DB, fn func(context.Context, *sql.Tx) error) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := fn(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func deliveryOutcome(ok bool) channelOutcome {
	return channelOutcome{ChannelID: 1, ChannelName: "测试渠道", Attempted: true, CalledSender: true, EndedAt: 100, Result: notify.Result{OK: ok, ErrorCode: "http_status", HTTPStatus: 503}, IntentIDs: []int64{1}}
}

func TestTaskAttemptSavesSummaryAndMarkersAtomically(t *testing.T) {
	db := deliveryDB(t)
	group := deliverySeed(t, db, 13, intentRecommended)
	s := &Service{db: db}
	outcome := deliveryOutcome(true)
	deliveryTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		_, err := s.recordTaskAttemptTx(ctx, tx, service.RunRef{TaskID: 1, SearchGeneration: 1}, group, "summary", outcome)
		return err
	})
	for _, query := range []string{`SELECT count(*) FROM delivery_items`, `SELECT count(*) FROM delivery_intents`, `SELECT count(*) FROM notification_intents WHERE state='sent'`, `SELECT count(*) FROM findings WHERE notified_at=100`} {
		if count := deliveryCount(t, db, query); count != 13 {
			t.Fatalf("摘要成员未全落盘：%s = %d", query, count)
		}
	}
	if _, err := db.ExecContext(t.Context(), `DELETE FROM deliveries`); err != nil {
		t.Fatal(err)
	}
	if deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE state='sent'`) != 13 {
		t.Fatal("清历史影响结算状态")
	}
}

func TestTaskAttemptRollbackCannotLeavePartialSuccess(t *testing.T) {
	db := deliveryDB(t)
	group := deliverySeed(t, db, 2, intentRecommended)
	s := &Service{db: db}
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER fail_item BEFORE INSERT ON delivery_intents WHEN NEW.intent_id=2 BEGIN SELECT RAISE(ABORT,'本地失败'); END`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.recordTaskAttemptTx(t.Context(), tx, service.RunRef{TaskID: 1, SearchGeneration: 1}, group, "summary", deliveryOutcome(true))
	tx.Rollback()
	if err == nil {
		t.Fatal("关联失败未返回")
	}
	if deliveryCount(t, db, `SELECT count(*) FROM deliveries`)+deliveryCount(t, db, `SELECT count(*) FROM findings WHERE notified_at IS NOT NULL`)+deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE state='sent'`) != 0 {
		t.Fatal("失败事务残留部分成功")
	}
}

func TestTaskAttemptOldGenerationKeepsTruthWithoutResurrectingFinding(t *testing.T) {
	db := deliveryDB(t)
	group := deliverySeed(t, db, 1, intentRecommended)
	s := &Service{db: db}
	if _, err := db.ExecContext(t.Context(), `UPDATE tasks SET search_generation=2`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE notification_intents SET state='cancelled'`); err != nil {
		t.Fatal(err)
	}
	deliveryTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		_, err := s.recordTaskAttemptTx(ctx, tx, service.RunRef{TaskID: 1, SearchGeneration: 1}, group, "recommended", deliveryOutcome(true))
		return err
	})
	if deliveryCount(t, db, `SELECT count(*) FROM deliveries WHERE ok=1`) != 1 || deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE state='sent'`) != 1 || deliveryCount(t, db, `SELECT count(*) FROM findings WHERE notified_at IS NOT NULL`) != 0 {
		t.Fatal("旧代真实记录或发现隔离错误")
	}
	if _, err := db.ExecContext(t.Context(), `DELETE FROM findings`); err != nil {
		t.Fatal(err)
	}
	deliveryTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		_, err := s.recordTaskAttemptTx(ctx, tx, service.RunRef{TaskID: 1, SearchGeneration: 1}, group, "recommended", deliveryOutcome(true))
		return err
	})
	if deliveryCount(t, db, `SELECT count(*) FROM findings`) != 0 {
		t.Fatal("旧回执复活了已删发现")
	}
}

func TestFinishTaskRoundCountsOnlyCompleteFailure(t *testing.T) {
	for _, mode := range []string{"all_failed", "some_success", "unattempted", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			db := deliveryDB(t)
			group := deliverySeed(t, db, 1, intentRecommended)
			s := &Service{db: db}
			outcomes := []channelOutcome{deliveryOutcome(false), deliveryOutcome(false)}
			switch mode {
			case "some_success":
				outcomes[1].Result.OK = true
			case "unattempted":
				outcomes[1].Attempted = false
			case "cancelled":
				outcomes[1].Result.ErrorCode = "cancelled"
			}
			deliveryTx(t, db, func(ctx context.Context, tx *sql.Tx) error { return s.finishRoundTx(ctx, tx, group, outcomes, 100) })
			want := int64(0)
			if mode == "all_failed" {
				want = 1
			}
			if got := deliveryCount(t, db, `SELECT failure_count FROM notification_intents WHERE id=1`); got != want {
				t.Fatalf("失败次数=%d，预期%d", got, want)
			}
		})
	}
}

func TestIntentRetryLimitAndStrictDayBoundary(t *testing.T) {
	for _, kind := range []intentKind{intentRecommended, intentNew, intentPriceDrop, intentPriceUpgrade} {
		t.Run(string(kind), func(t *testing.T) {
			db := deliveryDB(t)
			group := deliverySeed(t, db, 1, kind)
			s := &Service{db: db}
			if kind == intentNew {
				if _, err := db.ExecContext(t.Context(), `UPDATE findings SET notify_kind='new'`); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 3; i++ {
				deliveryTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
					return s.finishRoundTx(ctx, tx, group, []channelOutcome{deliveryOutcome(false)}, 100+int64(i))
				})
			}
			if deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE state='abandoned' AND failure_count=3 AND first_failed_at=100`) != 1 {
				t.Fatal("第三次全失败没有终止")
			}
			want := int64(0)
			if kind == intentRecommended || kind == intentNew {
				want = 1
			}
			if got := deliveryCount(t, db, `SELECT count(*) FROM findings WHERE notify_skip_reason IS NOT NULL`); got != want {
				t.Fatalf("错误覆盖首次推送原因：%d", got)
			}
		})
	}
	first := int64(100)
	if intentRetryExpired(intent{FirstFailedAt: &first}, first+(24*time.Hour).Milliseconds()) {
		t.Fatal("恰好24小时不能终止")
	}
	if !intentRetryExpired(intent{FirstFailedAt: &first}, first+(24*time.Hour).Milliseconds()+1) {
		t.Fatal("超过24小时应终止")
	}
}

func TestTaskFailureErrorContainsOnlyFixedMetadata(t *testing.T) {
	db := deliveryDB(t)
	group := deliverySeed(t, db, 1, intentRecommended)
	s := &Service{db: db}
	outcome := deliveryOutcome(false)
	outcome.Result.ErrorCode = "https://secret.invalid/token"
	outcome.Result.ProviderCode = "secret-token"
	deliveryTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		_, err := s.recordTaskAttemptTx(ctx, tx, service.RunRef{TaskID: 1, SearchGeneration: 1}, group, "recommended", outcome)
		return err
	})
	var raw string
	if err := db.QueryRowContext(t.Context(), `SELECT error FROM deliveries`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if json.Unmarshal([]byte(raw), &parsed) != nil {
		t.Fatal("错误不是固定消息")
	}
	if raw == "" || deliveryCount(t, db, `SELECT count(*) FROM deliveries WHERE error LIKE '%secret%'`) != 0 {
		t.Fatal("错误泄漏任意响应")
	}
}

func TestFinishTaskRoundDoesNotCountOmittedSummaryMember(t *testing.T) {
	db := deliveryDB(t)
	group := deliverySeed(t, db, 2, intentRecommended)
	s := &Service{db: db}
	a, b := deliveryOutcome(false), deliveryOutcome(false)
	a.IntentIDs = []int64{1, 2}
	b.IntentIDs = []int64{1}
	deliveryTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		return s.finishRoundTx(ctx, tx, group, []channelOutcome{a, b}, 100)
	})
	if deliveryCount(t, db, `SELECT failure_count FROM notification_intents WHERE id=1`) != 1 || deliveryCount(t, db, `SELECT failure_count FROM notification_intents WHERE id=2`) != 0 {
		t.Fatal("部分未发送的摘要成员被计作全部失败")
	}
}

func TestTaskSuccessUsesSentPriceAndOldDropCannotRegressBaseline(t *testing.T) {
	db := deliveryDB(t)
	group := deliverySeed(t, db, 1, intentRecommended)
	s := &Service{db: db}
	deliveryTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		_, err := s.recordTaskAttemptTx(ctx, tx, service.RunRef{TaskID: 1, SearchGeneration: 1}, group, "recommended", deliveryOutcome(true))
		return err
	})
	if deliveryCount(t, db, `SELECT recommended_price FROM findings WHERE id=1`) != 9000 {
		t.Fatal("推荐成功未使用实际发送价格")
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE findings SET last_drop_notified_price=8000,drop_notified_at=150`); err != nil {
		t.Fatal(err)
	}
	group.Members[0].Intent.Kind = intentPriceDrop
	outcome := deliveryOutcome(true)
	outcome.EndedAt = 200
	deliveryTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		_, err := s.recordTaskAttemptTx(ctx, tx, service.RunRef{TaskID: 1, SearchGeneration: 1}, group, "price_drop", outcome)
		return err
	})
	if deliveryCount(t, db, `SELECT last_drop_notified_price FROM findings WHERE id=1`) != 8000 || deliveryCount(t, db, `SELECT drop_notified_at FROM findings WHERE id=1`) != 150 {
		t.Fatal("旧渠道晚回执把成功降价基准倒退")
	}
}

func TestPriceUpgradeUsesRecommendationMarker(t *testing.T) {
	db := deliveryDB(t)
	group := deliverySeed(t, db, 1, intentPriceUpgrade)
	s := &Service{db: db}
	if _, err := db.ExecContext(t.Context(), `UPDATE findings SET notify_kind=NULL,notify_skip_reason='{"code":"notify.baseline","args":{}}'`); err != nil {
		t.Fatal(err)
	}
	deliveryTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		_, err := s.recordTaskAttemptTx(ctx, tx, service.RunRef{TaskID: 1, SearchGeneration: 1}, group, "recommended", deliveryOutcome(true))
		return err
	})
	if deliveryCount(t, db, `SELECT count(*) FROM findings WHERE notified_at=100 AND drop_notified_at IS NULL AND last_drop_notified_price IS NULL`) != 1 {
		t.Fatal("升档误用降价成功字段")
	}
}

func TestTaskSendSnapshotRechecksRecommendationHardGates(t *testing.T) {
	for _, mode := range []string{"over_budget", "avoid", "rejected", "drop_over_budget"} {
		t.Run(mode, func(t *testing.T) {
			db := deliveryDB(t)
			kind := intentRecommended
			if mode == "drop_over_budget" {
				kind = intentPriceDrop
			}
			group := deliverySeed(t, db, 1, kind)
			s := &Service{db: db}
			switch mode {
			case "over_budget", "drop_over_budget":
				if _, err := db.ExecContext(t.Context(), `UPDATE tasks SET budget_max=8000`); err != nil {
					t.Fatal(err)
				}
			case "avoid":
				if _, err := db.ExecContext(t.Context(), `UPDATE findings SET grade='avoid'`); err != nil {
					t.Fatal(err)
				}
			case "rejected":
				if _, err := db.ExecContext(t.Context(), `UPDATE findings SET decision='rejected',reject_kind='unmatched'`); err != nil {
					t.Fatal(err)
				}
			}
			deliveryTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
				_, actual, err := s.refreshTaskGroupTx(ctx, tx, service.RunRef{TaskID: 1, SearchGeneration: 1}, group, 100)
				if err != nil {
					return err
				}
				want := 0
				if mode == "drop_over_budget" {
					want = 1
				}
				if len(actual.Members) != want {
					t.Fatalf("硬闸不符合唯一规则：%d", len(actual.Members))
				}
				return nil
			})
			if deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE state='pending'`) != 1 || deliveryCount(t, db, `SELECT count(*) FROM findings WHERE notify_skip_reason IS NOT NULL`) != 0 {
				t.Fatal("重查改写了一生推送原因")
			}
		})
	}
}

func TestTaskSendSnapshotRemovesUnavailableSummaryMembers(t *testing.T) {
	for _, mode := range []string{"deleted", "blocked", "cancelled", "search_changed"} {
		t.Run(mode, func(t *testing.T) {
			db := deliveryDB(t)
			group := deliverySeed(t, db, 2, intentRecommended)
			s := &Service{db: db}
			statements := []string{}
			switch mode {
			case "deleted":
				statements = []string{`DELETE FROM findings WHERE id=2`}
			case "blocked":
				statements = []string{`UPDATE listings SET seller_id='卖家' WHERE item_id='商品B'`, `INSERT INTO blocked_sellers(seller_id) VALUES('卖家')`}
			case "cancelled":
				statements = []string{`UPDATE notification_intents SET state='cancelled' WHERE id=2`}
			case "search_changed":
				statements = []string{`UPDATE tasks SET search_generation=2`}
			}
			for _, statement := range statements {
				if _, err := db.ExecContext(t.Context(), statement); err != nil {
					t.Fatal(err)
				}
			}
			deliveryTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
				_, actual, err := s.refreshTaskGroupTx(ctx, tx, service.RunRef{TaskID: 1, SearchGeneration: 1}, group, 100)
				if err != nil {
					return err
				}
				want := 1
				if mode == "search_changed" {
					want = 0
				}
				if len(actual.Members) != want || len(actual.Members) > 0 && actual.Members[0].Finding.ID != 1 {
					t.Fatalf("没有剔除作废成员：%+v", actual)
				}
				return nil
			})
		})
	}
}

func TestTaskSendSnapshotPausesChangedPriceAndNewHardGates(t *testing.T) {
	for _, mode := range []string{"drop_changed", "drop_unknown", "drop_bounced", "upgrade_budget", "new_filter", "new_unmatched", "current_over_budget", "unknown_budget"} {
		t.Run(mode, func(t *testing.T) {
			db := deliveryDB(t)
			kind := intentPriceDrop
			switch mode {
			case "upgrade_budget":
				kind = intentPriceUpgrade
			case "new_filter", "new_unmatched":
				kind = intentNew
			case "current_over_budget", "unknown_budget":
				kind = intentRecommended
			}
			group := deliverySeed(t, db, 1, kind)
			s := &Service{db: db}
			statements := []string{}
			switch mode {
			case "drop_changed":
				statements = []string{`UPDATE listings SET price=9100`}
			case "drop_unknown":
				statements = []string{`UPDATE listings SET price=NULL`}
			case "drop_bounced":
				statements = []string{`UPDATE listings SET price=11000`}
			case "upgrade_budget":
				statements = []string{`UPDATE tasks SET budget_max=8000`}
			case "new_filter":
				statements = []string{`UPDATE findings SET decision='rejected',reject_kind='filter'`}
			case "new_unmatched":
				statements = []string{`UPDATE findings SET decision='rejected',reject_kind='unmatched'`}
			case "current_over_budget":
				statements = []string{`UPDATE tasks SET budget_max=9500`, `UPDATE listings SET price=10000`}
			case "unknown_budget":
				statements = []string{`UPDATE tasks SET budget_max=9500`, `UPDATE listings SET price=NULL`}
			}
			for _, statement := range statements {
				if _, err := db.ExecContext(t.Context(), statement); err != nil {
					t.Fatal(err)
				}
			}
			deliveryTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
				_, actual, err := s.refreshTaskGroupTx(ctx, tx, service.RunRef{TaskID: 1, SearchGeneration: 1}, group, 100)
				if err != nil {
					return err
				}
				want := 0
				if mode == "new_unmatched" {
					want = 1
				}
				if len(actual.Members) != want {
					t.Fatalf("发送前资格不一致：%s %d", mode, len(actual.Members))
				}
				return nil
			})
			state := "pending"
			if mode == "unknown_budget" {
				state = "cancelled"
			}
			if deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE state=? AND failure_count=0`, state) != 1 {
				t.Fatal("发送前资格没有按正式规则结算")
			}
		})
	}
}

func TestTaskSendSnapshotReanalysisCancelsOrKeepsOriginalIntent(t *testing.T) {
	for _, mode := range []string{"pending", "failed", "rejected", "policy_none", "unknown_budget", "matched"} {
		t.Run(mode, func(t *testing.T) {
			db := deliveryDB(t)
			group := deliverySeed(t, db, 1, intentRecommended)
			s := &Service{db: db}
			statements := []string{`UPDATE findings SET reanalysis_origin=1`, `UPDATE notification_intents SET failure_count=2,first_failed_at=50`}
			switch mode {
			case "pending":
				statements = append(statements, `UPDATE findings SET decision='pending',stage='detail'`)
			case "failed":
				statements = append(statements, `UPDATE findings SET decision='failed',stage='detail'`)
			case "rejected":
				statements = append(statements, `UPDATE findings SET decision='rejected',reject_kind='keyword_miss',reason='{"code":"notify.keyword_miss","args":{}}'`)
			case "policy_none":
				statements = append(statements, `UPDATE tasks SET notify_policy='none'`)
			case "unknown_budget":
				statements = append(statements, `UPDATE tasks SET budget_max=9500`, `UPDATE listings SET price=NULL`)
			case "matched":
				statements = append(statements, `UPDATE listings SET price=8500`)
			}
			for _, statement := range statements {
				if _, err := db.ExecContext(t.Context(), statement); err != nil {
					t.Fatal(err)
				}
			}
			deliveryTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
				_, actual, err := s.refreshTaskGroupTx(ctx, tx, service.RunRef{TaskID: 1, SearchGeneration: 1}, group, 100)
				if err != nil {
					return err
				}
				want := 0
				if mode == "matched" {
					want = 1
				}
				if len(actual.Members) != want {
					t.Fatalf("重分析资格错误：%s %+v", mode, actual)
				}
				return nil
			})
			cancelled := mode == "rejected" || mode == "policy_none" || mode == "unknown_budget"
			wantState := "pending"
			if cancelled {
				wantState = "cancelled"
			}
			if deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=1 AND state=? AND failure_count=2 AND first_failed_at=50`, wantState) != 1 {
				t.Fatal("重分析重置了重试窗口或未正确结算")
			}
			if cancelled {
				var raw string
				var reason locales.Message
				if err := db.QueryRowContext(t.Context(), `SELECT notify_skip_reason FROM findings WHERE id=1`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if json.Unmarshal([]byte(raw), &reason) != nil {
					t.Fatalf("取消没有具体原因：%s", raw)
				}
				want := "notify.keyword_miss"
				if mode == "policy_none" {
					want = "notify.policy_none"
				}
				if mode == "unknown_budget" {
					want = "notify.price_unknown_budget"
				}
				if reason.Code != want {
					t.Fatalf("取消原因=%s，预期%s", reason.Code, want)
				}
			}
			if mode == "matched" && deliveryCount(t, db, `SELECT price FROM notification_intents WHERE id=1`) != 8500 {
				t.Fatal("合格重分析没有沿原意图更新当前价")
			}
			wantOrigin := int64(0)
			if mode == "pending" || mode == "failed" {
				wantOrigin = 1
			}
			if deliveryCount(t, db, `SELECT reanalysis_origin FROM findings WHERE id=1`) != wantOrigin {
				t.Fatal("重分析来源没有按结算状态消费")
			}
			if mode == "matched" && deliveryCount(t, db, `SELECT recommended_price FROM findings WHERE id=1`) != 8500 {
				t.Fatal("重分析未同步推荐基准")
			}
		})
	}
}

func TestTaskAttemptKeepsACKAfterReanalysisCancellation(t *testing.T) {
	db := deliveryDB(t)
	group := deliverySeed(t, db, 1, intentRecommended)
	s := &Service{db: db}
	if _, err := db.ExecContext(t.Context(), `UPDATE findings SET reanalysis_origin=1,decision='rejected',reject_kind='keyword_miss',reason='{"code":"notify.keyword_miss","args":{}}'`); err != nil {
		t.Fatal(err)
	}
	deliveryTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		_, actual, err := s.refreshTaskGroupTx(ctx, tx, service.RunRef{TaskID: 1, SearchGeneration: 1}, group, 50)
		if err == nil && len(actual.Members) != 0 {
			t.Error("不合格重分析仍进入发送组")
		}
		return err
	})
	if deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE state='cancelled'`) != 1 {
		t.Fatal("重分析没有取消旧意图")
	}
	deliveryTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		_, err := s.recordTaskAttemptTx(ctx, tx, service.RunRef{TaskID: 1, SearchGeneration: 1}, group, "recommended", deliveryOutcome(true))
		return err
	})
	if deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE state='sent' AND sent_at=100`) != 1 || deliveryCount(t, db, `SELECT count(*) FROM findings WHERE notified_at=100 AND reanalysis_origin=0 AND notify_skip_reason IS NOT NULL`) != 1 || deliveryCount(t, db, `SELECT count(*) FROM deliveries WHERE ok=1`) != 1 {
		t.Fatal("已发出的真实成功被重新分析取消覆盖")
	}
}

func TestTaskSendSnapshotReanalysisKeepsNewAndUpgradeIdentity(t *testing.T) {
	for _, kind := range []intentKind{intentNew, intentPriceUpgrade} {
		t.Run(string(kind), func(t *testing.T) {
			db := deliveryDB(t)
			group := deliverySeed(t, db, 1, kind)
			s := &Service{db: db}
			statements := []string{`UPDATE findings SET reanalysis_origin=1`, `UPDATE listings SET price=8500`, `UPDATE notification_intents SET failure_count=2,first_failed_at=50`}
			if kind == intentNew {
				statements = append(statements, `UPDATE tasks SET notify_policy='all_new'`, `UPDATE findings SET notify_kind='new',recommended_price=NULL,decision='rejected',reject_kind='keyword_miss',reason='{"code":"notify.keyword_miss","args":{}}'`)
			} else {
				statements = append(statements, `UPDATE tasks SET judge_mode='ai',notify_policy='good'`, `UPDATE findings SET decision='scored',score=84,grade='good'`)
			}
			for _, statement := range statements {
				if _, err := db.ExecContext(t.Context(), statement); err != nil {
					t.Fatal(err)
				}
			}
			deliveryTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
				_, actual, err := s.refreshTaskGroupTx(ctx, tx, service.RunRef{TaskID: 1, SearchGeneration: 1}, group, 100)
				if err != nil {
					return err
				}
				if len(actual.Members) != 1 || actual.Members[0].Intent.ID != 1 || actual.Members[0].Intent.Kind != kind || actual.Members[0].Intent.Price == nil || *actual.Members[0].Intent.Price != 8500 {
					t.Fatalf("重分析换了提醒或没更新价格：%+v", actual)
				}
				return nil
			})
			if deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=1 AND state='pending' AND failure_count=2 AND first_failed_at=50 AND price=8500`) != 1 || deliveryCount(t, db, `SELECT reanalysis_origin FROM findings WHERE id=1`) != 0 {
				t.Fatal("重分析重置了窗口或没消费来源")
			}
			if kind == intentNew {
				if deliveryCount(t, db, `SELECT count(*) FROM findings WHERE recommended_price IS NULL`) != 1 {
					t.Fatal("新商品提醒新增了推荐基准")
				}
			} else if deliveryCount(t, db, `SELECT recommended_price FROM findings WHERE id=1`) != 8500 {
				t.Fatal("升档未同步当前推荐基准")
			}
		})
	}
}

func TestTaskSendSnapshotReanalysisDoesNotRewriteAlreadySentPrice(t *testing.T) {
	db := deliveryDB(t)
	group := deliverySeed(t, db, 1, intentRecommended)
	s := &Service{db: db}
	for _, statement := range []string{`UPDATE notification_intents SET state='sent',sent_at=40`, `UPDATE findings SET notified_at=40,recommended_price=9000,reanalysis_origin=1`, `UPDATE listings SET price=8500`} {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	deliveryTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		_, actual, err := s.refreshTaskGroupTx(ctx, tx, service.RunRef{TaskID: 1, SearchGeneration: 1}, group, 100)
		if err != nil {
			return err
		}
		if len(actual.Members) != 1 || actual.Members[0].Intent.Price == nil || *actual.Members[0].Intent.Price != 9000 {
			t.Fatalf("同轮成功阻断其它渠道或改写真实快照：%+v", actual)
		}
		return nil
	})
	if deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE state='sent' AND sent_at=40 AND price=9000`) != 1 || deliveryCount(t, db, `SELECT count(*) FROM findings WHERE notified_at=40 AND recommended_price=9000 AND reanalysis_origin=0`) != 1 {
		t.Fatal("重分析改写了已有成功基准或未消费来源")
	}
}

func TestTaskSendSnapshotReanalysisUpgradeBounceKeepsPendingOrigin(t *testing.T) {
	db := deliveryDB(t)
	group := deliverySeed(t, db, 1, intentPriceUpgrade)
	s := &Service{db: db}
	for _, statement := range []string{`UPDATE tasks SET judge_mode='ai',notify_policy='good'`, `UPDATE findings SET decision='scored',score=84,grade='good',reanalysis_origin=1`, `UPDATE listings SET price=11000`} {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	deliveryTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		_, actual, err := s.refreshTaskGroupTx(ctx, tx, service.RunRef{TaskID: 1, SearchGeneration: 1}, group, 100)
		if err == nil && len(actual.Members) != 0 {
			t.Error("回涨商品发出了升档通知")
		}
		return err
	})
	if deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE state='pending' AND price=9000`) != 1 || deliveryCount(t, db, `SELECT count(*) FROM findings WHERE recommended_price=10000 AND reanalysis_origin=1`) != 1 {
		t.Fatal("回涨消费了待复验来源或推进了基准")
	}
}
