package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
)

func loadItemSpecsTx(ctx context.Context, tx *sql.Tx, taskID int64, itemID string) (*TaskItemSpecs, error) {
	var row TaskItemSpecs
	var kind, raw, config sql.NullString
	var brandNew sql.NullBool
	err := tx.QueryRowContext(ctx, `SELECT task_id,item_id,kind,specs_json,brand_new,config_key,excluded,source,updated_at
		FROM task_item_specs WHERE task_id=? AND item_id=?`, taskID, itemID).
		Scan(&row.TaskID, &row.ItemID, &kind, &raw, &brandNew, &config, &row.Excluded, &row.Source, &row.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.Specs = []analysis.SpecValue{}
	if raw.Valid && json.Unmarshal([]byte(raw.String), &row.Specs) != nil {
		return nil, errors.New("商品规格记录损坏")
	}
	if row.Specs == nil {
		row.Specs = []analysis.SpecValue{}
	}
	row.Kind = analysis.Kind(kind.String)
	if brandNew.Valid {
		row.BrandNew = &brandNew.Bool
	}
	if config.Valid {
		row.ConfigKey = &config.String
	}
	return &row, nil
}

func saveItemSpecsTx(ctx context.Context, tx *sql.Tx, taskID int64, itemID string, specs analysis.ItemSpecs, nowMS int64) (TaskItemSpecs, error) {
	old, err := loadItemSpecsTx(ctx, tx, taskID, itemID)
	if err != nil {
		return TaskItemSpecs{}, err
	}
	if old != nil && old.Source == analysis.SpecUser && specs.Source != analysis.SpecUser {
		return *old, nil
	}
	values := make([]analysis.SpecValue, len(specs.Specs))
	for i, spec := range specs.Specs {
		values[i] = analysis.SpecValue{Name: spec.Name}
		if spec.Value != nil {
			value := *spec.Value
			values[i].Value = &value
		}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return TaskItemSpecs{}, err
	}
	var kind any
	if specs.Kind != "" {
		kind = string(specs.Kind)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_item_specs(task_id,item_id,kind,specs_json,brand_new,config_key,excluded,source,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(task_id,item_id) DO UPDATE SET
		kind=excluded.kind,specs_json=excluded.specs_json,brand_new=excluded.brand_new,config_key=excluded.config_key,
		excluded=excluded.excluded,source=excluded.source,updated_at=excluded.updated_at`,
		taskID, itemID, kind, string(raw), specs.BrandNew, specs.ConfigKey, specs.Excluded, specs.Source, nowMS)
	if err != nil {
		return TaskItemSpecs{}, err
	}
	row, err := loadItemSpecsTx(ctx, tx, taskID, itemID)
	if err != nil {
		return TaskItemSpecs{}, err
	}
	if row == nil {
		return TaskItemSpecs{}, errors.New("商品规格保存后不存在")
	}
	return *row, nil
}

func updateItemSpecsDerivedTx(ctx context.Context, tx *sql.Tx, taskID int64, itemID string, configKey *string, excluded *bool) error {
	query := `UPDATE task_item_specs SET config_key=? WHERE task_id=? AND item_id=?`
	args := []any{configKey, taskID, itemID}
	if excluded != nil {
		query = `UPDATE task_item_specs SET config_key=?,excluded=? WHERE task_id=? AND item_id=?`
		args = []any{configKey, *excluded, taskID, itemID}
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return &ServiceError{Kind: ErrorNotFound, Reason: locales.Message{Code: "listings.not_found"}}
	}
	return nil
}

func invalidItemAction(field string) error {
	reason := locales.Message{Code: "tasks.invalid", Args: map[string]any{}}
	return invalidTaskField(field, reason)
}

func changedItemAction() error {
	return &ServiceError{Kind: ErrorConflict, Reason: locales.Message{Code: "findings.changed", Args: map[string]any{}}}
}

func userItemSpecs(profile analysis.Profile, values []analysis.SpecValue, brandNew *bool) (analysis.ItemSpecs, error) {
	known := make(map[string]bool, len(profile.Specs))
	for _, spec := range profile.Specs {
		known[spec.Name] = true
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !known[value.Name] || seen[value.Name] {
			return analysis.ItemSpecs{}, invalidItemAction("specs")
		}
		seen[value.Name] = true
	}
	resolved := analysis.ResolveSpecs(profile, values, brandNew, analysis.SpecUser)
	for _, value := range values {
		if value.Value == nil {
			continue
		}
		for _, current := range resolved.Specs {
			if current.Name == value.Name && current.Value == nil {
				return analysis.ItemSpecs{}, invalidItemAction("specs")
			}
		}
	}
	return resolved, nil
}

func (s *Service) SetItemSpecs(ctx context.Context, taskID int64, itemID string, values []analysis.SpecValue, brandNew *bool) (ItemSpecsUpdateResult, error) {
	release, err := s.beginMutation()
	if err != nil {
		return ItemSpecsUpdateResult{}, err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ItemSpecsUpdateResult{}, storageFailure(err)
	}
	defer tx.Rollback()
	task, err := loadTaskTx(ctx, tx, taskID)
	if err != nil {
		return ItemSpecsUpdateResult{}, storageFailure(err)
	}
	var observed bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM findings WHERE task_id=? AND item_id=?)
		OR EXISTS(SELECT 1 FROM task_sightings WHERE task_id=? AND item_id=?)`, taskID, itemID, taskID, itemID).Scan(&observed); err != nil {
		return ItemSpecsUpdateResult{}, storageFailure(err)
	}
	if !observed {
		return ItemSpecsUpdateResult{}, &ServiceError{Kind: ErrorNotFound, Reason: locales.Message{Code: "listings.not_found"}}
	}
	resolved, err := userItemSpecs(task.Profile, values, brandNew)
	if err != nil {
		return ItemSpecsUpdateResult{}, err
	}
	old, err := loadItemSpecsTx(ctx, tx, taskID, itemID)
	if err != nil {
		return ItemSpecsUpdateResult{}, storageFailure(err)
	}
	if old != nil {
		resolved.Kind, resolved.Excluded = old.Kind, old.Excluded
	}
	now := s.now().UnixMilli()
	if _, err := saveItemSpecsTx(ctx, tx, taskID, itemID, resolved, now); err != nil {
		return ItemSpecsUpdateResult{}, storageFailure(err)
	}
	count, err := s.recalculateTaskTx(ctx, tx, task, now)
	if err != nil {
		return ItemSpecsUpdateResult{}, storageFailure(err)
	}
	saved, err := loadItemSpecsTx(ctx, tx, taskID, itemID)
	if err != nil {
		return ItemSpecsUpdateResult{}, storageFailure(err)
	}
	if saved == nil {
		return ItemSpecsUpdateResult{}, storageFailure(errors.New("重算后商品规格不存在"))
	}
	if err := tx.Commit(); err != nil {
		return ItemSpecsUpdateResult{}, storageFailure(err)
	}
	_ = s.emit("finding.changed", map[string]any{"task_id": taskID})
	return ItemSpecsUpdateResult{ItemSpecs: *saved, RescoredCount: count}, nil
}

func (s *Service) SetRiskOverrides(ctx context.Context, findingID int64, overrides analysis.UserOverrides) (FindingView, error) {
	release, err := s.beginMutation()
	if err != nil {
		return FindingView{}, err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FindingView{}, storageFailure(err)
	}
	defer tx.Rollback()
	finding, err := loadFindingTx(ctx, tx, findingID)
	if err != nil {
		return FindingView{}, storageFailure(err)
	}
	task, err := loadTaskTx(ctx, tx, finding.TaskID)
	if err != nil {
		return FindingView{}, storageFailure(err)
	}
	current := make(map[string]int64, len(task.Profile.Risks))
	for _, risk := range task.Profile.Risks {
		rev := risk.Rev
		if rev == 0 {
			rev = 1
		}
		current[risk.ID] = rev
	}
	saved := analysis.UserOverrides{Risks: make(map[string]analysis.RiskOverride, len(overrides.Risks))}
	for id, value := range overrides.Risks {
		rev, exists := current[id]
		if !exists || value.Rev < limits.MinSafeInteger || value.Rev > limits.MaxSafeInteger ||
			value.Status != analysis.CheckOK && value.Status != analysis.CheckRisk && value.Status != analysis.CheckSuspect && value.Status != analysis.CheckUnknown {
			return FindingView{}, invalidItemAction("user_overrides.risks." + id)
		}
		if value.Rev != rev || rev <= 0 {
			return FindingView{}, changedItemAction()
		}
		saved.Risks[id] = analysis.RiskOverride{Status: value.Status, Rev: rev}
	}
	raw, err := json.Marshal(saved)
	if err != nil {
		return FindingView{}, storageFailure(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE findings SET user_overrides_json=? WHERE id=?`, string(raw), findingID); err != nil {
		return FindingView{}, storageFailure(err)
	}
	if _, err := s.recalculateFindingTx(ctx, tx, task, findingID, s.now().UnixMilli()); err != nil {
		return FindingView{}, storageFailure(err)
	}
	view, err := getFindingViewTx(ctx, tx, findingID)
	if err != nil {
		return FindingView{}, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return FindingView{}, storageFailure(err)
	}
	_ = s.emit("finding.changed", map[string]any{"finding_id": findingID, "task_id": finding.TaskID})
	return view, nil
}

func (s *Service) AddExtraFinding(ctx context.Context, findingID int64, expected analysis.ExtraFinding) (TaskSaveResult, error) {
	release, err := s.beginMutation()
	if err != nil {
		return TaskSaveResult{}, err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	defer tx.Rollback()
	finding, err := loadFindingTx(ctx, tx, findingID)
	if err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	task, err := loadTaskTx(ctx, tx, finding.TaskID)
	if err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	if task.JudgeMode != analysis.ModeAI || finding.Analysis == nil {
		return TaskSaveResult{}, changedItemAction()
	}
	index := -1
	for i, extra := range finding.Analysis.ExtraFindings {
		if extra == expected {
			index = i
			break
		}
	}
	if index < 0 || expected.Source != "text" && expected.Source != "image" {
		return TaskSaveResult{}, changedItemAction()
	}
	name := analysis.RiskNameKey(expected.Name)
	if name == "" {
		return TaskSaveResult{}, invalidItemAction("extra_finding")
	}
	for _, risk := range analysis.ResolveRisks(task.Profile) {
		if analysis.RiskNameKey(risk.Name) == name {
			return TaskSaveResult{}, &ServiceError{Kind: ErrorConflict, Reason: locales.Message{Code: "profile.check_exists"}}
		}
	}
	proposed := task.Profile
	proposed.Risks = append(append([]analysis.ProfileRisk{}, task.Profile.Risks...), analysis.ProfileRisk{
		Name: expected.Name, Attitude: analysis.Mind, Source: analysis.SourceUser, Edited: true,
	})
	profile, warnings, err := analysis.ReviseProfile(task.Profile, proposed)
	if err != nil {
		var invalid *analysis.Error
		if errors.As(err, &invalid) {
			return TaskSaveResult{}, invalidTaskField("profile", invalid.Message)
		}
		return TaskSaveResult{}, storageFailure(err)
	}
	added := profile.Risks[len(profile.Risks)-1]
	facts := *finding.Analysis
	facts.Checks = append(append([]analysis.CheckFact{}, facts.Checks...), analysis.CheckFact{
		Key: added.ID, Rev: added.Rev, Status: analysis.CheckRisk, Source: &expected.Source, Evidence: &expected.Evidence,
	})
	facts.ExtraFindings = append(append([]analysis.ExtraFinding{}, facts.ExtraFindings[:index]...), facts.ExtraFindings[index+1:]...)
	raw, err := encodeFindingFacts(facts, finding.AnalysisWarnings)
	if err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE findings SET analysis_json=?,processing_revision=processing_revision+1
		WHERE id=? AND task_id=? AND processing_revision=?`, raw, findingID, task.ID, finding.ProcessingRevision)
	if err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	if changed == 0 {
		return TaskSaveResult{}, changedItemAction()
	}
	now := s.now().UnixMilli()
	updated := task
	updated.Profile, updated.UpdatedAt, updated.NoGoodHintDismissedAt = profile, &now, &now
	if err := saveTaskTx(ctx, tx, updated); err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	effects, err := s.reprofileEffectsTx(ctx, tx, task, updated, now)
	if err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	issues := make([]settings.Issue, 0, len(warnings))
	for _, warning := range warnings {
		issues = append(issues, settings.Issue{Key: "profile", Reason: warning})
	}
	if err := tx.Commit(); err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	_ = s.emit("task.changed", map[string]any{"task_id": task.ID})
	_ = s.emit("finding.changed", map[string]any{"task_id": task.ID})
	return TaskSaveResult{Task: updated, Effects: effects, Warnings: issues}, nil
}
