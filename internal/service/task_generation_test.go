package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/locales"
)

func prepareGenerationTest(t *testing.T, h taskHarness, taskID *int64, proposed analysis.Profile) (analysis.Profile, analysis.Profile, []locales.Message, error) {
	t.Helper()
	tx, err := h.s.db.BeginTx(h.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var before, after int64
	if err := tx.QueryRowContext(h.ctx, `SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	old, prepared, warnings, err := PrepareProfileGenerationTx(h.ctx, tx, taskID, proposed)
	if readErr := tx.QueryRowContext(h.ctx, `SELECT total_changes()`).Scan(&after); readErr != nil || after != before {
		t.Fatalf("生成准备写入了数据库：%d → %d %v", before, after, readErr)
	}
	return old, prepared, warnings, err
}

func TestPrepareProfileGenerationKeepsDraftIDsThroughDeletionAndSave(t *testing.T) {
	h := newTaskHarness(t)
	task := h.create(map[string]any{"judge_mode": "ai", "profile": analysis.Profile{
		Specs: []analysis.ProfileSpec{{Name: "型号", Options: []string{"A"}, Values: []string{"A"}, Source: analysis.SourceUser}},
	}})
	key := taskProfileWatermarkPrefix + strconv.FormatInt(task.ID, 10)
	h.exec(`UPDATE meta SET value='10' WHERE key=?`, key)
	draft := task.Profile
	draft.Bonuses = []analysis.Requirement{{ID: "b10", Text: "原盒", Source: analysis.SourceUser, Rev: 999}}
	draft.Risks = []analysis.ProfileRisk{{ID: "c11", Name: "亮斑", Attitude: analysis.Mind, Source: analysis.SourceUser, Edited: true, Rev: 999}}
	draft.NextNo = 12
	before, _ := json.Marshal(draft)
	old, first, warnings, err := prepareGenerationTest(t, h, &task.ID, draft)
	if err != nil || len(warnings) != 0 || old.NextNo != 10 || first.NextNo != 12 || first.Bonuses[0].ID != "b10" || first.Risks[0].ID != "c11" || first.Bonuses[0].Rev != 1 || first.Risks[0].Rev != 1 {
		t.Fatalf("初次准备改了草稿编号、真实水位或版本：%+v %+v %v %v", old, first, warnings, err)
	}
	after, _ := json.Marshal(draft)
	if string(before) != string(after) {
		t.Fatal("生成准备改写了输入")
	}
	first.Bonuses = nil
	first.Risks[0].Rev = 999
	old, prepared, _, err := prepareGenerationTest(t, h, &task.ID, first)
	if err != nil || old.NextNo != 10 || prepared.NextNo != 12 || len(prepared.Bonuses) != 0 || prepared.Risks[0].ID != "c11" || prepared.Risks[0].Rev != 1 ||
		first.Risks[0].Rev != 999 || h.count(`SELECT CAST(value AS INTEGER) FROM meta WHERE key=?`, key) != 10 {
		t.Fatalf("删掉b10后改了c11或提前推进持久水位：%+v %v", prepared, err)
	}
	view, err := h.s.GetTask(h.ctx, task.ID)
	if err != nil || !reflect.DeepEqual(view.Profile, task.Profile) {
		t.Fatalf("生成准备写了任务草稿：%+v %v", view, err)
	}
	for range 2 {
		saved, err := h.s.UpdateTask(h.ctx, task.ID, taskPatch(map[string]any{"profile": prepared}))
		if err != nil || saved.Task.Profile.NextNo != 12 || saved.Task.Profile.Risks[0].ID != "c11" || saved.Task.Profile.Risks[0].Rev != 1 ||
			h.count(`SELECT CAST(value AS INTEGER) FROM meta WHERE key=?`, key) != 12 {
			t.Fatalf("最终保存或再次保存改了编号：%+v %v", saved, err)
		}
	}
	prepared.Bonuses = []analysis.Requirement{{ID: "b10", Text: "被删的原盒", Source: analysis.SourceUser}}
	_, err = h.s.UpdateTask(h.ctx, task.ID, taskPatch(map[string]any{"profile": prepared}))
	taskProfileTestError(t, err, "profile", "analysis.profile_invalid")
}

func TestPrepareProfileGenerationNewDraftNeedsNoTaskMinimum(t *testing.T) {
	h := newTaskHarness(t)
	draft := analysis.Profile{Risks: []analysis.ProfileRisk{{ID: "c11", Name: "亮斑", Source: analysis.SourceAI, Attitude: analysis.Reject, Rev: 999}}, NextNo: 13}
	before, _ := json.Marshal(draft)
	old, prepared, warnings, err := prepareGenerationTest(t, h, nil, draft)
	if err != nil || !reflect.DeepEqual(old, analysis.Profile{}) || prepared.Risks[0].ID != "c11" || prepared.Risks[0].Rev != 1 || prepared.Risks[0].Attitude != analysis.Mind || prepared.NextNo != 13 ||
		len(warnings) != 1 || warnings[0].Code != "analysis.risk_softened" || h.count(`SELECT COUNT(*) FROM tasks`) != 0 || h.count(`SELECT COUNT(*) FROM meta WHERE key LIKE 'task.profile_next_no.%'`) != 0 {
		t.Fatalf("新草稿被整任务最低要求拦截、改编号或写库：%+v %+v %v %v", old, prepared, warnings, err)
	}
	after, _ := json.Marshal(draft)
	if string(before) != string(after) {
		t.Fatal("新草稿准备改写输入")
	}
	_, empty, _, err := prepareGenerationTest(t, h, nil, analysis.Profile{})
	if err != nil || empty.NextNo != 1 {
		t.Fatalf("空草稿不能进入生成准备：%+v %v", empty, err)
	}
}

func TestPrepareProfileGenerationUsesPersistedVersionsAndRejectsRetiredIDs(t *testing.T) {
	h := newTaskHarness(t)
	task := h.create(map[string]any{"judge_mode": "ai", "profile": analysis.Profile{Musts: []analysis.Requirement{{Text: "国行", Source: analysis.SourceUser}}}})
	key := taskProfileWatermarkPrefix + strconv.FormatInt(task.ID, 10)
	task.Profile.Musts[0].Rev = 3
	raw, _ := json.Marshal(task.Profile)
	h.exec(`UPDATE tasks SET profile_json=? WHERE id=?`, string(raw), task.ID)
	h.exec(`UPDATE meta SET value='10' WHERE key=?`, key)
	draft := task.Profile
	draft.Musts = append([]analysis.Requirement{}, draft.Musts...)
	draft.Musts[0].Text, draft.Musts[0].Rev = "日版", 999
	draft.Bonuses = []analysis.Requirement{{ID: "b10", Text: "原盒", Source: analysis.SourceUser, Rev: -8}}
	draft.NextNo = 1
	old, prepared, _, err := prepareGenerationTest(t, h, &task.ID, draft)
	if err != nil || old.Musts[0].Rev != 3 || old.NextNo != 10 || prepared.Musts[0].Rev != 4 || prepared.Bonuses[0].ID != "b10" || prepared.Bonuses[0].Rev != 1 || prepared.NextNo != 11 {
		t.Fatalf("准备使用了草稿版本或漏了真实水位：%+v %+v %v", old, prepared, err)
	}
	draft.Bonuses[0].ID = "b3"
	_, _, _, err = prepareGenerationTest(t, h, &task.ID, draft)
	taskProfileTestError(t, err, "profile", "analysis.profile_invalid")
	for _, value := range []int{int(limits.MaxSafeInteger + 1), int(limits.MinSafeInteger - 1)} {
		draft.Bonuses[0].ID, draft.NextNo = "b10", value
		_, _, _, err = prepareGenerationTest(t, h, &task.ID, draft)
		taskProfileTestError(t, err, "profile", "analysis.profile_invalid")
	}
	draft.Bonuses[0].ID, draft.NextNo = "", int(limits.MaxSafeInteger)
	_, _, _, err = prepareGenerationTest(t, h, &task.ID, draft)
	taskProfileTestError(t, err, "profile", "analysis.profile_id_exhausted")
}

func TestPrepareProfileGenerationKeepsCoreMissingAndStorageErrors(t *testing.T) {
	h := newTaskHarness(t)
	missing := int64(99)
	_, _, _, err := prepareGenerationTest(t, h, &missing, analysis.Profile{})
	var failure *ServiceError
	if !errors.As(err, &failure) || failure.Kind != ErrorNotFound || failure.Reason.Code != "tasks.not_found" {
		t.Fatalf("缺任务被隐藏或换成其他错误：%v", err)
	}
	task := h.create(map[string]any{"judge_mode": "keyword"})
	key := taskProfileWatermarkPrefix + strconv.FormatInt(task.ID, 10)
	h.exec(`UPDATE meta SET value='broken' WHERE key=?`, key)
	_, _, _, err = prepareGenerationTest(t, h, &task.ID, analysis.Profile{})
	if !errors.As(err, &failure) || failure.Kind != ErrorUnavailable || failure.Reason.Code != "service.storage_failure" {
		t.Fatalf("损坏的持久水位被当成新任务：%v", err)
	}
	tx, err := h.s.db.BeginTx(h.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithCancel(h.ctx)
	cancel()
	_, _, _, err = PrepareProfileGenerationTx(ctx, tx, &task.ID, analysis.Profile{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消被吞掉：%v", err)
	}
}
