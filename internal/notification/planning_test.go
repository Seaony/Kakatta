package notification

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/store"
)

const planNow int64 = 1_800_000_000_000

func planFixture(t *testing.T) (*Service, *sql.DB, service.RunRef) {
	t.Helper()
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "planning.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	db := database.DB()
	planExec(t, db, `INSERT INTO tasks(id,name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index)
		VALUES(1,'通知任务','camera',1,60,'ai','good',0)`)
	planExec(t, db, `INSERT INTO channels(id,uuid,type,name,config_json) VALUES(1,'planning-channel','webhook','本地样例','{}')`)
	planExec(t, db, `INSERT INTO task_channels(task_id,channel_id) VALUES(1,1)`)
	return &Service{db: db}, db, service.RunRef{RunID: 1, TaskID: 1, SearchGeneration: 1}
}

func planExec(t *testing.T, db *sql.DB, statement string, args ...any) sql.Result {
	t.Helper()
	result, err := db.ExecContext(t.Context(), statement, args...)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func planFinding(t *testing.T, db *sql.DB, id int64, decision analysis.Decision, price *int64, score *int64) {
	t.Helper()
	itemID := fmt.Sprintf("item-%d", id)
	planExec(t, db, `INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at,price)
		VALUES(?,?,'https://example.invalid/item',1,1,?)`, itemID, itemID, price)
	stage := service.StageDone
	if decision == service.DecisionPending || decision == service.DecisionFailed {
		stage = service.StageDetail
	}
	var grade *analysis.Grade
	if score != nil {
		grade = decisionPtr(gradeForScore(*score))
	}
	planExec(t, db, `INSERT INTO findings(id,task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read,score,grade,scored_price,reason)
		VALUES(?,1,?,1,?,0,?,?,0,?,?,?,'{"code":"analysis.no_fair_price","args":{}}')`,
		id, itemID, planNow-1000+id, decision, stage, score, grade, price)
}

func planRun(t *testing.T, s *Service, ref service.RunRef, firstIDs []int64, now int64) []candidate {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	values, err := s.planRunTx(t.Context(), tx, ref, firstIDs, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return values
}

func planCount(t *testing.T, db *sql.DB, statement string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := db.QueryRowContext(t.Context(), statement, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestPlanRunRecoversUnmarkedConclusionsWithoutRepeatingJudgment(t *testing.T) {
	s, db, ref := planFixture(t)
	for id, state := range []analysis.Decision{analysis.DecisionScored, analysis.DecisionScored, service.DecisionPending, service.DecisionFailed, service.DecisionSkipped, analysis.DecisionScored} {
		planFinding(t, db, int64(id+1), state, decisionPtr(int64(10000)), decisionPtr(int64(85)))
	}
	planExec(t, db, `UPDATE findings SET is_baseline=1 WHERE id=2`)
	planExec(t, db, `UPDATE findings SET reanalysis_origin=1,reanalyze_requested_at=NULL WHERE id=6`)
	values := planRun(t, s, ref, []int64{99, 2}, planNow)
	if len(values) != 1 || values[0].Finding.ID != 1 || values[0].Intent.Kind != intentRecommended {
		t.Fatalf("丢失崩溃补判或误发抑制项：%+v", values)
	}
	if got := planCount(t, db, `SELECT count(*) FROM findings WHERE id IN (1,2,6) AND recommended_price=10000`); got != 3 {
		t.Fatalf("基线和重新分析没有保存基准：%d", got)
	}
	if got := planCount(t, db, `SELECT count(*) FROM findings WHERE id IN (3,4,5) AND notify_kind IS NULL AND notify_skip_reason IS NULL`); got != 3 {
		t.Fatal("非结论状态做了首次判定")
	}
	if got := planCount(t, db, `SELECT count(*) FROM findings WHERE id=6 AND json_extract(notify_skip_reason,'$.code')='notify.reanalysis'`); got != 1 {
		t.Fatal("排队时间清空后丢失了重新分析来源")
	}
	if planCount(t, db, `SELECT count(*) FROM findings WHERE id=6 AND reanalysis_origin=0`) != 1 {
		t.Fatal("首次抑制决定落盘后没有消费重新分析来源")
	}
	intentID := values[0].Intent.ID
	planExec(t, db, `UPDATE findings SET score=70,grade='fair' WHERE id=1`)
	values = planRun(t, s, ref, nil, planNow+1000)
	if len(values) != 1 || values[0].Intent.ID != intentID || values[0].Finding.NotifyKind == nil ||
		*values[0].Finding.NotifyKind != "recommended" || planCount(t, db, `SELECT count(*) FROM notification_intents`) != 1 {
		t.Fatal("行情重算重复判定或丢弃原待发推荐")
	}
}

func TestPlanRunUnknownPriceUsesMarkersAndKeepsOneIntent(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, nil, nil)
	first := planRun(t, s, ref, nil, planNow)
	second := planRun(t, s, ref, nil, planNow+1)
	if len(first) != 1 || len(second) != 1 || first[0].Intent.ID != second[0].Intent.ID ||
		second[0].Finding.RecommendedPrice != nil || second[0].Finding.NotifyKind == nil ||
		planCount(t, db, `SELECT count(*) FROM notification_intents`) != 1 {
		t.Fatalf("未知价导致重新判定：%+v %+v", first, second)
	}
}

func TestPlanRunFirstDecisionAndIntentRollbackTogether(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), decisionPtr(int64(85)))
	planExec(t, db, `CREATE TRIGGER fail_intent BEFORE INSERT ON notification_intents BEGIN SELECT RAISE(ABORT,'意图写入失败'); END`)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.planRunTx(t.Context(), tx, ref, []int64{1}, planNow)
	tx.Rollback()
	if err == nil || planCount(t, db, `SELECT count(*) FROM findings WHERE notify_kind IS NULL AND notify_skip_reason IS NULL AND recommended_price IS NULL`) != 1 ||
		planCount(t, db, `SELECT count(*) FROM notification_intents`) != 0 {
		t.Fatal("意图失败后留下了半个首次判定")
	}
}

func TestPlanRunConcurrentTransactionsMakeOneFirstIntent(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), decisionPtr(int64(85)))
	planExec(t, db, `CREATE TABLE first_writes(n INTEGER NOT NULL)`)
	planExec(t, db, `CREATE TRIGGER count_first AFTER UPDATE OF notify_kind ON findings WHEN OLD.notify_kind IS NULL AND NEW.notify_kind IS NOT NULL BEGIN INSERT INTO first_writes(n) VALUES(1); END`)
	errorsSeen := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				errorsSeen <- err
				return
			}
			defer tx.Rollback()
			if _, err := s.planRunTx(context.Background(), tx, ref, []int64{1}, planNow); err != nil {
				errorsSeen <- err
				return
			}
			errorsSeen <- tx.Commit()
		})
	}
	workers.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	if planCount(t, db, `SELECT count(*) FROM notification_intents`) != 1 || planCount(t, db, `SELECT count(*) FROM first_writes`) != 1 {
		t.Fatal("并发收尾重复首次判定")
	}
}

