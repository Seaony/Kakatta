package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func seedRunRecommendation(t *testing.T, s *Service, run *runExecution, id int64, decision string, score any, grade any) {
	t.Helper()
	itemID := fmt.Sprintf("recommendation-%d", id)
	finishExec(t, s.db, `INSERT INTO listings(item_id,title,price,desktop_url,first_seen_at,last_seen_at)
		VALUES(?,'商品',10000,'本地商品链接',1,1)`, itemID)
	finishExec(t, s.db, `INSERT INTO findings(id,task_id,item_id,search_generation,processing_revision,found_at,
		is_baseline,decision,stage,score,grade,is_read) VALUES(?,1,?,1,1,1,0,?,'done',?,?,0)`, id, itemID, decision, score, grade)
	run.FirstConclusionIDs = append(run.FirstConclusionIDs, id)
	run.P = append(run.P, WorkRef{RunRef: run.Ref, FindingID: id, ProcessingRevision: 1})
}

func TestRunRecommendedCountUsesCompletedResultsInsteadOfDelivery(t *testing.T) {
	for _, cause := range []error{nil, web.ErrNeedVerify, errRunTimeout, errors.New("普通页面失败")} {
		t.Run(fmt.Sprint(cause), func(t *testing.T) {
			s, run, notifier, _ := finishFixture(t)
			run.FirstConclusionIDs = nil
			finishExec(t, s.db, `UPDATE tasks SET notify_policy='none' WHERE id=1`)
			seedRunRecommendation(t, s, run, 1, "matched", nil, nil)
			finishExec(t, s.db, `UPDATE findings SET is_baseline=1,notify_skip_reason='{"code":"findings.baseline","args":{}}' WHERE id=1`)
			// 切换模式保留既有结论，统计依据实际结论类型，而不是现在的任务开关。
			seedRunRecommendation(t, s, run, 2, "scored", 80, "good")
			seedRunRecommendation(t, s, run, 3, "scored", 79, "fair")
			finishExec(t, s.db, `UPDATE findings SET notify_kind='recommended',notified_at=200 WHERE id=3`)
			seedRunRecommendation(t, s, run, 4, "scored", nil, nil)
			seedRunRecommendation(t, s, run, 5, "scored", nil, "avoid")
			seedRunRecommendation(t, s, run, 6, "scored", 95, "avoid")
			seedRunRecommendation(t, s, run, 7, "matched", nil, nil)
			finishExec(t, s.db, `UPDATE listings SET is_dismissed=1 WHERE item_id='recommendation-7'`)
			seedRunRecommendation(t, s, run, 8, "matched", nil, nil)
			finishExec(t, s.db, `UPDATE listings SET seller_id='blocked' WHERE item_id='recommendation-8'`)
			finishExec(t, s.db, `INSERT INTO blocked_sellers(seller_id) VALUES('blocked')`)
			seedRunRecommendation(t, s, run, 9, "rejected", nil, nil)
			seedRunRecommendation(t, s, run, 10, "skipped", nil, nil)
			seedRunRecommendation(t, s, run, 11, "failed", 99, "steal")
			seedRunRecommendation(t, s, run, 12, "pending", 99, "steal")
			seedRunRecommendation(t, s, run, 13, "matched", nil, nil)
			finishExec(t, s.db, `UPDATE findings SET reanalysis_origin=1,notified_at=100,notify_skip_reason='{"code":"fixture.reanalysis","args":{}}' WHERE id=13`)
			seedRunRecommendation(t, s, run, 14, "matched", nil, nil)
			finishExec(t, s.db, `UPDATE findings SET processing_revision=2 WHERE id=14`)
			run.FirstConclusionIDs = append(run.FirstConclusionIDs, 1, 2, 13)
			// 不在本次首次结论集合里的历史推荐，不能顺手全任务计入。
			seedRunRecommendation(t, s, run, 15, "matched", nil, nil)
			run.FirstConclusionIDs = run.FirstConclusionIDs[:len(run.FirstConclusionIDs)-1]
			if err := s.finishRun(t.Context(), run, cause); err != nil {
				t.Fatal(err)
			}
			got := finishScalar[int64](t, s.db, `SELECT recommended_count FROM run_records WHERE id=1`)
			if got != 4 || run.RecommendedCount != 4 || run.Record.RecommendedCount == nil || *run.Record.RecommendedCount != 4 {
				t.Fatalf("已完成推荐应去重计4条，与通知无关：db=%d execution=%d record=%v", got, run.RecommendedCount, run.Record.RecommendedCount)
			}
			if notifier.calls != 1 || finishScalar[int64](t, s.db, `SELECT notified_count FROM run_records WHERE id=1`) != 0 {
				t.Fatal("推荐统计混入送达计数")
			}
		})
	}
}

