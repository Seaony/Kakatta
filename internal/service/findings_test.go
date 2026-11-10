package service

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

type findingsHarness struct {
	t   *testing.T
	s   *Service
	now int64
}

func newFindingsHarness(t *testing.T) findingsHarness {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "findings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Date(2026, 11, 11, 4, 0, 0, 0, time.UTC)
	preferences, err := appsettings.New(t.Context(), db.DB(), settings.Environment{TZ: "UTC"}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return findingsHarness{t: t, s: &Service{db: db.DB(), settings: preferences, now: func() time.Time { return now }}, now: now.UnixMilli()}
}

func (h findingsHarness) tx() *sql.Tx {
	h.t.Helper()
	tx, err := h.s.db.BeginTx(h.t.Context(), nil)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { tx.Rollback() })
	return tx
}

func (h findingsHarness) exec(query string, args ...any) sql.Result {
	h.t.Helper()
	result, err := h.s.db.ExecContext(h.t.Context(), query, args...)
	if err != nil {
		h.t.Fatal(err)
	}
	return result
}

func (h findingsHarness) task(mode analysis.Mode, baseline bool) RunRef {
	h.t.Helper()
	policy := "good"
	if mode == analysis.ModeKeyword {
		policy = "recommended"
	}
	insert := h.exec(`INSERT INTO tasks(name,keyword,personal_only,max_pages,interval_minutes,
		judge_mode,notify_policy,sort_index,baseline_pending) VALUES('任务','camera',0,1,60,?,?,0,?)`, mode, policy, baseline)
	id, err := insert.LastInsertId()
	if err != nil {
		h.t.Fatal(err)
	}
	return RunRef{TaskID: id, SearchGeneration: 1}
}

func (h findingsHarness) finding(ref RunRef, itemID string, stage FindingStage, decision analysis.Decision, at int64) WorkRef {
	h.t.Helper()
	h.exec(`INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at,price)
		VALUES(?,?,?,0,0,1000) ON CONFLICT(item_id) DO NOTHING`, itemID, "camera "+itemID, "https://example.invalid/item/"+itemID)
	insert := h.exec(`INSERT INTO findings(task_id,item_id,search_generation,processing_revision,
		found_at,is_baseline,decision,stage,is_read) VALUES(?,?,?,1,?,0,?,?,0)`, ref.TaskID, itemID, ref.SearchGeneration, at, decision, stage)
	id, err := insert.LastInsertId()
	if err != nil {
		h.t.Fatal(err)
	}
	return WorkRef{RunRef: ref, FindingID: id, ProcessingRevision: 1}
}

func (h findingsHarness) read(id int64) Finding {
	h.t.Helper()
	tx := h.tx()
	finding, err := loadFindingTx(h.t.Context(), tx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		h.t.Fatal(err)
	}
	return finding
}