func TestPlanRunRejectsOldGenerationAndStaleProcessingRevision(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), decisionPtr(int64(85)))
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	value, err := loadNotificationCandidateTx(t.Context(), tx, 1)
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.NotificationTaskTx(t.Context(), tx, 1)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := decideFirst(task, value, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), `UPDATE findings SET processing_revision=2 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	applied, err := applyFirstDecisionTx(t.Context(), tx, task, value, decision)
	if err != nil || applied {
		t.Fatalf("过期工作版本仍写入：%v %v", applied, err)
	}
	tx.Rollback()
	planExec(t, db, `UPDATE tasks SET search_generation=2 WHERE id=1`)
	tx, err = db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.planRunTx(t.Context(), tx, ref, []int64{1}, planNow)
	tx.Rollback()
	var conflict *service.ServiceError
	if !errors.As(err, &conflict) || conflict.Kind != service.ErrorConflict || planCount(t, db, `SELECT count(*) FROM notification_intents`) != 0 {
		t.Fatal("旧代运行仍判定或规划通知")
	}
	planExec(t, db, `UPDATE findings SET notify_kind='recommended',recommended_price=10000 WHERE id=1`)
	planExec(t, db, `INSERT INTO notification_intents(finding_id,task_id,kind,state,created_at,updated_at,price) VALUES(1,1,'recommended','pending',1,1,10000)`)
	ref.SearchGeneration = 2
	if got := planRun(t, s, ref, nil, planNow); len(got) != 0 || planCount(t, db, `SELECT count(*) FROM notification_intents WHERE state='cancelled'`) != 1 {
		t.Fatal("旧代待发意图没有取消")
	}
}

func TestPlanRunPendingRecommendationTracksNewPriceWithoutDrop(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), decisionPtr(int64(85)))
	first := planRun(t, s, ref, nil, planNow)
	planExec(t, db, `UPDATE notification_intents SET failure_count=1,first_failed_at=? WHERE id=?`, planNow, first[0].Intent.ID)
	planExec(t, db, `UPDATE listings SET price=9000 WHERE item_id='item-1'`)
	second := planRun(t, s, ref, nil, planNow+1000)
	if len(second) != 1 || second[0].Intent.ID != first[0].Intent.ID || second[0].Intent.Kind != intentRecommended ||
		second[0].Intent.Price == nil || *second[0].Intent.Price != 9000 || second[0].Intent.FailureCount != 1 ||
		second[0].Intent.FirstFailedAt == nil || *second[0].Intent.FirstFailedAt != planNow ||
		planCount(t, db, `SELECT count(*) FROM notification_intents`) != 1 {
		t.Fatalf("待发推荐降价产生第二事件或清空窗口：%+v", second)
	}
}

func TestPlanRunDoesNotRewriteIntentAlreadySending(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), decisionPtr(int64(85)))
	first := planRun(t, s, ref, nil, planNow)
	s.intentInFlight = map[int64]struct{}{first[0].Intent.ID: {}}
	planExec(t, db, `UPDATE listings SET price=9000 WHERE item_id='item-1'`)
	if got := planRun(t, s, ref, nil, planNow+1); len(got) != 0 ||
		planCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=? AND price=10000`, first[0].Intent.ID) != 1 {
		t.Fatal("另一轮改写了正在发送的实际价格快照")
	}
	delete(s.intentInFlight, first[0].Intent.ID)
	if got := planRun(t, s, ref, nil, planNow+2); len(got) != 1 || *got[0].Intent.Price != 9000 {
		t.Fatal("结束发送后没有重新接纳最新价格")
	}
}

