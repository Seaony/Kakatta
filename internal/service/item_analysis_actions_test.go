package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

func itemActionDB(t *testing.T) (context.Context, *sql.DB, int64) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "items.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	result, err := db.DB().ExecContext(ctx, `INSERT INTO tasks(name,keyword,max_pages,interval_minutes,judge_mode,sort_index,notify_policy) VALUES('任务','测试',1,60,'ai',0,'good')`)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return ctx, db.DB(), id
}

func itemActionTx(t *testing.T, ctx context.Context, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

func itemActionPtr[T any](value T) *T { return &value }

func TestItemSpecsTxProtectsEntireUserRow(t *testing.T) {
	ctx, db, taskID := itemActionDB(t)
	tx := itemActionTx(t, ctx, db)
	if found, err := loadItemSpecsTx(ctx, tx, taskID, "item"); err != nil || found != nil {
		t.Fatalf("未保存的规格不应捏造一行：%+v %v", found, err)
	}
	user := analysis.ItemSpecs{Kind: analysis.KindWanted, Specs: []analysis.SpecValue{{Name: "存储", Value: itemActionPtr("256G")}}, BrandNew: itemActionPtr(false), ConfigKey: itemActionPtr("256G"), Excluded: true, Source: analysis.SpecUser}
	saved, err := saveItemSpecsTx(ctx, tx, taskID, "item", user, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []analysis.SpecSource{analysis.SpecTitle, analysis.SpecDetail} {
		incoming := analysis.ItemSpecs{Kind: analysis.KindSale, Specs: []analysis.SpecValue{{Name: "存储", Value: itemActionPtr("512G")}}, BrandNew: itemActionPtr(true), ConfigKey: itemActionPtr("512G"), Source: source}
		actual, err := saveItemSpecsTx(ctx, tx, taskID, "item", incoming, 200)
		if err != nil || !reflect.DeepEqual(actual, saved) {
			t.Fatalf("%s 覆盖了用户整行：%+v %v", source, actual, err)
		}
		stored, err := loadItemSpecsTx(ctx, tx, taskID, "item")
		if err != nil || !reflect.DeepEqual(stored, &saved) {
			t.Fatalf("%s 没有返回实际沿用的行：%+v %v", source, stored, err)
		}
	}
	user.Specs[0].Value = itemActionPtr("128G")
	user.ConfigKey = itemActionPtr("128G")
	saved, err = saveItemSpecsTx(ctx, tx, taskID, "item", user, 300)
	if err != nil || saved.UpdatedAt != 300 || saved.Specs[0].Value == nil || *saved.Specs[0].Value != "128G" {
		t.Fatalf("明确用户操作不能再次保存：%+v %v", saved, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestItemSpecsTxRoundTripRollbackAndErrors(t *testing.T) {
	ctx, db, taskID := itemActionDB(t)
	tx := itemActionTx(t, ctx, db)
	saved, err := saveItemSpecsTx(ctx, tx, taskID, "item", analysis.ItemSpecs{Source: analysis.SpecTitle}, 10)
	if err != nil || saved.Specs == nil || saved.Kind != "" || saved.BrandNew != nil || saved.ConfigKey != nil {
		t.Fatalf("未知值或空数组丢失：%+v %v", saved, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadItemSpecsTx(ctx, tx, taskID, "item"); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("已结束事务的错误被吞：%v", err)
	}
	tx = itemActionTx(t, ctx, db)
	if found, err := loadItemSpecsTx(ctx, tx, taskID, "item"); err != nil || found != nil {
		t.Fatalf("调用方回滚后仍有数据：%+v %v", found, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := saveItemSpecsTx(cancelled, tx, taskID, "item", analysis.ItemSpecs{Source: analysis.SpecDetail}, 20); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消错误被吞：%v", err)
	}
	if _, err := saveItemSpecsTx(ctx, tx, taskID+1, "item", analysis.ItemSpecs{Source: analysis.SpecDetail}, 20); err == nil {
		t.Fatal("外键错误被吞")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_item_specs(task_id,item_id,specs_json,source,updated_at) VALUES(?, 'broken','{}','title',1)`, taskID); err != nil {
		t.Fatal(err)
	}
	if found, err := loadItemSpecsTx(ctx, tx, taskID, "broken"); err == nil || found != nil {
		t.Fatalf("损坏的规格被当成空值：%+v %v", found, err)
	}
}

func TestItemSpecsDerivedUpdateKeepsOriginalFields(t *testing.T) {
	ctx, db, taskID := itemActionDB(t)
	tx := itemActionTx(t, ctx, db)
	original := analysis.ItemSpecs{Kind: analysis.KindSale, Specs: []analysis.SpecValue{{Name: "旧名称", Value: itemActionPtr("256G")}}, BrandNew: itemActionPtr(true), ConfigKey: itemActionPtr("256G"), Excluded: true, Source: analysis.SpecUser}
	saved, err := saveItemSpecsTx(ctx, tx, taskID, "item", original, 123)
	if err != nil {
		t.Fatal(err)
	}
	if err := updateItemSpecsDerivedTx(ctx, tx, taskID, "item", nil, nil); err != nil {
		t.Fatal(err)
	}
	row, err := loadItemSpecsTx(ctx, tx, taskID, "item")
	if err != nil || row.ConfigKey != nil || !row.Excluded {
		t.Fatalf("只重建配置键时改了排除事实：%+v %v", row, err)
	}
	if err := updateItemSpecsDerivedTx(ctx, tx, taskID, "item", itemActionPtr("256G"), itemActionPtr(false)); err != nil {
		t.Fatal(err)
	}
	row, err = loadItemSpecsTx(ctx, tx, taskID, "item")
	if err != nil || row.Excluded || row.ConfigKey == nil || *row.ConfigKey != "256G" || row.UpdatedAt != saved.UpdatedAt || row.Kind != saved.Kind || row.Source != saved.Source || !reflect.DeepEqual(row.Specs, saved.Specs) || !reflect.DeepEqual(row.BrandNew, saved.BrandNew) {
		t.Fatalf("派生更新碰了用户原始字段：%+v %v", row, err)
	}
	err = updateItemSpecsDerivedTx(ctx, tx, taskID, "missing", nil, itemActionPtr(false))
	var business *ServiceError
	if !errors.As(err, &business) || business.Kind != ErrorNotFound {
		t.Fatalf("派生更新复活了不存在的行：%v", err)
	}
}

type itemActionFixture struct {
	ctx     context.Context
	db      *sql.DB
	service *Service
	taskID  int64
	finding int64
	other   int64
	profile analysis.Profile
	facts   analysis.Facts
	events  []string
}

func itemActions(t *testing.T) *itemActionFixture {
	t.Helper()
	ctx, db, taskID := itemActionDB(t)
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	prefs, err := appsettings.New(ctx, db, settings.Environment{TZ: "UTC"}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	f := &itemActionFixture{ctx: ctx, db: db, taskID: taskID}
	f.profile = analysis.Profile{Specs: []analysis.ProfileSpec{{Name: "存储", Options: []string{"128G", "256G"}, Values: []string{"256G"}, Source: analysis.SourceUser}}, Risks: []analysis.ProfileRisk{{ID: "c3", Name: "屏幕压伤", Attitude: analysis.Mind, Source: analysis.SourceUser, Rev: 1}}, NextNo: 4}
	source, quote := "text", "屏幕没有压伤"
	f.facts = analysis.Facts{Specs: []analysis.SpecValue{{Name: "存储", Value: itemActionPtr("256G")}}, Checks: []analysis.CheckFact{{Key: "c3", Status: analysis.CheckOK, Rev: 1, Source: &source, Evidence: &quote}}, ExtraFindings: []analysis.ExtraFinding{{Name: "电池鼓包", Evidence: "电池有鼓包", Source: "text"}}, Adjust: analysis.Adjustment{Reason: "按文字判断"}, Summary: "需要确认电池"}
	itemActionJSON(t, ctx, db, `UPDATE tasks SET profile_json=?,created_at=1,updated_at=1 WHERE id=?`, f.profile, taskID)
	for i, itemID := range []string{"item", "other"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at,price,description)
			VALUES(?,?,?,1,1,10000,'电池有鼓包，屏幕没有压伤')`, itemID, "测试商品", "https://example.invalid/item"); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(f.facts)
		if err != nil {
			t.Fatal(err)
		}
		result, err := db.ExecContext(ctx, `INSERT INTO findings(task_id,item_id,search_generation,found_at,is_baseline,is_read,decision,stage,decided_layer,
			analysis_json,user_overrides_json,score,grade,scored_at,scored_price,analyzed_at,recommended_price,notified_at)
			VALUES(?,?,1,1,0,0,'scored','done',3,?,'{"risks":{}}',80,'good',1,10000,1,10000,2)`, taskID, itemID, string(raw))
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			f.finding = id
		} else {
			f.other = id
		}
		tx := itemActionTx(t, ctx, db)
		if _, err := saveItemSpecsTx(ctx, tx, taskID, itemID, analysis.ResolveSpecs(f.profile, f.facts.Specs, nil, analysis.SpecDetail), 1); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	f.service = &Service{db: db, settings: prefs, now: func() time.Time { return now }, publish: func(kind string, _ *locales.Message, _ json.RawMessage) error {
		f.events = append(f.events, kind)
		return nil
	}}
	return f
}

func itemActionJSON(t *testing.T, ctx context.Context, db *sql.DB, query string, value any, args ...any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, query, append([]any{string(raw)}, args...)...); err != nil {
		t.Fatal(err)
	}
}

func itemActionKind(t *testing.T, err error, want ErrorKind) {
	t.Helper()
	var business *ServiceError
	if !errors.As(err, &business) || business.Kind != want {
		t.Fatalf("错误种类应为%s，实际%v", want, err)
	}
}

func TestSetItemSpecsPreservesUserFieldsAndAcceptsSightingOnly(t *testing.T) {
	f := itemActions(t)
	if _, err := f.db.ExecContext(f.ctx, `UPDATE task_item_specs SET kind='broken',excluded=1 WHERE task_id=? AND item_id='item'`, f.taskID); err != nil {
		t.Fatal(err)
	}
	result, err := f.service.SetItemSpecs(f.ctx, f.taskID, "item", []analysis.SpecValue{{Name: "存储", Value: itemActionPtr("１２８Ｇ")}}, itemActionPtr(true))
	if err != nil {
		t.Fatal(err)
	}
	if result.ItemSpecs.Source != analysis.SpecUser || result.ItemSpecs.Kind != analysis.KindBroken || result.ItemSpecs.BrandNew == nil || !*result.ItemSpecs.BrandNew || result.ItemSpecs.ConfigKey == nil || *result.ItemSpecs.ConfigKey != "128G" || result.ItemSpecs.Specs[0].Value == nil || *result.ItemSpecs.Specs[0].Value != "128G" || result.ItemSpecs.Excluded {
		t.Fatalf("用户原始值、规范值或派生排除规则错误：%+v", result)
	}
	if _, err := f.db.ExecContext(f.ctx, `INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at,price)
		VALUES('sighting-only','保留行情的商品','https://example.invalid/item',1,1,10000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(f.ctx, `INSERT INTO task_sightings(task_id,item_id,day,price,last_seen_at) VALUES(?,'sighting-only','2026-11-11',10000,1)`, f.taskID); err != nil {
		t.Fatal(err)
	}
	result, err = f.service.SetItemSpecs(f.ctx, f.taskID, "sighting-only", []analysis.SpecValue{{Name: "存储", Value: itemActionPtr("256G")}}, nil)
	if err != nil || result.ItemSpecs.ItemID != "sighting-only" || result.ItemSpecs.Source != analysis.SpecUser {
		t.Fatalf("发现删掉后不能修改仍有行情的规格：%+v %v", result, err)
	}
	_, err = f.service.SetItemSpecs(f.ctx, f.taskID, "unobserved", nil, nil)
	itemActionKind(t, err, ErrorNotFound)
}

func TestSetItemSpecsRejectsUnknownOrDuplicateWithoutDiscardingInput(t *testing.T) {
	for _, values := range [][]analysis.SpecValue{
		{{Name: "不存在的规格", Value: itemActionPtr("256G")}},
		{{Name: "存储", Value: itemActionPtr("1T")}},
		{{Name: "存储", Value: itemActionPtr("128G")}, {Name: "存储", Value: itemActionPtr("256G")}},
	} {
		f := itemActions(t)
		_, err := f.service.SetItemSpecs(f.ctx, f.taskID, "item", values, nil)
		itemActionKind(t, err, ErrorInvalid)
		tx := itemActionTx(t, f.ctx, f.db)
		row, err := loadItemSpecsTx(f.ctx, tx, f.taskID, "item")
		if err != nil || row.Source != analysis.SpecDetail || row.ConfigKey == nil || *row.ConfigKey != "256G" || len(f.events) != 0 {
			t.Fatalf("非法输入仍写库或发事件：%+v %v", row, err)
		}
	}
}

func TestSetRiskOverridesUsesCurrentRevisionAndChangesOnlyThisFinding(t *testing.T) {
	f := itemActions(t)
	var original string
	if err := f.db.QueryRowContext(f.ctx, `SELECT analysis_json FROM findings WHERE id=?`, f.finding).Scan(&original); err != nil {
		t.Fatal(err)
	}
	input := analysis.UserOverrides{Risks: map[string]analysis.RiskOverride{"c3": {Status: analysis.CheckRisk, Rev: 1}}}
	view, err := f.service.SetRiskOverrides(f.ctx, f.finding, input)
	if err != nil || !reflect.DeepEqual(view.UserOverrides, input) {
		t.Fatalf("手动覆盖没有保存：%+v %v", view, err)
	}
	var actual string
	var otherScore, otherScoredAt int64
	if err := f.db.QueryRowContext(f.ctx, `SELECT analysis_json FROM findings WHERE id=?`, f.finding).Scan(&actual); err != nil || actual != original {
		t.Fatalf("覆盖改写了模型事实：%s %v", actual, err)
	}
	if err := f.db.QueryRowContext(f.ctx, `SELECT score,scored_at FROM findings WHERE id=?`, f.other).Scan(&otherScore, &otherScoredAt); err != nil || otherScore != 80 || otherScoredAt != 1 {
		t.Fatalf("单件覆盖改了其他发现：%d %d %v", otherScore, otherScoredAt, err)
	}
	if input.Risks["c3"].Rev != 1 || input.Risks["c3"].Status != analysis.CheckRisk {
		t.Fatal("覆盖入口修改了调用方对象")
	}
	_, err = f.service.SetRiskOverrides(f.ctx, f.finding, analysis.UserOverrides{Risks: map[string]analysis.RiskOverride{"c3": {Status: analysis.CheckOK, Rev: 2}}})
	itemActionKind(t, err, ErrorConflict)
	_, err = f.service.SetRiskOverrides(f.ctx, f.finding, analysis.UserOverrides{Risks: map[string]analysis.RiskOverride{"missing": {Status: analysis.CheckOK, Rev: 1}}})
	itemActionKind(t, err, ErrorInvalid)
	_, err = f.service.SetRiskOverrides(f.ctx, f.finding, analysis.UserOverrides{Risks: map[string]analysis.RiskOverride{"c3": {Status: "bad", Rev: 1}}})
	itemActionKind(t, err, ErrorInvalid)
	view, err = f.service.SetRiskOverrides(f.ctx, f.finding, analysis.UserOverrides{})
	if err != nil || view.UserOverrides.Risks == nil || len(view.UserOverrides.Risks) != 0 {
		t.Fatalf("空对象不能撤销覆盖：%+v %v", view.UserOverrides, err)
	}
}

func TestAddExtraFindingMovesEvidenceAndInvalidatesOldWork(t *testing.T) {
	for _, source := range []string{"text", "image"} {
		t.Run(source, func(t *testing.T) {
			f := itemActions(t)
			expected := f.facts.ExtraFindings[0]
			expected.Source = source
			f.facts.ExtraFindings[0] = expected
			itemActionJSON(t, f.ctx, f.db, `UPDATE findings SET analysis_json=? WHERE id=?`, f.facts, f.finding)
			result, err := f.service.AddExtraFinding(f.ctx, f.finding, expected)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Task.Profile.Risks) != 2 || result.Task.Profile.NextNo != 5 || result.Effects.NeedsReanalysisCount != 1 {
				t.Fatalf("新条目或需要重分析数量错误：%+v", result)
			}
			added := result.Task.Profile.Risks[1]
			if added.ID != "c4" || added.Name != expected.Name || added.Hint != "" || added.Question != "" || added.Attitude != analysis.Mind || added.Source != analysis.SourceUser || !added.Edited || added.Rev != 1 {
				t.Fatalf("自定义检查项字段不符：%+v", added)
			}
			tx := itemActionTx(t, f.ctx, f.db)
			finding, err := loadFindingTx(f.ctx, tx, f.finding)
			if err != nil || finding.Analysis == nil || len(finding.Analysis.ExtraFindings) != 0 || finding.ProcessingRevision != 2 || finding.RecommendedPrice == nil || *finding.RecommendedPrice != 10000 || finding.NotifiedAt == nil || *finding.NotifiedAt != 2 {
				t.Fatalf("事实迁移丢失或改了通知基准：%+v %v", finding, err)
			}
			found := false
			for _, check := range finding.Analysis.Checks {
				if check.Key == added.ID {
					found = check.Rev == added.Rev && check.Status == analysis.CheckRisk && check.Source != nil && *check.Source == source && check.Evidence != nil && *check.Evidence == expected.Evidence
				}
			}
			if !found {
				t.Fatalf("新检查项没有沿用原证据：%+v", finding.Analysis.Checks)
			}
		})
	}
}

func TestAddExtraFindingKeepsParseWarnings(t *testing.T) {
	f := itemActions(t)
	warnings := []locales.Message{
		{Code: "analysis.evidence_missing", Args: map[string]any{"item": "c3"}},
		{Code: "analysis.unknown_field", Args: map[string]any{"field": "unknown_spec"}},
	}
	encoded, err := json.Marshal(f.facts)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &stored); err != nil {
		t.Fatal(err)
	}
	stored["parse_warnings"], err = json.Marshal(warnings)
	if err != nil {
		t.Fatal(err)
	}
	itemActionJSON(t, f.ctx, f.db, `UPDATE findings SET analysis_json=? WHERE id=?`, stored, f.finding)
	if _, err := f.service.AddExtraFinding(f.ctx, f.finding, f.facts.ExtraFindings[0]); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := f.db.QueryRowContext(f.ctx, `SELECT analysis_json FROM findings WHERE id=?`, f.finding).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var record struct {
		Warnings []locales.Message `json:"parse_warnings"`
	}
	if err := json.Unmarshal([]byte(raw), &record); err != nil || !reflect.DeepEqual(record.Warnings, warnings) {
		t.Fatalf("证据迁移清掉了解析警告：%+v %v", record.Warnings, err)
	}
	tx := itemActionTx(t, f.ctx, f.db)
	finding, err := loadFindingTx(f.ctx, tx, f.finding)
	if err != nil || !reflect.DeepEqual(finding.AnalysisWarnings, warnings) || finding.Analysis == nil || len(finding.Analysis.ExtraFindings) != 0 {
		t.Fatalf("迁移后的统一读取未保留警告和事实：%+v %v", finding, err)
	}
}

func TestAddExtraFindingRejectsStaleDuplicateAndOverflow(t *testing.T) {
	t.Run("stale", func(t *testing.T) {
		f := itemActions(t)
		expected := f.facts.ExtraFindings[0]
		expected.Evidence = "页面上旧的证据"
		_, err := f.service.AddExtraFinding(f.ctx, f.finding, expected)
		itemActionKind(t, err, ErrorConflict)
	})
	t.Run("duplicate", func(t *testing.T) {
		f := itemActions(t)
		f.profile.Risks = append(f.profile.Risks, analysis.ProfileRisk{ID: "c4", Name: "电池　鼓包！", Attitude: analysis.Mind, Source: analysis.SourceUser, Rev: 1})
		f.profile.NextNo = 5
		itemActionJSON(t, f.ctx, f.db, `UPDATE tasks SET profile_json=? WHERE id=?`, f.profile, f.taskID)
		_, err := f.service.AddExtraFinding(f.ctx, f.finding, f.facts.ExtraFindings[0])
		itemActionKind(t, err, ErrorConflict)
		var business *ServiceError
		if !errors.As(err, &business) || business.Reason.Code != "profile.check_exists" {
			t.Fatalf("同名项没有给出明确原因：%v", err)
		}
	})
	t.Run("profile_id_overflow", func(t *testing.T) {
		f := itemActions(t)
		f.profile.NextNo = math.MaxInt
		itemActionJSON(t, f.ctx, f.db, `UPDATE tasks SET profile_json=? WHERE id=?`, f.profile, f.taskID)
		_, err := f.service.AddExtraFinding(f.ctx, f.finding, f.facts.ExtraFindings[0])
		itemActionKind(t, err, ErrorInvalid)
	})
}

func TestAddExtraFindingRollsBackFactsWhenTaskSaveFails(t *testing.T) {
	f := itemActions(t)
	if _, err := f.db.ExecContext(f.ctx, `CREATE TRIGGER fail_item_action BEFORE UPDATE ON tasks BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := f.service.AddExtraFinding(f.ctx, f.finding, f.facts.ExtraFindings[0])
	itemActionKind(t, err, ErrorUnavailable)
	tx := itemActionTx(t, f.ctx, f.db)
	finding, err := loadFindingTx(f.ctx, tx, f.finding)
	if err != nil || finding.ProcessingRevision != 1 || finding.Analysis == nil || len(finding.Analysis.ExtraFindings) != 1 || len(finding.Analysis.Checks) != 1 || len(f.events) != 0 {
		t.Fatalf("后续写入失败仍留下半份迁移：%+v %v", finding, err)
	}
}

func TestItemActionsRollbackWhenRecalculationFails(t *testing.T) {
	for _, action := range []string{"specs", "overrides"} {
		t.Run(action, func(t *testing.T) {
			f := itemActions(t)
			if _, err := f.db.ExecContext(f.ctx, `CREATE TRIGGER fail_item_score BEFORE UPDATE OF score_breakdown_json ON findings BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
				t.Fatal(err)
			}
			var err error
			if action == "specs" {
				_, err = f.service.SetItemSpecs(f.ctx, f.taskID, "item", []analysis.SpecValue{{Name: "存储", Value: itemActionPtr("128G")}}, itemActionPtr(true))
			} else {
				_, err = f.service.SetRiskOverrides(f.ctx, f.finding, analysis.UserOverrides{Risks: map[string]analysis.RiskOverride{"c3": {Status: analysis.CheckRisk, Rev: 1}}})
			}
			itemActionKind(t, err, ErrorUnavailable)
			tx := itemActionTx(t, f.ctx, f.db)
			row, err := loadItemSpecsTx(f.ctx, tx, f.taskID, "item")
			if err != nil || row.Source != analysis.SpecDetail || row.ConfigKey == nil || *row.ConfigKey != "256G" || row.BrandNew != nil {
				t.Fatalf("重算失败留下部分规格更新：%+v %v", row, err)
			}
			finding, err := loadFindingTx(f.ctx, tx, f.finding)
			if err != nil || len(finding.UserOverrides.Risks) != 0 || len(f.events) != 0 {
				t.Fatalf("重算失败留下覆盖或发布事件：%+v %v", finding.UserOverrides, err)
			}
		})
	}
}

func TestItemActionsReturnCancellationWithoutWriting(t *testing.T) {
	f := itemActions(t)
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	_, err := f.service.SetItemSpecs(ctx, f.taskID, "item", nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("规格操作丢失取消：%v", err)
	}
	_, err = f.service.SetRiskOverrides(ctx, f.finding, analysis.UserOverrides{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("覆盖操作丢失取消：%v", err)
	}
	_, err = f.service.AddExtraFinding(ctx, f.finding, f.facts.ExtraFindings[0])
	if !errors.Is(err, context.Canceled) || len(f.events) != 0 {
		t.Fatalf("清单操作丢失取消或仍发事件：%v", err)
	}
}

func TestItemActionPublishesAfterCommitAndDoesNotUndoSavedResult(t *testing.T) {
	f := itemActions(t)
	called := 0
	f.service.publish = func(_ string, _ *locales.Message, _ json.RawMessage) error {
		called++
		ctx, cancel := context.WithTimeout(f.ctx, time.Second)
		defer cancel()
		var raw string
		if err := f.db.QueryRowContext(ctx, `SELECT user_overrides_json FROM findings WHERE id=?`, f.finding).Scan(&raw); err != nil {
			t.Fatalf("事件发出时单连接事务尚未提交：%v", err)
		}
		var saved analysis.UserOverrides
		if err := json.Unmarshal([]byte(raw), &saved); err != nil || saved.Risks["c3"].Status != analysis.CheckRisk {
			t.Fatalf("事件先于真实保存：%s %v", raw, err)
		}
		return errors.New("本地事件流已关闭")
	}
	result, err := f.service.SetRiskOverrides(f.ctx, f.finding, analysis.UserOverrides{Risks: map[string]analysis.RiskOverride{"c3": {Status: analysis.CheckRisk, Rev: 1}}})
	if err != nil || called == 0 || result.UserOverrides.Risks["c3"].Status != analysis.CheckRisk {
		t.Fatalf("事件失败使已保存动作被误报失败：%+v %v", result.UserOverrides, err)
	}
}

func TestItemActionsRespectMaintenanceBarrier(t *testing.T) {
	for _, action := range []string{"specs", "overrides", "extra"} {
		t.Run(action, func(t *testing.T) {
			f := itemActions(t)
			for _, query := range []string{
				`CREATE TABLE item_action_write_attempts(table_name TEXT NOT NULL)`,
				`CREATE TRIGGER item_action_specs_insert AFTER INSERT ON task_item_specs BEGIN INSERT INTO item_action_write_attempts VALUES('specs'); END`,
				`CREATE TRIGGER item_action_specs_update AFTER UPDATE ON task_item_specs BEGIN INSERT INTO item_action_write_attempts VALUES('specs'); END`,
				`CREATE TRIGGER item_action_finding_update AFTER UPDATE ON findings BEGIN INSERT INTO item_action_write_attempts VALUES('findings'); END`,
				`CREATE TRIGGER item_action_task_update AFTER UPDATE ON tasks BEGIN INSERT INTO item_action_write_attempts VALUES('tasks'); END`,
			} {
				if _, err := f.db.ExecContext(f.ctx, query); err != nil {
					t.Fatal(err)
				}
			}
			resume, err := f.service.quiesceScope(f.ctx, 0, 0, 0, errUserCancelled)
			if err != nil {
				t.Fatal(err)
			}
			defer resume()
			invoke := func() error {
				switch action {
				case "specs":
					_, err := f.service.SetItemSpecs(f.ctx, f.taskID, "item", []analysis.SpecValue{{Name: "存储", Value: itemActionPtr("128G")}}, nil)
					return err
				case "overrides":
					_, err := f.service.SetRiskOverrides(f.ctx, f.finding, analysis.UserOverrides{Risks: map[string]analysis.RiskOverride{"c3": {Status: analysis.CheckRisk, Rev: 1}}})
					return err
				default:
					_, err := f.service.AddExtraFinding(f.ctx, f.finding, f.facts.ExtraFindings[0])
					return err
				}
			}
			err = invoke()
			var business *ServiceError
			if !errors.As(err, &business) || business.Kind != ErrorUnavailable || business.Reason.Code != "runtime.closed" {
				t.Errorf("维护期间仍接纳写入：%v", err)
			}
			var writes int
			if err := f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM item_action_write_attempts`).Scan(&writes); err != nil {
				t.Fatal(err)
			}
			if writes != 0 || len(f.events) != 0 {
				t.Errorf("维护期间发生业务写入或发布：writes=%d events=%v", writes, f.events)
			}
			resume()
			f.service.publish = func(kind string, _ *locales.Message, _ json.RawMessage) error {
				f.service.mu.Lock()
				active := f.service.mutations
				f.service.mu.Unlock()
				if active != 1 {
					t.Errorf("动作结束前已不受维护等待保护：mutations=%d", active)
				}
				f.events = append(f.events, kind)
				return nil
			}
			if err := invoke(); err != nil {
				t.Fatalf("解除维护后动作没有恢复：%v", err)
			}
			f.service.mu.Lock()
			active := f.service.mutations
			f.service.mu.Unlock()
			if active != 0 {
				t.Fatalf("动作结束后没有释放维护计数：%d", active)
			}
		})
	}
}
