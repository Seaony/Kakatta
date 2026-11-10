package service

import (
	"reflect"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
)

func TestBacklogExpiryUsesRequestTimeAndKeepsAwaitAI(t *testing.T) {
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeAI, false)
	day := int64(24 * 60 * 60 * 1000)
	expired := h.finding(ref, "expired", StageTitle, DecisionPending, h.now-day-1)
	boundary := h.finding(ref, "boundary", StageTitle, DecisionPending, h.now-day)
	requested := h.finding(ref, "requested", StageTitle, DecisionPending, h.now-2*day)
	await := h.finding(ref, "await", StageDetail, DecisionPending, h.now-3*day)
	retry := h.finding(ref, "retry", StageDetail, DecisionFailed, h.now-1)
	exhausted := h.finding(ref, "exhausted", StageTitle, DecisionFailed, h.now)
	permanent := h.finding(ref, "permanent", StageTitle, DecisionFailed, h.now)
	oldRef := ref
	oldRef.SearchGeneration++
	h.finding(oldRef, "old-generation", StageTitle, DecisionPending, h.now)
	h.exec(`UPDATE findings SET reanalyze_requested_at=? WHERE id=?`, h.now-1, requested.FindingID)
	h.exec(`UPDATE findings SET reason='{"code":"await_ai","args":{"cause":"no_provider"}}' WHERE id=?`, await.FindingID)
	h.exec(`UPDATE findings SET attempts=2,retryable=1 WHERE id=?`, retry.FindingID)
	h.exec(`UPDATE findings SET attempts=3,retryable=1 WHERE id=?`, exhausted.FindingID)
	h.exec(`UPDATE findings SET retryable=0 WHERE id=?`, permanent.FindingID)
	tx := h.tx()
	backlog, err := loadBacklogTx(t.Context(), tx, ref, h.now)
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{}
	for _, finding := range backlog {
		ids = append(ids, finding.ID)
	}
	if want := []int64{await.FindingID, requested.FindingID, boundary.FindingID, retry.FindingID}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("Q顺序/资格错误：%v != %v", ids, want)
	}
	finding, _ := loadFindingTx(t.Context(), tx, expired.FindingID)
	if finding.Decision != DecisionSkipped || finding.Stage != StageDone || finding.Reason == nil || finding.Reason.Code != "findings.backlog_expired" {
		t.Fatalf("过期积压未清：%+v", finding)
	}
	finding, _ = loadFindingTx(t.Context(), tx, await.FindingID)
	if finding.Decision != DecisionPending || finding.Stage != StageDetail {
		t.Fatal("等待分析被过期清理")
	}
	tx.Rollback()
	if finding := h.read(expired.FindingID); finding.Decision != DecisionPending {
		t.Fatal("Q入口自行提交了调用者事务")
	}
}

func TestProcessingSetPriorityDedupAndLimit(t *testing.T) {
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeAI, false)
	r1 := h.finding(ref, "r1", StageDetail, DecisionPending, h.now)
	r2 := h.finding(ref, "r2", StageDetail, DecisionPending, h.now)
	n1 := h.finding(ref, "n1", StageDetail, DecisionPending, h.now)
	n2 := h.finding(ref, "n2", StageDetail, DecisionPending, h.now)
	q1 := h.finding(ref, "q1", StageDetail, DecisionPending, h.now)
	q2 := h.finding(ref, "q2", StageDetail, DecisionPending, h.now)
	b1 := h.finding(ref, "b1", StageDetail, DecisionPending, h.now)
	b2 := h.finding(ref, "b2", StageDetail, DecisionPending, h.now)
	title := h.finding(ref, "title", StageTitle, DecisionPending, h.now)
	h.exec(`UPDATE findings SET reanalyze_requested_at=10 WHERE id=?`, r1.FindingID)
	h.exec(`UPDATE findings SET reanalyze_requested_at=20 WHERE id=?`, r2.FindingID)
	h.exec(`UPDATE listings SET published_at=10 WHERE item_id IN ('n1','b1')`)
	h.exec(`UPDATE listings SET published_at=20 WHERE item_id='n2'`)
	h.exec(`UPDATE findings SET is_baseline=1 WHERE id IN (?,?)`, b1.FindingID, b2.FindingID)
	newIDs := []int64{n1.FindingID, r2.FindingID, n2.FindingID, n1.FindingID, title.FindingID}
	qIDs := []int64{b2.FindingID, q2.FindingID, r1.FindingID, b1.FindingID, q1.FindingID, n2.FindingID}
	tx := h.tx()
	work, err := loadProcessingTx(t.Context(), tx, ref, newIDs, qIDs, 100)
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{}
	for _, item := range work {
		ids = append(ids, item.FindingID)
		if item.ProcessingRevision != 1 || item.RunRef != ref {
			t.Fatalf("P没有捕获准确工作版本：%+v", item)
		}
	}
	want := []int64{r1.FindingID, r2.FindingID, n2.FindingID, n1.FindingID, q2.FindingID, q1.FindingID, b1.FindingID, b2.FindingID}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("P顺序错误：%v != %v", ids, want)
	}
	limited, err := loadProcessingTx(t.Context(), tx, ref, newIDs, qIDs, 5)
	if err != nil || len(limited) != 5 || limited[4].FindingID != q2.FindingID {
		t.Fatalf("P上限错误：%+v %v", limited, err)
	}
	left, _ := loadFindingTx(t.Context(), tx, b2.FindingID)
	if left.Decision != DecisionPending || left.Attempts != 0 {
		t.Fatal("截断集合删除或消耗了余下队列")
	}
	tx.Rollback()
}