func TestPlanRunPriceDropUsesSuccessfulBaselineWithoutNewPricePoint(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), decisionPtr(int64(85)))
	planExec(t, db, `UPDATE findings SET is_baseline=1 WHERE id=1`)
	if got := planRun(t, s, ref, nil, planNow); len(got) != 0 {
		t.Fatal("基线发出了首次推荐")
	}
	for index, price := range []int64{9000, 11000, 9500, 8500} {
		planExec(t, db, `UPDATE listings SET price=? WHERE item_id='item-1'`, price)
		// 普通降价无需继续达到首次推荐的分数。
		planExec(t, db, `UPDATE findings SET score=72,grade='fair' WHERE id=1`)
		values := planRun(t, s, ref, nil, planNow+int64(index+1))
		if index == 1 || index == 2 {
			if len(values) != 0 {
				t.Fatalf("价格 %d 高于成功基准仍提醒", price)
			}
			continue
		}
		previous := int64(10000)
		if index == 3 {
			previous = 9000
		}
		if len(values) != 1 || values[0].Intent.Kind != intentPriceDrop || *values[0].Intent.PreviousPrice != previous || *values[0].Intent.Price != price {
			t.Fatalf("价格 %d 的基准错误：%+v", price, values)
		}
		planExec(t, db, `UPDATE notification_intents SET state='sent',sent_at=? WHERE id=?`, planNow+int64(index+1), values[0].Intent.ID)
		planExec(t, db, `UPDATE findings SET last_drop_notified_price=?,drop_notified_at=? WHERE id=1`, price, planNow+int64(index+1))
	}
	if planCount(t, db, `SELECT count(*) FROM price_points`) != 0 || planCount(t, db, `SELECT count(*) FROM notification_intents WHERE state='sent' AND kind='price_drop'`) != 2 {
		t.Fatal("降价依赖了本轮价格点，或成功次数不对")
	}
}

func TestPlanRunPriceChangesRequireKnownPriceWithinBudget(t *testing.T) {
	for _, kind := range []intentKind{intentPriceDrop, intentPriceUpgrade} {
		t.Run(string(kind), func(t *testing.T) {
			s, db, ref := planFixture(t)
			score := int64(72)
			var recommendedPrice *int64
			if kind == intentPriceDrop {
				score = 85
				recommendedPrice = decisionPtr(int64(10000))
			}
			planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), &score)
			planExec(t, db, `UPDATE findings SET is_baseline=1 WHERE id=1`)
			if got := planRun(t, s, ref, nil, planNow); len(got) != 0 {
				t.Fatal("基线发出了首次推荐")
			}
			planExec(t, db, `UPDATE tasks SET budget_max=7000 WHERE id=1`)
			planExec(t, db, `UPDATE findings SET score=85,grade='good' WHERE id=1`)
			for _, price := range []*int64{nil, decisionPtr(int64(9000))} {
				planExec(t, db, `UPDATE listings SET price=? WHERE item_id='item-1'`, price)
				if got := planRun(t, s, ref, nil, planNow+1); len(got) != 0 || planCount(t, db, `SELECT count(*) FROM notification_intents`) != 0 {
					t.Fatal("价格未知或超预算时创建了降价通知")
				}
				if planCount(t, db, `SELECT count(*) FROM findings WHERE last_drop_notified_price IS NULL AND recommended_price IS ?`, recommendedPrice) != 1 {
					t.Fatal("预算未通过时改写了降价基准")
				}
			}
			planExec(t, db, `UPDATE listings SET price=7000 WHERE item_id='item-1'`)
			got := planRun(t, s, ref, nil, planNow+2)
			if len(got) != 1 || got[0].Intent.Kind != kind || got[0].Intent.Price == nil || *got[0].Intent.Price != 7000 ||
				got[0].Intent.PreviousPrice == nil || *got[0].Intent.PreviousPrice != 10000 {
				t.Fatalf("到达预算上限后没有按原基准生成通知：%+v", got)
			}
		})
	}
}

