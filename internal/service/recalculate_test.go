package service

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
)

func marketTestFinding(t *testing.T, db *sql.DB, task Task, itemID string, facts analysis.Facts, layer int64, decision analysis.Decision, analyzedAt int64) int64 {
	t.Helper()
	result, err := db.ExecContext(t.Context(), `INSERT INTO findings(task_id,item_id,search_generation,found_at,is_baseline,decision,stage,decided_layer,
		analysis_json,user_overrides_json,score,grade,score_breakdown_json,scored_price,scored_at,analyzed_at,is_read,
		notify_kind,notified_at,recommended_price,last_drop_notified_price,drop_notified_at,analysis_model,analysis_provider_id,summary)
		VALUES(?,?,1,?,0,?,'done',?,?,?,60,'pricey',?,NULL,?, ?,0,'recommended',111,1111,900,222,'原模型',42,'原结论')`,
		task.ID, itemID, analyzedAt, decision, layer, marketTestJSON(t, facts), `{"risks":{}}`,
		marketTestJSON(t, analysis.ScoreBreakdown{Items: []analysis.ScoreItem{}, Caps: []analysis.ScoreCap{}, Gates: nil}), analyzedAt-1, analyzedAt)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func marketTestFacts(revision int64) analysis.Facts {
	return analysis.Facts{
		Specs:   []analysis.SpecValue{{Name: "存储", Value: marketTestPtr("128G")}},
		Musts:   []analysis.RequirementFact{{ID: "m1", Rev: revision, Status: analysis.ReqMet, Source: marketTestPtr("text"), Evidence: marketTestPtr("国行")}},
		Bonuses: []analysis.RequirementFact{}, Checks: []analysis.CheckFact{}, SellerSignals: []analysis.SellerSignal{}, ExtraFindings: []analysis.ExtraFinding{},
	}
}

func TestRecalculationUpdatesEligibilityBeforeMarketAndProtectsUserSource(t *testing.T) {
	s, db, task, now := marketTestService(t)
	task.Profile.Musts[0].Text, task.Profile.Musts[0].Rev = "日版", 2
	var targetID int64
	for i := 0; i < 5; i++ {
		id := strconv.Itoa(i)
		marketTestSeed(t, db, task, id, "平板", marketTestPtr(int64(1000+i*10)), "2026-11-11", now)
		facts := marketTestFacts(2)
		if i == 0 {
			facts.Musts[0].Status, facts.Musts[0].Rev = analysis.ReqUnmet, 1
			facts.Musts[0].Evidence = marketTestPtr("美版")
		}
		findingID := marketTestFinding(t, db, task, id, facts, 3, analysis.DecisionScored, now-100)
		if i == 0 {
			targetID = findingID
		}
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE listings SET price=(SELECT price FROM task_sightings s WHERE s.item_id=listings.item_id)`); err != nil {
		t.Fatal(err)
	}
	userRaw := marketTestJSON(t, []analysis.SpecValue{{Name: "存储", Value: marketTestPtr("１２８Ｇ")}})
	if _, err := db.ExecContext(t.Context(), `UPDATE task_item_specs SET source='user',specs_json=?,excluded=1,config_key='旧键',updated_at=123 WHERE item_id='0'`, userRaw); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE findings SET decision='rejected',reject_kind='unmatched' WHERE id=?`, targetID); err != nil {
		t.Fatal(err)
	}
	marketTestSeed(t, db, task, "title-rejected", "平板", marketTestPtr(int64(1020)), "2026-11-11", now)
	titleID := marketTestFinding(t, db, task, "title-rejected", marketTestFacts(2), 2, analysis.DecisionRejected, now-100)
	if _, err := db.ExecContext(t.Context(), `UPDATE findings SET reject_kind='unmatched' WHERE id=?`, titleID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE task_item_specs SET excluded=1 WHERE item_id='title-rejected'`); err != nil {
		t.Fatal(err)
	}
	marketTestLatest(t, db, task, []string{"0", "1", "2", "3", "4"})
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	count, err := s.recalculateTaskTx(t.Context(), tx, task, now)
	if err != nil || count != 5 {
		t.Fatalf("第三层重算数量错误：%d %v", count, err)
	}
	result, err := loadFindingTx(t.Context(), tx, targetID)
	if err != nil || result.Decision != analysis.DecisionScored || result.FairPrice == nil || *result.FairPrice != 1020 || result.FairSample == nil || *result.FairSample != 5 {
		t.Fatalf("旧版本排除未在算行情前清理：%+v %v", result, err)
	}
	if result.ScoredPrice != nil || result.ScoredAt == nil || *result.ScoredAt != now || result.NotifiedAt == nil || *result.NotifiedAt != 111 || result.RecommendedPrice == nil || *result.RecommendedPrice != 1111 || result.LastDropNotifiedPrice == nil || *result.LastDropNotifiedPrice != 900 || result.DropNotifiedAt == nil || *result.DropNotifiedAt != 222 || result.AnalysisModel == nil || *result.AnalysisModel != "原模型" || result.Summary == nil || *result.Summary != "原结论" {
		t.Fatal("重算改变了首次未知价格、通知基准或模型原话")
	}
	var raw, kind, source, key string
	var excluded bool
	var updated int64
	var brandNew bool
	if err := tx.QueryRowContext(t.Context(), `SELECT specs_json,kind,source,config_key,excluded,brand_new,updated_at FROM task_item_specs WHERE task_id=? AND item_id='0'`, task.ID).
		Scan(&raw, &kind, &source, &key, &excluded, &brandNew, &updated); err != nil {
		t.Fatal(err)
	}
	if raw != userRaw || kind != "sale" || source != "user" || brandNew || updated != 123 || key != "128G" || excluded {
		t.Fatalf("派生更新越过用户原始字段保护：%q %s %s %s %t %t %d", raw, kind, source, key, excluded, brandNew, updated)
	}
	title, err := loadFindingTx(t.Context(), tx, titleID)
	if err != nil || title.Decision != analysis.DecisionRejected || title.DecidedLayer == nil || *title.DecidedLayer != 2 || *title.ScoredAt != now-101 {
		t.Fatal("第二层不符合被免费重算复活", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal("重算擅自结束调用方事务", err)
	}
}

func TestSingleRecalculationKeepsFirstUnknownPriceAndOtherFindings(t *testing.T) {
	s, db, task, now := marketTestService(t)
	ids := []int64{}
	for i := 0; i < 5; i++ {
		id := strconv.Itoa(i)
		marketTestSeed(t, db, task, id, "平板", marketTestPtr(int64(1000+i*10)), "2026-11-11", now)
		ids = append(ids, marketTestFinding(t, db, task, id, marketTestFacts(1), 3, analysis.DecisionScored, now-100))
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE findings SET scored_at=NULL WHERE id=?`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE listings SET price=NULL WHERE item_id='0'`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	first, err := s.recalculateFindingTx(t.Context(), tx, task, ids[0], now)
	if err != nil || first.ScoredAt == nil || first.ScoredPrice != nil || first.Score != nil {
		t.Fatalf("首次未知价格不正确：%+v %v", first, err)
	}
	if _, err := tx.ExecContext(t.Context(), `UPDATE listings SET price=1000 WHERE item_id='0'`); err != nil {
		t.Fatal(err)
	}
	second, err := s.recalculateFindingTx(t.Context(), tx, task, ids[0], now+100)
	if err != nil || second.Score == nil || second.ScoredPrice != nil || *second.ScoredAt != now+100 {
		t.Fatalf("之后的已知价格被倒填首次基准：%+v %v", second, err)
	}
	other, err := loadFindingTx(t.Context(), tx, ids[1])
	if err != nil || other.ScoredAt == nil || *other.ScoredAt != now-101 {
		t.Fatal("单件覆盖重算了无关发现", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var stored sql.NullInt64
	if err := db.QueryRowContext(t.Context(), `SELECT scored_at FROM findings WHERE id=?`, ids[0]).Scan(&stored); err != nil || stored.Valid {
		t.Fatal("调用方回滚后仍留下重算", err)
	}
}

func TestRecalculationFailureRollsBackDerivedEligibility(t *testing.T) {
	s, db, task, now := marketTestService(t)
	marketTestSeed(t, db, task, "one", "平板", marketTestPtr(int64(1000)), "2026-11-11", now)
	marketTestFinding(t, db, task, "one", marketTestFacts(1), 3, analysis.DecisionScored, now-100)
	if _, err := db.ExecContext(t.Context(), `UPDATE task_item_specs SET excluded=1,config_key='原键' WHERE item_id='one'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER fail_market_score BEFORE UPDATE ON findings BEGIN SELECT RAISE(ABORT,'本地故障'); END`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.recalculateTaskTx(t.Context(), tx, task, now); err == nil {
		t.Fatal("保存评分失败却返回成功")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var excluded bool
	var key string
	if err := db.QueryRowContext(t.Context(), `SELECT excluded,config_key FROM task_item_specs WHERE item_id='one'`).Scan(&excluded, &key); err != nil || !excluded || key != "原键" {
		t.Fatal("评分失败后留下了部分派生资格", err)
	}
}

func TestSingleRecalculationDoesNotRequireFactsForPendingOrTitleResults(t *testing.T) {
	s, db, task, now := marketTestService(t)
	for index, decision := range []analysis.Decision{DecisionPending, DecisionFailed, analysis.DecisionRejected} {
		itemID := "unfinished-" + strconv.Itoa(index)
		marketTestSeed(t, db, task, itemID, "平板", marketTestPtr(int64(1000)), "2026-11-11", now)
		id := marketTestFinding(t, db, task, itemID, marketTestFacts(1), 2, decision, now-100)
		if _, err := db.ExecContext(t.Context(), `UPDATE findings SET analysis_json=NULL,reject_kind='unmatched',stage='title' WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := s.recalculateFindingTx(t.Context(), tx, task, id, now)
		if err != nil || actual.ID != id || actual.Decision != decision || actual.Analysis != nil || *actual.ScoredAt != now-101 {
			t.Fatalf("没有第三层结论的对象被强求事实或重算：%+v %v", actual, err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNoGoodHintHasIndependentEligibilityAndDismissal(t *testing.T) {
	_, db, task, now := marketTestService(t)
	seed := func(index int, baseline bool) int64 {
		id := "hint-" + strconv.Itoa(index)
		marketTestSeed(t, db, task, id, "平板", marketTestPtr(int64(1000)), "2026-11-11", now)
		findingID := marketTestFinding(t, db, task, id, marketTestFacts(1), 3, analysis.DecisionScored, now+int64(index))
		breakdown := analysis.ScoreBreakdown{Base: &analysis.ScoreBase{Score: 75}, AI: analysis.Adjustment{Value: -15, Reason: "整体不合适"}, Items: []analysis.ScoreItem{}, Caps: []analysis.ScoreCap{}}
		if _, err := db.ExecContext(t.Context(), `UPDATE findings SET is_baseline=?,score_breakdown_json=? WHERE id=?`, baseline, marketTestJSON(t, breakdown), findingID); err != nil {
			t.Fatal(err)
		}
		return findingID
	}
	read := func() *NoGoodHint {
		t.Helper()
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		hint, err := loadNoGoodHintTx(t.Context(), tx, task)
		if err != nil {
			t.Fatal(err)
		}
		return hint
	}
	for i := 0; i < 29; i++ {
		seed(i, false)
	}
	if read() != nil {
		t.Fatal("不足30个仍显示提示")
	}
	seed(29, false)
	hint := read()
	if hint == nil || hint.SampleCount != 30 || hint.LatestAnalyzedAt != now+29 || hint.Reasons == nil || len(hint.Reasons) != 0 {
		t.Fatalf("没有规定计数原因被误当作无提示：%+v", hint)
	}
	task.NoGoodHintDismissedAt = marketTestPtr(now + 29)
	if read() != nil {
		t.Fatal("知道了没有隐藏到下一次第三层结论")
	}
	latestID := seed(30, false)
	if read() == nil {
		t.Fatal("新第三层结论后没有重新显示提示")
	}
	baselineID := seed(31, true)
	if _, err := db.ExecContext(t.Context(), `UPDATE findings SET score=95,grade='steal' WHERE id=?`, baselineID); err != nil {
		t.Fatal(err)
	}
	if read() == nil {
		t.Fatal("基线高分改变了非基线提示资格")
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE findings SET score=85,grade='good' WHERE id=?`, latestID); err != nil {
		t.Fatal(err)
	}
	if read() != nil {
		t.Fatal("已有划算结果仍显示提示")
	}
}

func TestSellerProjectionUsesKnownRatesAndNeverGuessesCredit(t *testing.T) {
	listing := Listing{SellerRegistrationDays: marketTestPtr(int64(10))}
	profile := &SellerProfile{SellerCredit: marketTestPtr("信用极好"), SellerCreditLevel: marketTestPtr("999")}
	result := sellerResultForFinding(listing, profile, nil, "平板")
	if result.BonusEligible || result.Type != analysis.SellerPersonal {
		t.Fatal("把展示文字或未知等级猜成信用奖励")
	}
	unknown := []analysis.EffectiveCheck{}
	for _, check := range result.Checks {
		if check.Key == "seller_bad_reviews" || check.Key == "seller_partial_refund" {
			unknown = append(unknown, check)
		}
	}
	if len(unknown) != 2 || unknown[0].Status != analysis.CheckUnknown || unknown[1].Status != analysis.CheckUnknown {
		t.Fatal("未取得评价被伪装成没有差评")
	}
	profile.RecentRates = []SellerRecentRate{}
	result = sellerResultForFinding(listing, profile, nil, "平板")
	for _, check := range result.Checks {
		if (check.Key == "seller_bad_reviews" || check.Key == "seller_partial_refund") && check.Status != analysis.CheckOK {
			t.Fatal("明确空评价没有与未取得区分")
		}
	}
	facts := &analysis.Facts{SellerSignals: []analysis.SellerSignal{{Key: "desc_mismatch", Evidence: "到货与描述不符"}}}
	before := *facts
	profile.RecentRates = nil
	result = sellerResultForFinding(listing, profile, facts, "平板")
	found := false
	for _, check := range result.Checks {
		if check.Key == "seller_bad_reviews" && check.Status == analysis.CheckRisk && check.Source == analysis.EvidenceProgram {
			found = true
		}
	}
	if !found || !reflect.DeepEqual(before, *facts) {
		t.Fatal("已核对风险因评价不可用被丢弃，或污染原事实")
	}
}

func sellerAssessmentCheck(t *testing.T, value analysis.SellerResult, key string) analysis.EffectiveCheck {
	t.Helper()
	for _, check := range value.Checks {
		if check.Key == key {
			return check
		}
	}
	t.Fatal("缺少卖家检查项", key)
	return analysis.EffectiveCheck{}
}

func TestSellerAssessmentCodecKeepsShortEvidenceAndRejectsCorruption(t *testing.T) {
	evidence := strings.Repeat("卖家确认描述不符", 30)
	assessment := sellerResultForFinding(Listing{SellerRegistrationDays: marketTestPtr(int64(365))},
		&SellerProfile{RecentRates: []SellerRecentRate{}},
		&analysis.Facts{SellerSignals: []analysis.SellerSignal{{Key: "desc_mismatch", Evidence: evidence}}}, "平板")
	raw, err := sellerAssessmentJSON(assessment)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeSellerAssessment(raw)
	if err != nil || decoded == nil || len([]rune(*sellerAssessmentCheck(t, *decoded, "seller_bad_reviews").Evidence)) != 120 {
		t.Fatalf("短证据没有按字符保存：%+v %v", decoded, err)
	}
	if *sellerAssessmentCheck(t, assessment, "seller_bad_reviews").Evidence != evidence {
		t.Fatal("保存短证据污染了完整事实")
	}
	for _, token := range []string{"recent_rates", "seller_credit_level", "rate_count", "on_sale_count"} {
		if strings.Contains(raw, token) {
			t.Fatal("持久结论复制了原始数字缓存", token)
		}
	}
	for _, bad := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"missing bonus", func(value map[string]any) { delete(value, "bonus_eligible") }},
		{"null bonus", func(value map[string]any) { value["bonus_eligible"] = nil }},
		{"unknown top field", func(value map[string]any) { value["raw_counts"] = 1 }},
		{"unknown type", func(value map[string]any) { value["type"] = "made_up" }},
		{"null checks", func(value map[string]any) { value["checks"] = nil }},
		{"null reasons", func(value map[string]any) { value["reasons"] = nil }},
		{"unknown seller key", func(value map[string]any) { value["checks"].([]any)[0].(map[string]any)["key"] = "seller_future" }},
		{"non seller key", func(value map[string]any) { value["checks"].([]any)[0].(map[string]any)["key"] = "id_lock" }},
		{"duplicate key", func(value map[string]any) {
			value["checks"].([]any)[1].(map[string]any)["key"] = value["checks"].([]any)[0].(map[string]any)["key"]
		}},
		{"invalid status", func(value map[string]any) { value["checks"].([]any)[0].(map[string]any)["status"] = "safe" }},
		{"invalid source", func(value map[string]any) { value["checks"].([]any)[0].(map[string]any)["source"] = "user" }},
	} {
		t.Run(bad.name, func(t *testing.T) {
			var edited map[string]any
			if err := json.Unmarshal([]byte(raw), &edited); err != nil {
				t.Fatal(err)
			}
			bad.edit(edited)
			if value, err := decodeSellerAssessment(marketTestJSON(t, edited)); err == nil || value != nil {
				t.Fatalf("坏记录被当成有效或未知：%+v %v", value, err)
			}
		})
	}
	for _, bad := range []string{"null", "[]", "{", raw + " {}", string([]byte{'{', 0xff, '}'})} {
		if value, err := decodeSellerAssessment(bad); err == nil || value != nil {
			t.Fatalf("坏JSON被当成可回填NULL：%q %+v %v", bad, value, err)
		}
	}
}

func TestPersistedSellerAssessmentSurvivesCacheCleanupAndNewAttitudes(t *testing.T) {
	s, db, task, now := marketTestService(t)
	task.Profile.Risks = []analysis.ProfileRisk{{ID: "seller_bad_reviews", Attitude: analysis.Mind, Source: analysis.SourceSystem, Rev: 1}}
	var targetID int64
	for index := 0; index < 5; index++ {
		itemID := strconv.Itoa(index)
		marketTestSeed(t, db, task, itemID, "平板", marketTestPtr(int64(10000+index*100)), "2026-11-11", now)
		id := marketTestFinding(t, db, task, itemID, marketTestFacts(1), 3, analysis.DecisionScored, now-100)
		if index == 0 {
			targetID = id
		}
	}
	assessment := analysis.JudgeSeller(analysis.SellerInput{Available: true, RatesAvailable: true,
		RegistrationDays: marketTestPtr(365), CreditExcellent: marketTestPtr(true), RecentRates: []analysis.SellerRate{}},
		[]analysis.SellerSignal{{Key: "desc_mismatch", Evidence: "收到的商品与描述不符"}}, task.Keyword)
	raw, err := sellerAssessmentJSON(assessment)
	if err != nil || !assessment.BonusEligible {
		t.Fatal("测试需要已确认的个人信用奖励", err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE findings SET seller_checks_json=? WHERE id=?`, raw, targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE listings SET price=7800,seller_registration_days=365 WHERE item_id='0'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO seller_profiles(seller_id,recent_items_json,recent_rates_json,fetched_at)
		VALUES('seller-0','[]','[]',?)`, now-86400000); err != nil {
		t.Fatal(err)
	}
	recalculate := func(at int64) Finding {
		t.Helper()
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		finding, err := s.recalculateFindingTx(t.Context(), tx, task, targetID, at)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return finding
	}
	first := recalculate(now)
	if first.SellerAssessment == nil || first.Score == nil || first.SellerType == nil || *first.SellerType != analysis.SellerPersonal {
		t.Fatalf("没有读取已保存的卖家结论：%+v", first)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE seller_profiles SET recent_rates_json='broken' WHERE seller_id='seller-0'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE listings SET seller_registration_days=1 WHERE item_id='0'`); err != nil {
		t.Fatal(err)
	}
	withBadCache := recalculate(now + 1)
	if _, err := db.ExecContext(t.Context(), `DELETE FROM seller_profiles`); err != nil {
		t.Fatal(err)
	}
	withoutCache := recalculate(now + 2)
	if *first.Score != *withBadCache.Score || *first.Score != *withoutCache.Score || *withoutCache.SellerType != *first.SellerType || !withoutCache.SellerAssessment.BonusEligible || !reflect.DeepEqual(first.SellerAssessment, withoutCache.SellerAssessment) {
		t.Fatal("缓存变化改变了历史分、类型或信用奖励")
	}
	creditBonus := false
	for _, item := range withoutCache.ScoreBreakdown.Items {
		creditBonus = creditBonus || item.ID == "seller_credit" && item.Value == 2
	}
	if !creditBonus {
		t.Fatal("持久的个人信用奖励在缓存删除后丢失")
	}
	task.Profile.Risks[0].Attitude = analysis.Confirm
	strict := recalculate(now + 3)
	if strict.Grade == nil || *strict.Grade != analysis.GradeAvoid || strict.Score == nil || *strict.Score > 39 {
		t.Fatal("改态度后没有对已保存的有证据风险重新封顶")
	}
	task.Profile.Risks[0].Attitude = analysis.Ignore
	ignored := recalculate(now + 4)
	if ignored.Score == nil || *ignored.Score != *first.Score+5 || !reflect.DeepEqual(first.SellerAssessment, ignored.SellerAssessment) {
		t.Fatal("修改态度覆盖了原卖家事实或没有重新算分")
	}
	var afterRaw string
	if err := db.QueryRowContext(t.Context(), `SELECT seller_checks_json FROM findings WHERE id=?`, targetID).Scan(&afterRaw); err != nil || afterRaw != raw {
		t.Fatal("普通重算覆盖了分析当时的持久结论", err)
	}
}

func TestLegacySellerAssessmentBackfillsOnceFromExistingEvidence(t *testing.T) {
	for _, cached := range []bool{true, false} {
		t.Run(strconv.FormatBool(cached), func(t *testing.T) {
			s, db, task, now := marketTestService(t)
			marketTestSeed(t, db, task, "legacy", "平板", marketTestPtr(int64(1000)), "2026-11-11", now)
			facts := marketTestFacts(1)
			facts.SellerSignals = []analysis.SellerSignal{{Key: "partial_refund", Evidence: "被卖家要求收到后退差价"}}
			id := marketTestFinding(t, db, task, "legacy", facts, 3, analysis.DecisionScored, now-100)
			breakdown := analysis.ScoreBreakdown{Items: []analysis.ScoreItem{{ID: "seller_credit", Value: 2}}}
			if _, err := db.ExecContext(t.Context(), `UPDATE findings SET seller_type='personal',score_breakdown_json=? WHERE id=?`, marketTestJSON(t, breakdown), id); err != nil {
				t.Fatal(err)
			}
			if cached {
				items := []SellerRecentItem{{Title: "平板1", SaleStatus: marketTestPtr("on_sale")}, {Title: "平板2", SaleStatus: marketTestPtr("on_sale")}, {Title: "平板3", SaleStatus: marketTestPtr("on_sale")}}
				if _, err := db.ExecContext(t.Context(), `INSERT INTO seller_profiles(seller_id,recent_items_json,recent_rates_json,fetched_at)
					VALUES('seller-legacy',?,'[]',?)`, marketTestJSON(t, items), now-100); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			value, err := s.recalculateFindingTx(t.Context(), tx, task, id, now)
			if err != nil || value.SellerAssessment == nil {
				t.Fatalf("没有一次回填旧结论：%+v %v", value, err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			assessment := *value.SellerAssessment
			code := "analysis.seller_legacy_partial"
			if cached {
				code = "analysis.seller_legacy"
				if assessment.Type != analysis.SellerDealerSuspect {
					t.Fatal("有缓存没有利用现存资料")
				}
			} else if assessment.Type != analysis.SellerPersonal || !assessment.BonusEligible || sellerAssessmentCheck(t, assessment, "seller_new").Status != analysis.CheckUnknown || sellerAssessmentCheck(t, assessment, "seller_bad_reviews").Status != analysis.CheckUnknown {
				t.Fatal("缺缓存时编造了新号、无差评或丢掉已有信用奖励")
			}
			if sellerAssessmentCheck(t, assessment, "seller_partial_refund").Status != analysis.CheckRisk {
				t.Fatal("旧已核对的到手刀证据丢失")
			}
			compatibility := false
			for _, reason := range assessment.Reasons {
				compatibility = compatibility || reason.Code == code
				for _, language := range []locales.Language{locales.ZhCN, locales.En} {
					if _, err := locales.Format(language, reason); err != nil {
						t.Fatal("兼容或原有原因没有两语文案", err)
					}
				}
			}
			if !compatibility {
				t.Fatal("旧NULL回填没有标注兼容来源")
			}
			if _, err := db.ExecContext(t.Context(), `DELETE FROM seller_profiles`); err != nil {
				t.Fatal(err)
			}
			tx, err = db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			again, err := s.recalculateFindingTx(t.Context(), tx, task, id, now+100)
			if err != nil || !reflect.DeepEqual(value.SellerAssessment, again.SellerAssessment) || *value.SellerType != *again.SellerType {
				t.Fatalf("后来缓存变化覆盖了一次回填：%+v %v", again, err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