func TestPersistSearchBaselineKeepsSeenAndSelectsTenNewest(t *testing.T) {
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeAI, true)
	h.exec(`UPDATE tasks SET min_price=100,exclude_keywords_json='["broken"]' WHERE id=?`, ref.TaskID)
	observations := []SearchObservation{}
	for i := 0; i < 14; i++ {
		id := string(rune('a' + i))
		published := int64(i)
		observations = append(observations, SearchObservation{ItemID: id, Title: "camera " + id,
			DesktopURL: "https://example.invalid/item/" + id, Price: findingInt64(1000), PublishedAt: &published, ObservedAt: h.now})
	}
	observations[0].PublishedAt = nil
	observations = append(observations,
		SearchObservation{ItemID: "a", Title: "later duplicate", Price: findingInt64(1), ObservedAt: h.now},
		SearchObservation{ItemID: "filtered", Title: "camera", Price: findingInt64(50), ObservedAt: h.now},
		SearchObservation{ItemID: "excluded", Title: "broken camera", Price: findingInt64(1000), ObservedAt: h.now},
		SearchObservation{ItemID: "dismissed", Title: "camera", Price: findingInt64(1000), ObservedAt: h.now},
		SearchObservation{ItemID: "blocked", Title: "camera", Price: findingInt64(1000), ObservedAt: h.now})
	h.exec(`INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at,is_dismissed)
		VALUES('dismissed','camera','https://example.invalid/item/d',0,0,1)`)
	h.exec(`INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at,seller_id)
		VALUES('blocked','camera','https://example.invalid/item/b',0,0,'seller')`)
	h.exec(`INSERT INTO blocked_sellers(seller_id) VALUES('seller')`)
	tx := h.tx()
	saved, err := h.s.persistSearchTx(h.t.Context(), tx, ref, observations, h.now)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if saved.ScannedCount != 18 || saved.NewCount != 18 || saved.Layer1Rejected != 2 || len(saved.NewFindingIDs) != 18 || len(saved.TitleOnlyFindingIDs) != 5 {
		t.Fatalf("落盘计数不准确：%+v", saved)
	}
	var seen, pending, baselineUnread int
	h.s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM seen_items WHERE task_id=?`, ref.TaskID).Scan(&seen)
	h.s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM findings WHERE task_id=? AND decision='pending'`, ref.TaskID).Scan(&pending)
	h.s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM findings WHERE task_id=? AND (is_baseline<>1 OR is_read<>1)`, ref.TaskID).Scan(&baselineUnread)
	if seen != 18 || pending != 10 || baselineUnread != 0 {
		t.Fatalf("基线/见过错误：%d %d %d", seen, pending, baselineUnread)
	}
	for _, id := range saved.NewFindingIDs {
		finding := h.read(id)
		if len(finding.ItemID) == 1 && finding.ItemID >= "e" && finding.ItemID <= "n" {
			if finding.Decision != DecisionPending || finding.Stage != StageTitle {
				t.Fatalf("最新十件未分析：%+v", finding)
			}
		}
		if finding.ItemID == "a" && (finding.Decision != DecisionSkipped || finding.Reason.Code != "findings.baseline") {
			t.Fatalf("未知发布时间优先级错误：%+v", finding)
		}
	}
	tx = h.tx()
	latest, err := loadLatestSearchItemIDsTx(t.Context(), tx, ref.TaskID, 1)
	if err != nil || len(latest) != 18 || latest[0] != "a" {
		t.Fatalf("最近搜索集合错误：%v %v", latest, err)
	}
	tx.Rollback()
	var title string
	h.s.db.QueryRowContext(t.Context(), `SELECT title FROM listings WHERE item_id='a'`).Scan(&title)
	if title != "camera a" {
		t.Fatal("后一个重复编号覆盖首个", title)
	}
}

func TestPersistSearchEmptyAndDeletedFindingDoNotResetSeen(t *testing.T) {
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeKeyword, false)
	obs := []SearchObservation{{ItemID: "x", Title: "camera", Price: findingInt64(1000), ObservedAt: h.now}}
	tx := h.tx()
	saved, err := h.s.persistSearchTx(t.Context(), tx, ref, obs, h.now)
	if err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	h.exec(`DELETE FROM findings WHERE id=?`, saved.NewFindingIDs[0])
	tx = h.tx()
	saved, err = h.s.persistSearchTx(t.Context(), tx, ref, obs, h.now+1)
	if err != nil || saved.NewCount != 0 || len(saved.NewFindingIDs) != 0 {
		t.Fatalf("删除发现使商品重新变新：%+v %v", saved, err)
	}
	tx.Commit()
	tx = h.tx()
	if _, err := h.s.persistSearchTx(t.Context(), tx, ref, nil, h.now+2); err != nil {
		t.Fatal(err)
	}
	ids, err := loadLatestSearchItemIDsTx(t.Context(), tx, ref.TaskID, 1)
	if err != nil || ids == nil || len(ids) != 0 {
		t.Fatalf("空搜索仍沿旧集合：%v %v", ids, err)
	}
	tx.Commit()
	var count int
	h.s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM seen_items`).Scan(&count)
	if count != 1 {
		t.Fatalf("seen被清：%d", count)
	}
}