func TestPlanRunPendingPriceChangesResumeWithinBudget(t *testing.T) {
	for _, kind := range []intentKind{intentPriceDrop, intentPriceUpgrade} {
		t.Run(string(kind), func(t *testing.T) {
			s, db, ref := planFixture(t)
			planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(9000)), decisionPtr(int64(85)))
			planExec(t, db, `UPDATE findings SET recommended_price=10000,scored_price=10000,notify_skip_reason='{"code":"notify.baseline","args":{}}',reanalysis_origin=?`, kind == intentPriceUpgrade)
			planExec(t, db, `INSERT INTO notification_intents(finding_id,task_id,kind,state,created_at,updated_at,price,previous_price,failure_count,first_failed_at)
				VALUES(1,1,?,'pending',?,?,9000,10000,1,?)`, kind, planNow, planNow, planNow)
			planExec(t, db, `UPDATE tasks SET budget_max=7000 WHERE id=1`)
			for _, price := range []*int64{decisionPtr(int64(9000)), nil} {
				planExec(t, db, `UPDATE listings SET price=? WHERE item_id='item-1'`, price)
				if got := planRun(t, s, ref, nil, planNow+1); len(got) != 0 {
					t.Fatal("价格未知或超预算时仍重试降价通知")
				}
				if planCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=1 AND state='pending' AND failure_count=1 AND first_failed_at=? AND price=9000`, planNow) != 1 ||
					planCount(t, db, `SELECT count(*) FROM findings WHERE recommended_price=10000 AND last_drop_notified_price IS NULL`) != 1 {
					t.Fatal("预算未通过时改写了重试记录或降价基准")
				}
			}
			planExec(t, db, `UPDATE listings SET price=7000 WHERE item_id='item-1'`)
			got := planRun(t, s, ref, nil, planNow+2)
			if len(got) != 1 || got[0].Intent.ID != 1 || got[0].Intent.Kind != kind || got[0].Intent.Price == nil ||
				*got[0].Intent.Price != 7000 || got[0].Intent.FailureCount != 1 {
				t.Fatalf("到达预算上限后没有沿用原重试记录：%+v", got)
			}
		})
	}
}

func TestPlanRunPriceUpgradeSurvivesRetry(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), decisionPtr(int64(72)))
	if got := planRun(t, s, ref, nil, planNow); len(got) != 0 {
		t.Fatal("原低分发出了推荐")
	}
	planExec(t, db, `UPDATE listings SET price=9000 WHERE item_id='item-1'`)
	planExec(t, db, `UPDATE findings SET score=85,grade='good' WHERE id=1`)
	first := planRun(t, s, ref, nil, planNow+1)
	if len(first) != 1 || first[0].Intent.Kind != intentPriceUpgrade || *first[0].Intent.PreviousPrice != 10000 ||
		*first[0].Finding.RecommendedPrice != 9000 || first[0].Finding.NotifyKind != nil {
		t.Fatalf("升档没保存独立耐久意图：%+v", first)
	}
	planExec(t, db, `UPDATE notification_intents SET failure_count=1,first_failed_at=? WHERE id=?`, planNow+1, first[0].Intent.ID)
	second := planRun(t, s, ref, nil, planNow+2)
	if len(second) != 1 || second[0].Intent.ID != first[0].Intent.ID || second[0].Intent.Kind != intentPriceUpgrade || second[0].Intent.FailureCount != 1 {
		t.Fatal("升档写基准后丢失了待发意图")
	}
}

func TestPlanRunPriceUpgradeStillRecordsDisabledBaseline(t *testing.T) {
	for _, policy := range []string{"none", "good"} {
		t.Run(policy, func(t *testing.T) {
			s, db, ref := planFixture(t)
			planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(9000)), decisionPtr(int64(85)))
			planExec(t, db, `UPDATE tasks SET notify_policy=?,notify_price_drop=0 WHERE id=1`, policy)
			planExec(t, db, `UPDATE findings SET scored_price=10000,notify_skip_reason='{"code":"notify.below_good","args":{"score":72}}' WHERE id=1`)
			if got := planRun(t, s, ref, nil, planNow); len(got) != 0 || planCount(t, db, `SELECT count(*) FROM findings WHERE recommended_price=9000`) != 1 {
				t.Fatal("关闭降价提醒后没有保存升档基准")
			}
		})
	}
}

func TestPlanRunRetriesArePerIntentAndStopPastWindow(t *testing.T) {
	const day = int64(24 * time.Hour / time.Millisecond)
	for _, test := range []struct {
		name       string
		failures   int64
		first, now int64
		send       bool
	}{
		{"第三次前", 2, planNow, planNow + day, true},
		{"三次失败", 3, planNow, planNow + 1, false},
		{"刚好二十四小时", 1, planNow, planNow + day, true},
		{"超过二十四小时一毫秒", 1, planNow, planNow + day + 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, db, ref := planFixture(t)
			planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), decisionPtr(int64(85)))
			first := planRun(t, s, ref, nil, planNow)
			planExec(t, db, `UPDATE notification_intents SET failure_count=?,first_failed_at=? WHERE id=?`, test.failures, test.first, first[0].Intent.ID)
			values := planRun(t, s, ref, nil, test.now)
			if (len(values) == 1) != test.send {
				t.Fatalf("窗口结果错误：%+v", values)
			}
			if !test.send && (planCount(t, db, `SELECT count(*) FROM notification_intents WHERE state='abandoned'`) != 1 ||
				planCount(t, db, `SELECT count(*) FROM findings WHERE json_extract(notify_skip_reason,'$.code')='notify.retry_exhausted'`) != 1) {
				t.Fatal("达到上限没有保存终止状态")
			}
		})
	}
}

func TestPlanRunDoesNotReviveCancelledIntent(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), decisionPtr(int64(85)))
	first := planRun(t, s, ref, nil, planNow)
	planExec(t, db, `UPDATE listings SET is_dismissed=1 WHERE item_id='item-1'`)
	if got := planRun(t, s, ref, nil, planNow+1); len(got) != 0 {
		t.Fatal("不感兴趣仍发通知")
	}
	planExec(t, db, `UPDATE listings SET is_dismissed=0 WHERE item_id='item-1'`)
	if got := planRun(t, s, ref, nil, planNow+2); len(got) != 0 || planCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=? AND state='cancelled'`, first[0].Intent.ID) != 1 ||
		planCount(t, db, `SELECT count(*) FROM notification_intents`) != 1 {
		t.Fatal("撤销不感兴趣复活了旧提醒")
	}
}

func TestPlanRunAbandonedDropCannotRestartAtSamePrice(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(9000)), decisionPtr(int64(85)))
	planExec(t, db, `UPDATE findings SET recommended_price=10000,notify_skip_reason='{"code":"notify.baseline","args":{}}' WHERE id=1`)
	planExec(t, db, `INSERT INTO notification_intents(finding_id,task_id,kind,state,created_at,updated_at,price,previous_price,failure_count,first_failed_at)
		VALUES(1,1,'price_drop','abandoned',1,1,9000,10000,3,1)`)
	for _, price := range []int64{9000, 9500, 11000, 9500} {
		planExec(t, db, `UPDATE listings SET price=? WHERE item_id='item-1'`, price)
		if got := planRun(t, s, ref, nil, planNow); len(got) != 0 || planCount(t, db, `SELECT count(*) FROM notification_intents`) != 1 {
			t.Fatal("相同或更高价绕过放弃上限重新建了意图")
		}
	}
	planExec(t, db, `UPDATE listings SET price=8500 WHERE item_id='item-1'`)
	values := planRun(t, s, ref, nil, planNow+1)
	if len(values) != 1 || values[0].Intent.Kind != intentPriceDrop || values[0].Intent.FailureCount != 0 ||
		values[0].Intent.FirstFailedAt != nil || *values[0].Intent.PreviousPrice != 10000 {
		t.Fatal("新的更低价沿用了旧意图的失败窗口")
	}
}