func TestTitleOnlyDoesNotChangeWorkflowAndRejectsHiddenItems(t *testing.T) {
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeAI, false)
	filtered := h.finding(ref, "filtered", StageDone, analysis.DecisionRejected, h.now)
	h.exec(`UPDATE findings SET reject_kind='filter',decided_layer=1,attempts=2 WHERE id=?`, filtered.FindingID)
	baseline := h.finding(ref, "baseline", StageDone, DecisionSkipped, h.now)
	h.exec(`UPDATE findings SET reason='{"code":"findings.baseline","args":{}}' WHERE id=?`, baseline.FindingID)
	hidden := h.finding(ref, "hidden", StageDone, DecisionSkipped, h.now)
	h.exec(`UPDATE findings SET reason='{"code":"findings.dismissed","args":{}}' WHERE id=?`, hidden.FindingID)
	tx := h.tx()
	for _, work := range []WorkRef{filtered, baseline} {
		before, _ := loadFindingTx(t.Context(), tx, work.FindingID)
		if err := saveTitleOnlyTx(t.Context(), tx, work, analysis.Profile{}, analysis.TitleResult{Item: analysis.TitleItem{Kind: analysis.KindSale, Verdict: "pass"}}, h.now); err != nil {
			t.Fatal(err)
		}
		after, _ := loadFindingTx(t.Context(), tx, work.FindingID)
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("只抽行情改变发现：%+v => %+v", before, after)
		}
		spec, err := loadItemSpecsTx(t.Context(), tx, ref.TaskID, before.ItemID)
		if err != nil || spec == nil || spec.Kind != analysis.KindSale {
			t.Fatalf("行情规格未保存：%+v %v", spec, err)
		}
	}
	if err := saveTitleOnlyTx(t.Context(), tx, hidden, analysis.Profile{}, analysis.TitleResult{Item: analysis.TitleItem{Kind: analysis.KindSale}}, h.now); err == nil {
		t.Fatal("隐藏商品仍然标题分析")
	}
	tx.Rollback()
}

func TestKeywordConclusionUsesCurrentRulesAndKeepsNotificationHistory(t *testing.T) {
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeKeyword, false)
	work := h.finding(ref, "x", StageDetail, DecisionPending, h.now)
	h.exec(`UPDATE listings SET description='has broken sensor' WHERE item_id='x'`)
	h.exec(`UPDATE tasks SET exclude_keywords_json='["broken"]' WHERE id=?`, ref.TaskID)
	h.exec(`UPDATE findings SET recommended_price=1000,notified_at=1,notify_kind='recommended',score=80,grade='good',score_breakdown_json='{}',reanalyze_requested_at=1,reanalysis_origin=1 WHERE id=?`, work.FindingID)
	tx := h.tx()
	first, err := h.s.saveConclusionTx(t.Context(), tx, work, Conclusion{Layer: &analysis.LayerResult{Reason: nil}}, h.now)
	if err != nil || !first {
		t.Fatalf("当前关键词结论未保存：%v %v", first, err)
	}
	finding, _ := loadFindingTx(t.Context(), tx, work.FindingID)
	if finding.Decision != analysis.DecisionRejected || finding.RejectKind == nil || *finding.RejectKind != analysis.RejectExcluded || finding.Score != nil || finding.Grade != nil || finding.ScoreBreakdown != nil || finding.Attempts != 1 {
		t.Fatalf("当前排除词未生效：%+v", finding)
	}
	if finding.RecommendedPrice == nil || *finding.RecommendedPrice != 1000 || finding.NotifiedAt == nil || *finding.NotifiedAt != 1 || finding.NotifyKind == nil || *finding.NotifyKind != "recommended" {
		t.Fatal("重新分析清掉通知历史")
	}
	var origin bool
	if err := tx.QueryRowContext(t.Context(), `SELECT reanalysis_origin FROM findings WHERE id=?`, finding.ID).Scan(&origin); err != nil || !origin {
		t.Fatal("清排队时间丢掉重分析来源", err)
	}
	if finding.ReanalyzeRequestedAt != nil {
		t.Fatal("得出结论未清排队时间")
	}
	tx.Rollback()
}

func TestAIConclusionRechecksCurrentExclusionsAndGlobalHiding(t *testing.T) {
	for _, test := range []struct {
		name, update string
		want         analysis.Decision
		reason       string
		first        bool
	}{
		{name: "new-exclude", update: `UPDATE tasks SET exclude_keywords_json='["broken"]'`, want: analysis.DecisionRejected, reason: "analysis.keyword_excluded", first: true},
		{name: "dismissed", update: `UPDATE listings SET is_dismissed=1`, want: DecisionSkipped, reason: "findings.dismissed"},
		{name: "seller-blocked", update: `INSERT INTO blocked_sellers(seller_id) VALUES('seller')`, want: DecisionSkipped, reason: "findings.blocked_seller"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newFindingsHarness(t)
			ref := h.task(analysis.ModeAI, false)
			work := h.finding(ref, "x", StageDetail, DecisionPending, h.now)
			h.exec(`UPDATE listings SET description='broken sensor',seller_id='seller' WHERE item_id='x'`)
			h.exec(test.update)
			tx := h.tx()
			first, err := h.s.saveConclusionTx(t.Context(), tx, work, Conclusion{Facts: &analysis.Facts{Summary: "旧请求说推荐"}}, h.now)
			if err != nil || first != test.first {
				t.Fatalf("当前规则结算错误：%v %v", first, err)
			}
			finding, err := loadFindingTx(t.Context(), tx, work.FindingID)
			if err != nil || finding.Decision != test.want || finding.Reason == nil || finding.Reason.Code != test.reason || finding.Analysis != nil {
				t.Fatalf("晚到事实越过当前排除/隐藏：%+v %v", finding, err)
			}
			tx.Rollback()
		})
	}
}