func TestPersistSearchRollbackIncludesObservationsSeenAndLatest(t *testing.T) {
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeKeyword, false)
	h.exec(`CREATE TRIGGER reject_finding BEFORE INSERT ON findings WHEN NEW.item_id='bad'
		BEGIN SELECT RAISE(ABORT,'injected failure'); END`)
	tx := h.tx()
	_, err := h.s.persistSearchTx(t.Context(), tx, ref, []SearchObservation{
		{ItemID: "ok", Title: "camera", Price: findingInt64(1000), ObservedAt: h.now},
		{ItemID: "bad", Title: "camera", Price: findingInt64(1000), ObservedAt: h.now},
	}, h.now)
	if err == nil {
		t.Fatal("注入失败未外传")
	}
	tx.Rollback()
	for _, table := range []string{"listings", "price_points", "task_sightings", "seen_items", "findings"} {
		var count int
		if err := h.s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s留下半次搜索：%d %v", table, count, err)
		}
	}
	tx = h.tx()
	latest, err := loadLatestSearchItemIDsTx(t.Context(), tx, ref.TaskID, 1)
	if err != nil || len(latest) != 0 {
		t.Fatal("失败搜索写入最新集合", latest, err)
	}
	tx.Rollback()
}

func TestPersistSearchKeywordJunkAndDisabledBaseline(t *testing.T) {
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeKeyword, false)
	tx := h.tx()
	saved, err := h.s.persistSearchTx(t.Context(), tx, ref, []SearchObservation{{ItemID: "wanted", Title: "求购 camera", ObservedAt: h.now}}, h.now)
	if err != nil {
		t.Fatal(err)
	}
	finding, err := loadFindingTx(t.Context(), tx, saved.NewFindingIDs[0])
	if err != nil || finding.Decision != analysis.DecisionRejected || finding.DecidedLayer == nil || *finding.DecidedLayer != 2 || *finding.RejectKind != analysis.RejectJunk {
		t.Fatalf("关键词垃圾未在详情前拦截：%+v %v", finding, err)
	}
	item, err := loadItemSpecsTx(t.Context(), tx, ref.TaskID, "wanted")
	if err != nil || item == nil || item.Kind != analysis.KindWanted {
		t.Fatalf("关键词垃圾进入行情：%+v %v", item, err)
	}
	tx.Commit()
	baseline := h.task(analysis.ModeAI, true)
	h.exec(`UPDATE tasks SET analyze_on_first_run=0 WHERE id=?`, baseline.TaskID)
	tx = h.tx()
	saved, err = h.s.persistSearchTx(t.Context(), tx, baseline, []SearchObservation{{ItemID: "regular", Title: "camera", ObservedAt: h.now}}, h.now)
	if err != nil || len(saved.TitleOnlyFindingIDs) != 1 {
		t.Fatalf("基线仍需只抽行情：%+v %v", saved, err)
	}
	finding, err = loadFindingTx(t.Context(), tx, saved.NewFindingIDs[0])
	if err != nil || finding.Decision != DecisionSkipped || finding.Stage != StageDone {
		t.Fatalf("关闭首分析仍处理详情：%+v %v", finding, err)
	}
	tx.Rollback()
}

func TestLatestSearchCodecDistinguishesEmptyOldAndCorrupt(t *testing.T) {
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeAI, false)
	key := latestSearchPrefix + "1"
	for _, test := range []struct {
		raw        string
		generation int64
		wantError  bool
		want       []string
	}{
		{`{"run_id":2,"search_generation":1,"item_ids":[]}`, 1, false, []string{}},
		{`{"run_id":2,"search_generation":1,"item_ids":["x"]}`, 2, false, []string{}},
		{`{"run_id":2,"search_generation":1,"item_ids":["x","x"]}`, 1, true, nil},
		{`{"run_id":2,"search_generation":1}`, 1, true, nil},
		{`{"run_id":2,"search_generation":1,"item_ids":[]} {}`, 1, true, nil},
	} {
		h.exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, test.raw)
		tx := h.tx()
		got, err := loadLatestSearchItemIDsTx(t.Context(), tx, ref.TaskID, test.generation)
		tx.Rollback()
		if (err != nil) != test.wantError || !reflect.DeepEqual(got, test.want) {
			t.Fatalf("%s：%v %v", test.raw, got, err)
		}
	}
}