func TestPlanRunPendingDropUpdatesCurrentPriceWithinOriginalWindow(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(9000)), decisionPtr(int64(85)))
	planExec(t, db, `UPDATE findings SET recommended_price=10000,notify_skip_reason='{"code":"notify.baseline","args":{}}' WHERE id=1`)
	first := planRun(t, s, ref, nil, planNow)
	planExec(t, db, `UPDATE notification_intents SET failure_count=2,first_failed_at=? WHERE id=?`, planNow, first[0].Intent.ID)
	for index, price := range []int64{8500, 8800} {
		planExec(t, db, `UPDATE listings SET price=? WHERE item_id='item-1'`, price)
		values := planRun(t, s, ref, nil, planNow+int64(index+1))
		if len(values) != 1 || values[0].Intent.ID != first[0].Intent.ID || values[0].Intent.Kind != intentPriceDrop ||
			*values[0].Intent.Price != price || *values[0].Intent.PreviousPrice != 10000 || values[0].Intent.FailureCount != 2 ||
			values[0].Intent.FirstFailedAt == nil || *values[0].Intent.FirstFailedAt != planNow ||
			*values[0].Finding.RecommendedPrice != 10000 || values[0].Finding.LastDropNotifiedPrice != nil || values[0].Finding.DropNotifiedAt != nil ||
			planCount(t, db, `SELECT count(*) FROM notification_intents`) != 1 {
			t.Fatalf("待发降价变价没有沿原窗口更新实价：%+v", values)
		}
	}
	s.intentInFlight = map[int64]struct{}{first[0].Intent.ID: {}}
	planExec(t, db, `UPDATE listings SET price=8000 WHERE item_id='item-1'`)
	if got := planRun(t, s, ref, nil, planNow+3); len(got) != 0 ||
		planCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=? AND price=8800 AND failure_count=2`, first[0].Intent.ID) != 1 {
		t.Fatal("变价规划改写了正在发送的降价快照")
	}
}

func TestPlanRunPendingDropNewPriceCannotRestartExpiredWindow(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(9000)), decisionPtr(int64(85)))
	planExec(t, db, `UPDATE findings SET recommended_price=10000,notify_skip_reason='{"code":"notify.baseline","args":{}}' WHERE id=1`)
	first := planRun(t, s, ref, nil, planNow)
	planExec(t, db, `UPDATE notification_intents SET failure_count=1,first_failed_at=? WHERE id=?`, planNow, first[0].Intent.ID)
	planExec(t, db, `UPDATE listings SET price=8500 WHERE item_id='item-1'`)
	const day = int64(24 * time.Hour / time.Millisecond)
	values := planRun(t, s, ref, nil, planNow+day)
	if len(values) != 1 || values[0].Intent.ID != first[0].Intent.ID || *values[0].Intent.FirstFailedAt != planNow || *values[0].Intent.Price != 8500 {
		t.Fatal("变价重开了二十四小时窗口")
	}
	if got := planRun(t, s, ref, nil, planNow+day+1); len(got) != 0 ||
		planCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=? AND state='abandoned' AND price=8500 AND first_failed_at=?`, first[0].Intent.ID, planNow) != 1 {
		t.Fatal("原窗口过期后仍发送新价格")
	}
	if got := planRun(t, s, ref, nil, planNow+day+2); len(got) != 0 || planCount(t, db, `SELECT count(*) FROM notification_intents`) != 1 {
		t.Fatal("到期后同价立即开了新窗口")
	}
}

func TestPlanRunPendingUpgradeUpdatesOriginalIntentAndRecommendationPrice(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), decisionPtr(int64(72)))
	planRun(t, s, ref, nil, planNow)
	planExec(t, db, `UPDATE listings SET price=9000 WHERE item_id='item-1'`)
	planExec(t, db, `UPDATE findings SET score=85,grade='good' WHERE id=1`)
	first := planRun(t, s, ref, nil, planNow+1)
	planExec(t, db, `UPDATE notification_intents SET failure_count=1,first_failed_at=? WHERE id=?`, planNow+1, first[0].Intent.ID)
	planExec(t, db, `UPDATE listings SET price=8500 WHERE item_id='item-1'`)
	values := planRun(t, s, ref, nil, planNow+2)
	if len(values) != 1 || values[0].Intent.ID != first[0].Intent.ID || values[0].Intent.Kind != intentPriceUpgrade ||
		*values[0].Intent.Price != 8500 || *values[0].Intent.PreviousPrice != 10000 || values[0].Intent.FailureCount != 1 ||
		*values[0].Intent.FirstFailedAt != planNow+1 || *values[0].Finding.RecommendedPrice != 8500 ||
		values[0].Finding.LastDropNotifiedPrice != nil || values[0].Finding.DropNotifiedAt != nil ||
		planCount(t, db, `SELECT count(*) FROM notification_intents`) != 1 {
		t.Fatalf("待发升档变价丢失原意图或基准未同步：%+v", values)
	}
}

func TestPlanRunAbandonedRecommendationCannotChangeKindAtSamePrice(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), decisionPtr(int64(85)))
	first := planRun(t, s, ref, nil, planNow)
	planExec(t, db, `UPDATE listings SET price=9000 WHERE item_id='item-1'`)
	planExec(t, db, `UPDATE notification_intents SET price=9000,failure_count=3,first_failed_at=? WHERE id=?`, planNow, first[0].Intent.ID)
	if got := planRun(t, s, ref, nil, planNow+1); len(got) != 0 {
		t.Fatal("三次失败后还在发送")
	}
	if got := planRun(t, s, ref, nil, planNow+2); len(got) != 0 || planCount(t, db, `SELECT count(*) FROM notification_intents`) != 1 {
		t.Fatal("同价改成降价意图绕过了推荐的重发上限")
	}
	planExec(t, db, `UPDATE listings SET price=8500 WHERE item_id='item-1'`)
	values := planRun(t, s, ref, nil, planNow+3)
	if len(values) != 1 || values[0].Intent.Kind != intentPriceDrop || values[0].Intent.FailureCount != 0 {
		t.Fatal("更低的新价格没有独立的降价意图")
	}
}

