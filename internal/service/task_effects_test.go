package service

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
)

func taskEffectsProfile() analysis.Profile {
	return analysis.Profile{
		Specs:   []analysis.ProfileSpec{{Name: "存储", Options: []string{"128G"}, Values: []string{"128G"}, Source: analysis.SourceUser}},
		Musts:   []analysis.Requirement{{Text: "国行", Source: analysis.SourceUser}},
		Bonuses: []analysis.Requirement{{Text: "原盒", Source: analysis.SourceUser}},
		Risks:   []analysis.ProfileRisk{{Name: "屏幕", Attitude: analysis.Mind, Source: analysis.SourceUser}},
	}
}

func seedTaskEffectsFacts(t *testing.T, h taskHarness, task Task, item string, facts analysis.Facts, overrides analysis.UserOverrides) int64 {
	t.Helper()
	id := h.seedFinding(task, item, analysis.DecisionScored, StageDone, taskProfileTestPtr[int64](3))
	rawFacts, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	rawOverrides, err := json.Marshal(overrides)
	if err != nil {
		t.Fatal(err)
	}
	specs, err := json.Marshal(facts.Specs)
	if err != nil {
		t.Fatal(err)
	}
	h.exec(`UPDATE findings SET analysis_json=?,user_overrides_json=?,score=88,grade='good',scored_at=1 WHERE id=?`, string(rawFacts), string(rawOverrides), id)
	h.exec(`INSERT INTO task_item_specs(task_id,item_id,kind,specs_json,excluded,source,updated_at) VALUES(?,?,'sale',?,0,'detail',1)`, task.ID, item, string(specs))
	return id
}

func assertTaskEffectIDs(t *testing.T, effects TaskSaveEffects, needs, titles []int64) {
	t.Helper()
	if !reflect.DeepEqual(effects.NeedsReanalysisIDs, needs) || !reflect.DeepEqual(effects.TitleRejectedIDs, titles) ||
		effects.NeedsReanalysisCount != int64(len(needs)) || effects.TitleRejectedCount != int64(len(titles)) {
		t.Fatalf("保存影响与精确集合不一致：%+v，期望 needs=%v title=%v", effects, needs, titles)
	}
	raw, err := json.Marshal(effects)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"needs_reanalysis_ids", "title_rejected_ids"} {
		if len(fields[field]) == 0 || fields[field][0] != '[' {
			t.Fatalf("编号列表必须是非null数组：%s", raw)
		}
	}
}