func TestSaveTitleKeepsReanalysisPriorityAndUserSpecs(t *testing.T) {
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeAI, false)
	work := h.finding(ref, "x", StageTitle, DecisionPending, h.now)
	h.exec(`UPDATE findings SET reanalyze_requested_at=?,reanalysis_origin=1 WHERE id=?`, h.now, work.FindingID)
	tx := h.tx()
	if _, err := saveItemSpecsTx(t.Context(), tx, ref.TaskID, "x", analysis.ItemSpecs{Kind: analysis.KindSale, Source: analysis.SpecUser, Specs: []analysis.SpecValue{}, ConfigKey: stringPointerFindings("user")}, h.now); err != nil {
		t.Fatal(err)
	}
	first, err := saveTitleTx(t.Context(), tx, work, analysis.Profile{}, analysis.TitleResult{Item: analysis.TitleItem{Kind: analysis.KindSale, Verdict: "pass", Reason: "seller camera"}}, h.now)
	if err != nil || first {
		t.Fatalf("通过标题不是终态：%v %v", first, err)
	}
	finding, _ := loadFindingTx(t.Context(), tx, work.FindingID)
	if finding.Stage != StageDetail || finding.Attempts != 1 || finding.ReanalyzeRequestedAt == nil || *finding.ReanalyzeRequestedAt != h.now {
		t.Fatalf("标题处理丢了重分析优先级：%+v", finding)
	}
	spec, _ := loadItemSpecsTx(t.Context(), tx, ref.TaskID, "x")
	if spec.Source != analysis.SpecUser || *spec.ConfigKey != "user" {
		t.Fatalf("标题覆盖用户规格：%+v", spec)
	}
	if _, err := saveTitleTx(t.Context(), tx, work, analysis.Profile{}, analysis.TitleResult{Item: analysis.TitleItem{Kind: analysis.KindSale}}, h.now); err == nil {
		t.Fatal("旧title重复结果越过stage守卫")
	}
	tx.Commit()
}

func TestOldTitleUnmatchedDoesNotRejectChangedMust(t *testing.T) {
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeAI, false)
	old := analysis.Profile{Musts: []analysis.Requirement{{ID: "m1", Text: "国行", Rev: 1}}}
	current := analysis.Profile{Musts: []analysis.Requirement{{ID: "m1", Text: "日版", Rev: 2}}}
	raw, _ := json.Marshal(current)
	h.exec(`UPDATE tasks SET profile_json=? WHERE id=?`, string(raw), ref.TaskID)
	work := h.finding(ref, "x", StageTitle, DecisionPending, h.now)
	tx := h.tx()
	first, err := saveTitleTx(t.Context(), tx, work, old, analysis.TitleResult{Item: analysis.TitleItem{Kind: analysis.KindSale, Verdict: "unmatched", Reason: "不是国行"}}, h.now)
	if err != nil || first {
		t.Fatal("旧清单不符合仍被沿用", first, err)
	}
	finding, _ := loadFindingTx(t.Context(), tx, work.FindingID)
	if finding.Stage != StageDetail || finding.Decision != DecisionPending || finding.TitleScreenReason == nil || *finding.TitleScreenReason != "" {
		t.Fatalf("新清单被旧结论拒绝：%+v", finding)
	}
	tx.Rollback()
}