func TestPlanRunPendingDropDoesNotSendStalePriceAfterRebound(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(9000)), decisionPtr(int64(85)))
	planExec(t, db, `UPDATE findings SET recommended_price=10000,notify_skip_reason='{"code":"notify.baseline","args":{}}' WHERE id=1`)
	values := planRun(t, s, ref, nil, planNow)
	if len(values) != 1 {
		t.Fatal("未创建降价意图")
	}
	planExec(t, db, `UPDATE listings SET price=11000 WHERE item_id='item-1'`)
	if got := planRun(t, s, ref, nil, planNow+1); len(got) != 0 {
		t.Fatal("现价已经回涨仍发送旧九十元提醒")
	}
}

func TestPlanRunNoEnabledChannelKeepsBaselineWithoutLaterFirstSend(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), decisionPtr(int64(85)))
	planExec(t, db, `UPDATE channels SET enabled=0 WHERE id=1`)
	if got := planRun(t, s, ref, nil, planNow); len(got) != 0 ||
		planCount(t, db, `SELECT count(*) FROM findings WHERE recommended_price=10000 AND json_extract(notify_skip_reason,'$.code')='notify.no_channel'`) != 1 {
		t.Fatal("无启用渠道没有保存不推原因和推荐基准")
	}
	planExec(t, db, `UPDATE channels SET enabled=1 WHERE id=1`)
	if got := planRun(t, s, ref, nil, planNow+1); len(got) != 0 {
		t.Fatal("启用渠道后重新做首次判定")
	}
	planExec(t, db, `UPDATE listings SET price=9000 WHERE item_id='item-1'`)
	values := planRun(t, s, ref, nil, planNow+2)
	if len(values) != 1 || values[0].Intent.Kind != intentPriceDrop {
		t.Fatal("无渠道时保存的基准没能用于后续降价")
	}
}

func TestPlanRunPendingRecommendationDoesNotSendNewAvoidConclusion(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), decisionPtr(int64(85)))
	first := planRun(t, s, ref, nil, planNow)
	planExec(t, db, `UPDATE findings SET score=NULL,grade='avoid',reanalysis_origin=1,
		reason='{"code":"analysis.risk_confirmed","args":{"name":"ID锁","id":"id_lock","attitude":"confirm"}}' WHERE id=1`)
	if got := planRun(t, s, ref, nil, planNow+1); len(got) != 0 ||
		planCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=? AND state='cancelled' AND json_extract(reason,'$.code')='analysis.risk_confirmed'`, first[0].Intent.ID) != 1 ||
		planCount(t, db, `SELECT count(*) FROM findings WHERE notify_kind='recommended' AND json_extract(notify_skip_reason,'$.code')='analysis.risk_confirmed'`) != 1 {
		t.Fatal("重新分析的不建议未取消原提醒或没有保留具体原因")
	}
}

func TestPlanRunPendingRecommendationCannotBypassNewBudget(t *testing.T) {
	for _, test := range []struct {
		name, code string
		price      *int64
	}{
		{"已知超预算", "notify.over_budget", decisionPtr(int64(10000))},
		{"价格未知", "notify.price_unknown_budget", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, db, ref := planFixture(t)
			planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), decisionPtr(int64(85)))
			first := planRun(t, s, ref, nil, planNow)
			planExec(t, db, `UPDATE tasks SET budget_max=9000 WHERE id=1`)
			planExec(t, db, `UPDATE findings SET reanalysis_origin=1,processing_revision=2 WHERE id=1`)
			planExec(t, db, `UPDATE listings SET price=? WHERE item_id='item-1'`, test.price)
			if got := planRun(t, s, ref, nil, planNow+1); len(got) != 0 ||
				planCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=? AND state='cancelled' AND json_extract(reason,'$.code')=?`, first[0].Intent.ID, test.code) != 1 ||
				planCount(t, db, `SELECT count(*) FROM findings WHERE json_extract(notify_skip_reason,'$.code')=?`, test.code) != 1 {
				t.Fatal("新结论未核对预算或没有保存具体取消原因")
			}
		})
	}
}

func TestPlanRunReanalysisPausesAndResumesOriginalWindow(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), decisionPtr(int64(85)))
	first := planRun(t, s, ref, nil, planNow)
	planExec(t, db, `UPDATE notification_intents SET failure_count=2,first_failed_at=? WHERE id=?`, planNow, first[0].Intent.ID)
	planExec(t, db, `UPDATE findings SET decision='pending',stage='detail',reanalysis_origin=1,processing_revision=2 WHERE id=1`)
	for _, state := range []string{"pending", "failed"} {
		planExec(t, db, `UPDATE findings SET decision=? WHERE id=1`, state)
		if got := planRun(t, s, ref, nil, planNow+1); len(got) != 0 ||
			planCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=? AND state='pending' AND failure_count=2 AND first_failed_at=? AND price=10000`, first[0].Intent.ID, planNow) != 1 {
			t.Fatal("重分析未出结论时没有暂停原提醒")
		}
	}
	planExec(t, db, `UPDATE findings SET decision='scored',stage='done',score=85,grade='good',reanalyze_requested_at=NULL WHERE id=1`)
	planExec(t, db, `UPDATE listings SET price=9000 WHERE item_id='item-1'`)
	values := planRun(t, s, ref, nil, planNow+2)
	if len(values) != 1 || values[0].Intent.ID != first[0].Intent.ID || values[0].Intent.FailureCount != 2 ||
		values[0].Intent.FirstFailedAt == nil || *values[0].Intent.FirstFailedAt != planNow || *values[0].Intent.Price != 9000 ||
		*values[0].Finding.RecommendedPrice != 9000 || values[0].ReanalysisOrigin || planCount(t, db, `SELECT count(*) FROM notification_intents`) != 1 {
		t.Fatalf("新结论开启了新窗口或未更新当前价格：%+v", values)
	}
	planExec(t, db, `UPDATE findings SET score=72,grade='fair' WHERE id=1`)
	values = planRun(t, s, ref, nil, planNow+3)
	if len(values) != 1 || values[0].Intent.ID != first[0].Intent.ID || values[0].ReanalysisOrigin {
		t.Fatal("消费来源后普通行情重算仍在反复检查首次推荐门槛")
	}
}

func TestPlanRunReanalysisCancelsOldRecommendationBelowCurrentThreshold(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), decisionPtr(int64(85)))
	first := planRun(t, s, ref, nil, planNow)
	planExec(t, db, `UPDATE findings SET score=72,grade='fair',reanalysis_origin=1,processing_revision=2 WHERE id=1`)
	if got := planRun(t, s, ref, nil, planNow+1); len(got) != 0 ||
		planCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=? AND state='cancelled' AND json_extract(reason,'$.code')='notify.below_good' AND json_extract(reason,'$.args.score')=72`, first[0].Intent.ID) != 1 ||
		planCount(t, db, `SELECT count(*) FROM findings WHERE reanalysis_origin=0 AND json_extract(notify_skip_reason,'$.code')='notify.below_good'`) != 1 {
		t.Fatal("低于新结论档位的旧推荐没有取消或原因不具体")
	}
}