func TestTaskSaveEffectsReturnExactVersionedFindingIDs(t *testing.T) {
	h := newTaskHarness(t)
	task := h.create(map[string]any{"judge_mode": "ai", "profile": taskEffectsProfile()})
	h.exec(`UPDATE tasks SET search_generation=2 WHERE id=?`, task.ID)
	task.SearchGeneration = 2
	profile := task.Profile
	profile.Musts[0].Text = "日版"
	profile.Bonuses[0].Text = "原盒配件"
	profile.Risks[0].Hint = "无亮斑"
	profile.Specs = append(profile.Specs, analysis.ProfileSpec{Name: "颜色", Options: []string{"黑色"}, Values: []string{"黑色"}, Source: analysis.SourceUser})
	complete := analysis.Facts{
		Specs:   []analysis.SpecValue{{Name: "存储", Value: taskProfileTestPtr("128G")}, {Name: "颜色", Value: taskProfileTestPtr("黑色")}},
		Musts:   []analysis.RequirementFact{{ID: "m1", Status: analysis.ReqMet, Rev: 2}},
		Bonuses: []analysis.RequirementFact{{ID: "b2", Status: analysis.ReqMet, Rev: 2}},
		Checks:  []analysis.CheckFact{{Key: "c3", Status: analysis.CheckOK, Rev: 2}},
	}
	needs := []int64{}
	originals := map[int64][2]string{}
	for _, tc := range []struct {
		name   string
		change func(*analysis.Facts, *analysis.UserOverrides)
		needs  bool
	}{
		{"complete", func(*analysis.Facts, *analysis.UserOverrides) {}, false},
		{"missing_must", func(f *analysis.Facts, _ *analysis.UserOverrides) { f.Musts = nil }, true},
		{"old_must", func(f *analysis.Facts, _ *analysis.UserOverrides) { f.Musts[0].Rev = 1 }, true},
		{"missing_bonus", func(f *analysis.Facts, _ *analysis.UserOverrides) { f.Bonuses = nil }, true},
		{"missing_check", func(f *analysis.Facts, _ *analysis.UserOverrides) { f.Checks = nil }, true},
		{"missing_spec", func(f *analysis.Facts, _ *analysis.UserOverrides) { f.Specs = f.Specs[:1] }, true},
		{"current_override", func(f *analysis.Facts, o *analysis.UserOverrides) {
			f.Checks = nil
			o.Risks = map[string]analysis.RiskOverride{"c3": {Status: analysis.CheckOK, Rev: 2}}
		}, false},
		{"old_override", func(f *analysis.Facts, o *analysis.UserOverrides) {
			f.Checks = nil
			o.Risks = map[string]analysis.RiskOverride{"c3": {Status: analysis.CheckOK, Rev: 1}}
		}, true},
	} {
		raw, _ := json.Marshal(complete)
		var facts analysis.Facts
		if err := json.Unmarshal(raw, &facts); err != nil {
			t.Fatal(err)
		}
		overrides := analysis.UserOverrides{Risks: map[string]analysis.RiskOverride{}}
		tc.change(&facts, &overrides)
		id := seedTaskEffectsFacts(t, h, task, tc.name, facts, overrides)
		savedFacts, _ := json.Marshal(facts)
		savedOverrides, _ := json.Marshal(overrides)
		originals[id] = [2]string{string(savedFacts), string(savedOverrides)}
		if tc.needs {
			needs = append(needs, id)
		}
		if tc.name == "old_must" {
			h.exec(`UPDATE findings SET search_generation=1,decision='rejected',reject_kind='unmatched' WHERE id=?`, id)
		}
	}
	other := h.create(map[string]any{"judge_mode": "ai", "profile": taskEffectsProfile()})
	seedTaskEffectsFacts(t, h, other, "other_task", analysis.Facts{}, analysis.UserOverrides{})
	titleIDs := []int64{}
	for _, name := range []string{"title_one", "title_two"} {
		id := h.seedFinding(task, name, analysis.DecisionRejected, StageDone, taskProfileTestPtr[int64](2))
		h.exec(`UPDATE findings SET reject_kind='unmatched' WHERE id=?`, id)
		titleIDs = append(titleIDs, id)
	}
	for _, tc := range []struct {
		task     Task
		item     string
		decision analysis.Decision
		layer    int64
		reject   string
	}{
		{task, "first_layer", analysis.DecisionRejected, 1, "unmatched"},
		{task, "title_junk", analysis.DecisionRejected, 2, "junk"},
		{task, "title_pending", DecisionPending, 2, "unmatched"},
		{other, "other_title", analysis.DecisionRejected, 2, "unmatched"},
	} {
		id := h.seedFinding(tc.task, tc.item, tc.decision, StageDone, &tc.layer)
		h.exec(`UPDATE findings SET reject_kind=? WHERE id=?`, tc.reject, id)
	}
	models, browsers := &runtimeCoreModels{}, &runtimeCoreFactory{}
	h.s.models, h.s.resources = models, browsers
	snapshot := h.count(`SELECT SUM(processing_revision) FROM findings`)
	result, err := h.s.UpdateTask(h.ctx, task.ID, taskPatch(map[string]any{"profile": profile}))
	if err != nil {
		t.Fatal(err)
	}
	assertTaskEffectIDs(t, result.Effects, needs, titleIDs)
	if result.Task.Profile.Musts[0].Rev != 2 || result.Task.Profile.Bonuses[0].Rev != 2 || result.Task.Profile.Risks[0].Rev != 2 || models.calls.Load() != 0 || browsers.calls.Load() != 0 ||
		h.count(`SELECT COUNT(*) FROM findings WHERE reanalyze_requested_at IS NOT NULL`) != 0 || h.count(`SELECT SUM(processing_revision) FROM findings`) != snapshot || h.count(`SELECT COUNT(*) FROM ai_calls`) != 0 {
		t.Fatal("保存改变了版本规则、自动重分析或请求了外部资源")
	}
	for id, want := range originals {
		var facts, overrides string
		if err := h.s.db.QueryRowContext(h.ctx, `SELECT analysis_json,user_overrides_json FROM findings WHERE id=?`, id).Scan(&facts, &overrides); err != nil || facts != want[0] || overrides != want[1] {
			t.Fatalf("保存改写旧事实或手动覆盖：%d %s %s %v", id, facts, overrides, err)
		}
	}
}