func TestUserSpecsControlTitleDecision(t *testing.T) {
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeAI, false)
	profile := analysis.Profile{Specs: []analysis.ProfileSpec{{Name: "存储", Options: []string{"128G", "256G"}, Values: []string{"256G"}}}}
	raw, _ := json.Marshal(profile)
	h.exec(`UPDATE tasks SET profile_json=? WHERE id=?`, string(raw), ref.TaskID)
	work := h.finding(ref, "x", StageTitle, DecisionPending, h.now)
	tx := h.tx()
	userSpecs := analysis.ResolveSpecs(profile, []analysis.SpecValue{{Name: "存储", Value: stringPointerFindings("256G")}}, nil, analysis.SpecUser)
	userSpecs.Kind = analysis.KindSale
	if _, err := saveItemSpecsTx(t.Context(), tx, ref.TaskID, "x", userSpecs, h.now); err != nil {
		t.Fatal(err)
	}
	first, err := saveTitleTx(t.Context(), tx, work, profile, analysis.TitleResult{Item: analysis.TitleItem{
		Kind: analysis.KindSale, Specs: []analysis.SpecValue{{Name: "存储", Value: stringPointerFindings("128G")}}, Verdict: "pass",
	}}, h.now)
	if err != nil || first {
		t.Fatalf("手改规格仍被晚到抽取拒绝：%v %v", first, err)
	}
	finding, err := loadFindingTx(t.Context(), tx, work.FindingID)
	if err != nil || finding.Stage != StageDetail || finding.Decision != DecisionPending {
		t.Fatalf("用户规格没有参与标题判断：%+v %v", finding, err)
	}
	item, err := loadItemSpecsTx(t.Context(), tx, ref.TaskID, "x")
	if err != nil || item.Source != analysis.SpecUser || item.Specs[0].Value == nil || *item.Specs[0].Value != "256G" {
		t.Fatal("手改规格丢失", item, err)
	}
	tx.Rollback()
}

func TestTitleRuleComparisonIgnoresPresentationAndDoesNotMutateInputs(t *testing.T) {
	old := analysis.Profile{Summary: "旧摘要", Specs: []analysis.ProfileSpec{
		{Name: "存储", Options: []string{"256G", "128G"}, Values: []string{"256G", "128G"}},
		{Name: "型号", Options: []string{"B", "A"}, Values: []string{"A"}},
	}, Musts: []analysis.Requirement{{ID: "m1", Text: "国行", Rev: 1}, {ID: "m2", Text: "无维修", Rev: 1}},
		Risks:        []analysis.ProfileRisk{{ID: "c1", Name: "故障", Hint: "不能启动", Question: "能开机吗", Attitude: analysis.Confirm, Rev: 1}},
		AllowedKinds: []analysis.Kind{analysis.KindSale, analysis.KindAccessory}}
	current := analysis.Profile{Summary: "新摘要", Specs: []analysis.ProfileSpec{
		{Name: "型号", Options: []string{"A", "B"}, Values: []string{"A"}, Edited: true},
		{Name: "存储", Options: []string{"128G", "256G"}, Values: []string{"128G", "256G"}},
	}, Musts: []analysis.Requirement{{ID: "m2", Text: "无维修", Rev: 1}, {ID: "m1", Text: "国行", Rev: 1, Edited: true}},
		Risks:        []analysis.ProfileRisk{{ID: "c1", Name: "故障", Hint: "不能启动", Question: "能拍开机图吗", Attitude: analysis.Reject, Rev: 1}},
		AllowedKinds: []analysis.Kind{analysis.KindAccessory, analysis.KindSale}}
	beforeOld, _ := json.Marshal(old)
	beforeCurrent, _ := json.Marshal(current)
	if !sameTitleRules(old, current) {
		t.Fatal("排序、来源标记和问句被当成新判断含义")
	}
	afterOld, _ := json.Marshal(old)
	afterCurrent, _ := json.Marshal(current)
	if !reflect.DeepEqual(beforeOld, afterOld) || !reflect.DeepEqual(beforeCurrent, afterCurrent) {
		t.Fatal("比较修改了调用方的清单")
	}
	current.Musts[0].Text = "有维修也可"
	if sameTitleRules(old, current) {
		t.Fatal("必须满足文字改变没被识别")
	}
}