func TestPlanRunReanalysisKeepsAllNewKindAndAllowedRejectedConclusions(t *testing.T) {
	for _, test := range []struct {
		mode, rejected string
	}{
		{"ai", "unmatched"}, {"keyword", "keyword_miss"},
	} {
		t.Run(test.rejected, func(t *testing.T) {
			s, db, ref := planFixture(t)
			planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), decisionPtr(int64(79)))
			planExec(t, db, `UPDATE tasks SET judge_mode=?,notify_policy='all_new' WHERE id=1`, test.mode)
			if test.mode == "keyword" {
				planExec(t, db, `UPDATE findings SET decision='rejected',reject_kind='keyword_miss',score=NULL,grade=NULL WHERE id=1`)
			}
			first := planRun(t, s, ref, nil, planNow)
			planExec(t, db, `UPDATE findings SET decision='rejected',reject_kind=?,score=NULL,grade=NULL,reanalysis_origin=1,processing_revision=2 WHERE id=1`, test.rejected)
			planExec(t, db, `UPDATE listings SET price=9000 WHERE item_id='item-1'`)
			values := planRun(t, s, ref, nil, planNow+1)
			if len(values) != 1 || values[0].Intent.ID != first[0].Intent.ID || values[0].Intent.Kind != intentNew ||
				*values[0].Intent.Price != 9000 || values[0].Finding.RecommendedPrice != nil || values[0].ReanalysisOrigin ||
				planCount(t, db, `SELECT count(*) FROM notification_intents`) != 1 {
				t.Fatalf("所有新商品被缩掉或重新创建推荐：%+v", values)
			}
		})
	}
}

func TestPlanRunReanalysisSourceConsumptionChecksProcessingRevision(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), decisionPtr(int64(85)))
	first := planRun(t, s, ref, nil, planNow)
	planExec(t, db, `UPDATE findings SET reanalysis_origin=1,processing_revision=2 WHERE id=1`)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	value, err := loadNotificationCandidateTx(t.Context(), tx, 1)
	if err != nil {
		t.Fatal(err)
	}
	value.Listing.Price = decisionPtr(int64(9000))
	if _, err := tx.ExecContext(t.Context(), `UPDATE findings SET processing_revision=3 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	consumed, err := consumeReanalysisOriginTx(t.Context(), tx, value, first[0].Intent, true, planNow+1)
	if err != nil || consumed {
		t.Fatalf("旧工作版本消费了新请求来源：%v %v", consumed, err)
	}
	var origin bool
	var price int64
	if err := tx.QueryRowContext(t.Context(), `SELECT reanalysis_origin,recommended_price FROM findings WHERE id=1`).Scan(&origin, &price); err != nil || !origin || price != 10000 {
		t.Fatal("消费CAS失败仍修改了发现")
	}
	if err := tx.QueryRowContext(t.Context(), `SELECT price FROM notification_intents WHERE id=?`, first[0].Intent.ID).Scan(&price); err != nil || price != 10000 {
		t.Fatal("消费CAS失败仍改写原意图价格")
	}
}

func TestPlanRunUnknownBudgetPriceDoesNotBlockOtherCandidates(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, nil, nil)
	planFinding(t, db, 2, analysis.DecisionScored, decisionPtr(int64(9000)), decisionPtr(int64(85)))
	planExec(t, db, `UPDATE tasks SET budget_max=10000 WHERE id=1`)
	values := planRun(t, s, ref, nil, planNow)
	if len(values) != 1 || values[0].Finding.ID != 2 ||
		planCount(t, db, `SELECT count(*) FROM findings WHERE id=1 AND notify_kind IS NULL AND recommended_price IS NULL AND json_extract(notify_skip_reason,'$.code')='notify.price_unknown_budget'`) != 1 {
		t.Fatal("未知预算价没有独立记录原因或挡住了其他提醒")
	}
}

func TestPlanRunReanalysisCancellationStoresSpecificReasons(t *testing.T) {
	for _, test := range []struct {
		name, code string
		edits      []string
	}{
		{"不感兴趣", "notify.dismissed", []string{`UPDATE listings SET is_dismissed=1 WHERE item_id='item-1'`}},
		{"屏蔽卖家", "notify.blocked", []string{`UPDATE listings SET seller_id='seller-1' WHERE item_id='item-1'`, `INSERT INTO blocked_sellers(seller_id) VALUES('seller-1')`}},
		{"不推送", "notify.policy_none", []string{`UPDATE tasks SET notify_policy='none' WHERE id=1`}},
		{"没有启用渠道", "notify.no_channel", []string{`UPDATE channels SET enabled=0 WHERE id=1`}},
		{"只推捡漏", "notify.below_steal", []string{`UPDATE tasks SET notify_policy='steal' WHERE id=1`}},
		{"用户硬条件", "analysis.filter_shipping", []string{`UPDATE findings SET decision='rejected',reject_kind='filter',reason='{"code":"analysis.filter_shipping","args":{}}' WHERE id=1`}},
		{"垃圾类型", "analysis.junk.wanted", []string{`UPDATE findings SET decision='rejected',reject_kind='junk',reason='{"code":"analysis.junk.wanted","args":{}}' WHERE id=1`}},
		{"不符合需求", "analysis.spec_mismatch", []string{`UPDATE findings SET decision='rejected',reject_kind='unmatched',reason='{"code":"analysis.spec_mismatch","args":{"name":"型号","value":"mini 5"}}' WHERE id=1`}},
		{"所有新商品不转换原推荐", "analysis.spec_mismatch", []string{`UPDATE tasks SET notify_policy='all_new' WHERE id=1`, `UPDATE findings SET decision='rejected',reject_kind='unmatched',reason='{"code":"analysis.spec_mismatch","args":{"name":"型号","value":"mini 5"}}' WHERE id=1`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, db, ref := planFixture(t)
			planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), decisionPtr(int64(85)))
			first := planRun(t, s, ref, nil, planNow)
			planExec(t, db, `UPDATE notification_intents SET failure_count=1,first_failed_at=? WHERE id=?`, planNow, first[0].Intent.ID)
			planExec(t, db, `UPDATE findings SET reanalysis_origin=1,processing_revision=2 WHERE id=1`)
			for _, edit := range test.edits {
				planExec(t, db, edit)
			}
			if got := planRun(t, s, ref, nil, planNow+1); len(got) != 0 ||
				planCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=? AND state='cancelled' AND failure_count=1 AND first_failed_at=? AND json_extract(reason,'$.code')=?`, first[0].Intent.ID, planNow, test.code) != 1 ||
				planCount(t, db, `SELECT count(*) FROM findings WHERE reanalysis_origin=0 AND json_extract(notify_skip_reason,'$.code')=?`, test.code) != 1 {
				t.Fatal("重分析取消没有保留具体原因、原失败窗口或来源没有消费")
			}
		})
	}
}