func TestTaskSaveEffectsKeepEmptyListsAndUnchangedMeaning(t *testing.T) {
	h := newTaskHarness(t)
	created, err := h.s.CreateTask(h.ctx, taskPatch(map[string]any{"keyword": "camera", "judge_mode": "ai", "profile": taskEffectsProfile()}))
	if err != nil {
		t.Fatal(err)
	}
	assertTaskEffectIDs(t, created.Effects, []int64{}, []int64{})
	task := created.Task
	seedTaskEffectsFacts(t, h, task, "history", analysis.Facts{}, analysis.UserOverrides{})
	titleID := h.seedFinding(task, "title", analysis.DecisionRejected, StageDone, taskProfileTestPtr[int64](2))
	h.exec(`UPDATE findings SET reject_kind='unmatched' WHERE id=?`, titleID)
	profile := task.Profile
	profile.Risks[0].Question, profile.Risks[0].Attitude = "有亮斑吗？", analysis.Reject
	result, err := h.s.UpdateTask(h.ctx, task.ID, taskPatch(map[string]any{"profile": profile}))
	if err != nil || result.Task.Profile.Risks[0].Rev != 1 {
		t.Fatalf("不改含义的编辑改了条目版本：%+v %v", result, err)
	}
	assertTaskEffectIDs(t, result.Effects, []int64{}, []int64{titleID})
	for _, patch := range []map[string]any{
		{"name": "新名称"}, {"budget_min": 100}, {"keyword": "new camera"}, {"judge_mode": "keyword"}, {"name": "关键词任务"},
	} {
		result, err := h.s.UpdateTask(h.ctx, task.ID, taskPatch(patch))
		if err != nil {
			t.Fatal(err)
		}
		assertTaskEffectIDs(t, result.Effects, []int64{}, []int64{})
	}
}

func TestTaskSaveEffectsFailureRollsBackProfileAndRecalculation(t *testing.T) {
	h := newTaskHarness(t)
	task := h.create(map[string]any{"judge_mode": "ai", "profile": taskEffectsProfile()})
	first := seedTaskEffectsFacts(t, h, task, "first", analysis.Facts{}, analysis.UserOverrides{})
	second := seedTaskEffectsFacts(t, h, task, "second", analysis.Facts{}, analysis.UserOverrides{})
	var before string
	if err := h.s.db.QueryRowContext(h.ctx, `SELECT profile_json FROM tasks WHERE id=?`, task.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	h.exec(`CREATE TRIGGER fail_effect_recalculation BEFORE UPDATE OF score ON findings WHEN NEW.id=` + strconv.FormatInt(second, 10) + ` BEGIN SELECT RAISE(ABORT,'模拟重算失败'); END`)
	profile := task.Profile
	profile.Musts[0].Text = "日版"
	result, err := h.s.UpdateTask(h.ctx, task.ID, taskPatch(map[string]any{"profile": profile}))
	if err == nil || !reflect.DeepEqual(result, TaskSaveResult{}) {
		t.Fatalf("失败仍返回成功集合：%+v %v", result, err)
	}
	var after string
	if err := h.s.db.QueryRowContext(h.ctx, `SELECT profile_json FROM tasks WHERE id=?`, task.ID).Scan(&after); err != nil || after != before ||
		h.count(`SELECT COUNT(*) FROM findings WHERE id=? AND score=88 AND scored_at=1 AND seller_checks_json IS NULL`, first) != 1 {
		t.Fatalf("失败保存留下新清单或部分重算：%s %v", after, err)
	}
}