func TestRunRecommendedCountReadsFinalBudgetAndVisibility(t *testing.T) {
	s, run, notifier, _ := finishFixture(t)
	run.FirstConclusionIDs = nil
	for id := int64(1); id <= 4; id++ {
		seedRunRecommendation(t, s, run, id, "matched", nil, nil)
	}
	finishExec(t, s.db, `UPDATE listings SET price=10001 WHERE item_id='recommendation-2'`)
	finishExec(t, s.db, `UPDATE listings SET price=NULL WHERE item_id='recommendation-3'`)
	notifier.flush = func(context.Context, RunRef, []int64) (NotifyResult, error) {
		finishExec(t, s.db, `UPDATE tasks SET budget_max=10000 WHERE id=1`)
		finishExec(t, s.db, `UPDATE listings SET is_dismissed=1 WHERE item_id='recommendation-4'`)
		return NotifyResult{NotifiedCount: 99}, nil
	}
	if err := s.finishRun(t.Context(), run, nil); err != nil {
		t.Fatal(err)
	}
	if got := finishScalar[int64](t, s.db, `SELECT recommended_count FROM run_records WHERE id=1`); got != 1 {
		t.Fatalf("应按最终预算/可见性仅计边界价一条，不能用通知数99：%d", got)
	}
}

func TestRunRecommendedCountExcludesOtherTaskGenerationAndUncapturedWork(t *testing.T) {
	s, run, _, _ := finishFixture(t)
	run.FirstConclusionIDs = nil
	for id := int64(1); id <= 4; id++ {
		seedRunRecommendation(t, s, run, id, "matched", nil, nil)
	}
	finishExec(t, s.db, `INSERT INTO tasks(id,name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index)
		VALUES(2,'其他任务','商品',1,30,'keyword','none',1)`)
	finishExec(t, s.db, `UPDATE findings SET task_id=2 WHERE id=2`)
	finishExec(t, s.db, `UPDATE findings SET search_generation=2 WHERE id=3`)
	run.P = run.P[:3]
	if err := s.finishRun(t.Context(), run, nil); err != nil {
		t.Fatal(err)
	}
	if got := finishScalar[int64](t, s.db, `SELECT recommended_count FROM run_records WHERE id=1`); got != 1 {
		t.Fatalf("其他任务/代次/不属于本次处理的结论混入统计：%d", got)
	}
}