func TestPlanRunReanalysisCancelledUpgradePreservesEarlierSuccess(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), decisionPtr(int64(72)))
	planRun(t, s, ref, nil, planNow)
	planExec(t, db, `UPDATE findings SET score=85,grade='good',notified_at=? WHERE id=1`, planNow-1)
	planExec(t, db, `UPDATE listings SET price=9000 WHERE item_id='item-1'`)
	first := planRun(t, s, ref, nil, planNow+1)
	if len(first) != 1 || first[0].Intent.Kind != intentPriceUpgrade {
		t.Fatal("没有创建升档意图")
	}
	planExec(t, db, `UPDATE findings SET reanalysis_origin=1,processing_revision=2,score=NULL,grade='avoid',
		reason='{"code":"analysis.risk_confirmed","args":{"name":"ID锁","id":"id_lock","attitude":"confirm"}}' WHERE id=1`)
	if got := planRun(t, s, ref, nil, planNow+2); len(got) != 0 ||
		planCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=? AND state='cancelled'`, first[0].Intent.ID) != 1 ||
		planCount(t, db, `SELECT count(*) FROM findings WHERE notified_at=? AND reanalysis_origin=0 AND json_extract(notify_skip_reason,'$.code')='analysis.risk_confirmed'`, planNow-1) != 1 {
		t.Fatal("取消旧升档没有写具体原因或抹掉了更早的真实送达")
	}
}

func TestPlanRunReanalysisAllNewLowScoreRespectsOriginalIntentKind(t *testing.T) {
	for _, kind := range []intentKind{intentRecommended, intentNew} {
		t.Run(string(kind), func(t *testing.T) {
			s, db, ref := planFixture(t)
			score := int64(85)
			if kind == intentNew {
				score = 79
				planExec(t, db, `UPDATE tasks SET notify_policy='all_new' WHERE id=1`)
			}
			planFinding(t, db, 1, analysis.DecisionScored, decisionPtr(int64(10000)), &score)
			first := planRun(t, s, ref, nil, planNow)
			planExec(t, db, `UPDATE notification_intents SET failure_count=1,first_failed_at=? WHERE id=?`, planNow, first[0].Intent.ID)
			planExec(t, db, `UPDATE tasks SET notify_policy='all_new' WHERE id=1`)
			planExec(t, db, `UPDATE findings SET score=72,grade='fair',reanalysis_origin=1,processing_revision=2 WHERE id=1`)
			values := planRun(t, s, ref, nil, planNow+1)
			if kind == intentNew {
				if len(values) != 1 || values[0].Intent.ID != first[0].Intent.ID || values[0].Intent.Kind != intentNew || values[0].Intent.FailureCount != 1 {
					t.Fatal("原新商品在所有新商品设置下被取消或改了种类")
				}
			} else if len(values) != 0 || planCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=? AND kind='recommended' AND state='cancelled'
				AND failure_count=1 AND first_failed_at=? AND json_extract(reason,'$.code')='notify.reanalysis_below_recommendation'
				AND json_extract(reason,'$.args.score')=72`, first[0].Intent.ID, planNow) != 1 ||
				planCount(t, db, `SELECT count(*) FROM findings WHERE notify_kind='recommended' AND json_extract(notify_skip_reason,'$.code')='notify.reanalysis_below_recommendation'`) != 1 {
				t.Fatal("旧推荐未沿原种类取消或原因不符合实际设置")
			}
			if planCount(t, db, `SELECT count(*) FROM notification_intents`) != 1 {
				t.Fatal("重新分析创建了第二条意图")
			}
		})
	}
}