func TestSaveFindingFailureAwaitDoesNotConsumeAttempt(t *testing.T) {
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeAI, false)
	work := h.finding(ref, "x", StageDetail, DecisionPending, h.now)
	h.exec(`UPDATE findings SET attempts=2,reanalyze_requested_at=? WHERE id=?`, h.now, work.FindingID)
	tx := h.tx()
	if err := saveFindingFailureTx(t.Context(), tx, work, StageDetail, findingMessage("await_ai", map[string]any{"cause": "no_provider"}), false, true, h.now); err != nil {
		t.Fatal(err)
	}
	finding, _ := loadFindingTx(t.Context(), tx, work.FindingID)
	if finding.Decision != DecisionPending || finding.Attempts != 2 || finding.Stage != StageDetail || finding.ReanalyzeRequestedAt != nil {
		t.Fatalf("等待分析错误计次：%+v", finding)
	}
	if err := saveFindingFailureTx(t.Context(), tx, work, StageDetail, findingMessage("analysis.client.network", nil), true, false, h.now); err != nil {
		t.Fatal(err)
	}
	finding, _ = loadFindingTx(t.Context(), tx, work.FindingID)
	if finding.Decision != DecisionFailed || finding.Attempts != 3 || finding.Retryable || finding.Stage != StageDetail {
		t.Fatalf("第三次后仍自动重试：%+v", finding)
	}
	tx.Rollback()
}

func TestFindingCASBlocksChangedRevisionDeletedObjectAndGeneration(t *testing.T) {
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeAI, false)
	work := h.finding(ref, "x", StageDetail, DecisionPending, h.now)
	h.exec(`UPDATE findings SET processing_revision=2,attempts=0,reanalysis_origin=1 WHERE id=?`, work.FindingID)
	tx := h.tx()
	err := saveFindingFailureTx(t.Context(), tx, work, StageDetail, findingMessage("analysis.client.network", nil), true, false, h.now)
	var problem *ServiceError
	if !errors.As(err, &problem) || problem.Kind != ErrorConflict {
		t.Fatalf("旧同代请求覆盖新请求：%v", err)
	}
	tx.Rollback()
	if got := h.read(work.FindingID); got.Attempts != 0 || got.Decision != DecisionPending {
		t.Fatalf("CAS失败仍修改：%+v", got)
	}
	h.exec(`DELETE FROM findings WHERE id=?`, work.FindingID)
	tx = h.tx()
	_, _, err = validateWorkTx(t.Context(), tx, work, StageDetail)
	if !errors.As(err, &problem) || problem.Kind != ErrorNotFound {
		t.Fatalf("删除对象没有拒绝晚写：%v", err)
	}
	tx.Rollback()
	h.exec(`UPDATE tasks SET search_generation=2 WHERE id=?`, ref.TaskID)
	tx = h.tx()
	if _, err := h.s.persistSearchTx(t.Context(), tx, ref, nil, h.now); !errors.As(err, &problem) || problem.Kind != ErrorConflict {
		t.Fatalf("旧搜索代次仍可落盘：%v", err)
	}
	tx.Rollback()
}

func TestFindingScannerArraysAndCorruptJSON(t *testing.T) {
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeAI, false)
	work := h.finding(ref, "x", StageDetail, DecisionPending, h.now)
	h.exec(`UPDATE findings SET analysis_json='{}',score_breakdown_json='{}',user_overrides_json='null',matched_keywords_json='null' WHERE id=?`, work.FindingID)
	finding := h.read(work.FindingID)
	if finding.UserOverrides.Risks == nil || finding.MatchedKeywords == nil || finding.Analysis.Specs == nil || finding.Analysis.Musts == nil || finding.Analysis.Bonuses == nil || finding.Analysis.Checks == nil || finding.Analysis.SellerSignals == nil || finding.Analysis.ExtraFindings == nil || finding.ScoreBreakdown.Items == nil || finding.ScoreBreakdown.Caps == nil || finding.ScoreBreakdown.Gates == nil {
		t.Fatal("对象数组输出null")
	}
	h.exec(`UPDATE findings SET analysis_json='{' WHERE id=?`, work.FindingID)
	tx := h.tx()
	if _, err := loadFindingTx(t.Context(), tx, work.FindingID); err == nil {
		t.Fatal("损坏事实被当成空数据")
	}
	tx.Rollback()
}