func TestRunRecommendedCountRetainsCompletedOldGenerationAfterCancellation(t *testing.T) {
	s, run, notifier, now := finishFixture(t)
	run.FirstConclusionIDs = nil
	for id := int64(1); id <= 3; id++ {
		seedRunRecommendation(t, s, run, id, "matched", nil, nil)
	}
	finishExec(t, s.db, `UPDATE tasks SET search_generation=2,next_run_at=?,baseline_pending=1 WHERE id=1`, now.Add(12*time.Hour).UnixMilli())
	finishExec(t, s.db, `UPDATE findings SET search_generation=2 WHERE id=2`)
	finishExec(t, s.db, `UPDATE findings SET processing_revision=2 WHERE id=3`)
	seedRunRecommendation(t, s, run, 4, "scored", 20, "avoid")
	prepareRunRecommendationAI(t, s, 4)
	if err := s.finishRun(t.Context(), run, errSearchChanged); err != nil {
		t.Fatal(err)
	}
	if got := finishScalar[int64](t, s.db, `SELECT recommended_count FROM run_records WHERE id=1`); got != 1 {
		t.Fatalf("取消后应计已落地旧代结论，不能混新代/新工作版本：%d", got)
	}
	if notifier.calls != 0 || finishScalar[int64](t, s.db, `SELECT next_run_at FROM tasks WHERE id=1`) != now.Add(12*time.Hour).UnixMilli() {
		t.Fatal("为了统计续做旧代通知或覆盖新计划")
	}
	if finishScalar[int64](t, s.db, `SELECT score FROM findings WHERE id=4`) != 20 {
		t.Fatal("取消时为了统计重新计算了旧代分数")
	}
}

func prepareRunRecommendationAI(t *testing.T, s *Service, id int64) {
	t.Helper()
	profile := analysis.Profile{Specs: []analysis.ProfileSpec{{Name: "型号", Options: []string{"a"}, Values: []string{"a"}, Source: analysis.SourceUser, Edited: true}}, NextNo: 1}
	rawProfile, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	value := "a"
	rawFacts, err := encodeFindingFacts(analysis.Facts{Specs: []analysis.SpecValue{{Name: "型号", Value: &value}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	finishExec(t, s.db, `UPDATE tasks SET judge_mode='ai',profile_json=?,notify_policy='none' WHERE id=1`, string(rawProfile))
	finishExec(t, s.db, `UPDATE findings SET analysis_json=?,decided_layer=3 WHERE id=?`, rawFacts, id)
	finishExec(t, s.db, `INSERT INTO task_item_specs(task_id,item_id,kind,specs_json,config_key,source,updated_at)
		VALUES(1,?,'sale','[{"name":"型号","value":"a"}]','型号=a','detail',1)`, fmt.Sprintf("recommendation-%d", id))
}

func TestRunRecommendedCountUsesScoreAfterRunRecalculation(t *testing.T) {
	s, run, _, _ := finishFixture(t)
	run.FirstConclusionIDs = nil
	seedRunRecommendation(t, s, run, 1, "scored", 20, "avoid")
	prepareRunRecommendationAI(t, s, 1)
	if err := s.finishRun(t.Context(), run, nil); err != nil {
		t.Fatal(err)
	}
	if !finishScalar[bool](t, s.db, `SELECT score IS NULL AND grade IS NULL FROM findings WHERE id=1`) {
		t.Fatal("缺样本时应重算为无法比价")
	}
	if got := finishScalar[int64](t, s.db, `SELECT recommended_count FROM run_records WHERE id=1`); got != 1 {
		t.Fatalf("统计没有使用运行重算后无法比价的推荐资格：%d", got)
	}
}

func TestRunRecommendedCountDoesNotAdvanceWhenFinalTransactionFails(t *testing.T) {
	s, run, _, _ := finishFixture(t)
	run.FirstConclusionIDs = nil
	seedRunRecommendation(t, s, run, 1, "matched", nil, nil)
	finishExec(t, s.db, `CREATE TRIGGER reject_recommendation_finish BEFORE UPDATE ON run_records BEGIN SELECT RAISE(FAIL,'blocked'); END`)
	if err := s.finishRun(t.Context(), run, nil); err == nil {
		t.Fatal("结束事务失败仍返回成功")
	}
	if run.RecommendedCount != 0 || run.Record.RecommendedCount != nil || run.Record.EndedAt != nil {
		t.Fatal("结束事务失败推进了内存推荐统计")
	}
	if !finishScalar[bool](t, s.db, `SELECT ended_at IS NULL AND recommended_count IS NULL FROM run_records WHERE id=1`) {
		t.Fatal("结束事务失败仍保存推荐统计")
	}
}