func TestFactConclusionKeepsRequestRevisionsAndFirstUnknownPrice(t *testing.T) {
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeAI, false)
	work := h.finding(ref, "x", StageDetail, DecisionPending, h.now)
	old := analysis.Profile{Musts: []analysis.Requirement{{ID: "m1", Text: "国行", Rev: 1}}}
	current := analysis.Profile{Musts: []analysis.Requirement{{ID: "m1", Text: "日版", Rev: 2}}}
	raw, _ := json.Marshal(current)
	h.exec(`UPDATE tasks SET profile_json=? WHERE id=?`, string(raw), ref.TaskID)
	h.exec(`UPDATE listings SET price=NULL,description='不是国行' WHERE item_id='x'`)
	source, evidence := "text", "不是国行"
	facts := analysis.Facts{Specs: []analysis.SpecValue{}, Musts: []analysis.RequirementFact{{ID: "m1", Rev: 1,
		Status: analysis.ReqUnmet, Source: &source, Evidence: &evidence}}, Bonuses: []analysis.RequirementFact{},
		Checks: []analysis.CheckFact{}, SellerSignals: []analysis.SellerSignal{}, ExtraFindings: []analysis.ExtraFinding{},
		Summary: "原请求的事实"}
	providerID, model := int64(7), "local-fake"
	tx := h.tx()
	first, err := h.s.saveConclusionTx(t.Context(), tx, work, Conclusion{RequestProfile: old, Facts: &facts, ProviderID: &providerID, Model: &model}, h.now)
	if err != nil || !first {
		t.Fatalf("事实结论未保存：%v %v", first, err)
	}
	finding, err := loadFindingTx(t.Context(), tx, work.FindingID)
	if err != nil || finding.Decision != analysis.DecisionScored || finding.Analysis == nil || finding.Analysis.Musts[0].Rev != 1 {
		t.Fatalf("旧请求事实被改成新含义：%+v %v", finding, err)
	}
	if finding.ScoredAt == nil || finding.ScoredPrice != nil || finding.AnalysisProviderID == nil || *finding.AnalysisProviderID != providerID || finding.AnalysisModel == nil || *finding.AnalysisModel != model {
		t.Fatalf("首次未知价或成功来源错误：%+v", finding)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	h.exec(`UPDATE findings SET decision='pending',stage='detail',processing_revision=2 WHERE id=?`, work.FindingID)
	h.exec(`UPDATE listings SET price=1000 WHERE item_id='x'`)
	work.ProcessingRevision++
	tx = h.tx()
	if _, err := h.s.saveConclusionTx(t.Context(), tx, work, Conclusion{RequestProfile: old, Facts: &facts}, h.now+100); err != nil {
		t.Fatal(err)
	}
	finding, err = loadFindingTx(t.Context(), tx, work.FindingID)
	if err != nil || finding.ScoredPrice != nil || finding.ScoredAt == nil || *finding.ScoredAt != h.now+100 {
		t.Fatalf("后来价格填进首次未知快照：%+v %v", finding, err)
	}
	tx.Rollback()
}

func TestConclusionRejectsAmbiguousResultAndStaleRevision(t *testing.T) {
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeKeyword, false)
	work := h.finding(ref, "x", StageDetail, DecisionPending, h.now)
	tx := h.tx()
	if _, err := h.s.saveConclusionTx(t.Context(), tx, work, Conclusion{}, h.now); err == nil {
		t.Fatal("空结果伪成功")
	}
	skip := findingMessage("findings.blocked_seller", nil)
	if _, err := h.s.saveConclusionTx(t.Context(), tx, work, Conclusion{Layer: &analysis.LayerResult{}, Skipped: &skip}, h.now); err == nil {
		t.Fatal("多种结果同时保存")
	}
	if _, err := tx.ExecContext(t.Context(), `UPDATE findings SET processing_revision=2 WHERE id=?`, work.FindingID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.saveConclusionTx(t.Context(), tx, work, Conclusion{Layer: &analysis.LayerResult{}}, h.now); err == nil {
		t.Fatal("旧请求覆盖新分析")
	}
	finding, _ := loadFindingTx(t.Context(), tx, work.FindingID)
	if finding.Decision != DecisionPending || finding.Attempts != 0 {
		t.Fatalf("失败结论仍写入：%+v", finding)
	}
	tx.Rollback()
}

func stringPointerFindings(value string) *string { return &value }
