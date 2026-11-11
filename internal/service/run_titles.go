package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/analysis/prompts"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func loadPromptSnapshotTx(ctx context.Context, tx *sql.Tx) (prompts.Snapshot, error) {
	rows, err := tx.QueryContext(ctx, `SELECT key,content FROM prompts`)
	if err != nil {
		return prompts.Snapshot{}, err
	}
	defer rows.Close()
	contents := map[client.Kind]string{}
	for rows.Next() {
		var key client.Kind
		var content string
		if err := rows.Scan(&key, &content); err != nil {
			return prompts.Snapshot{}, err
		}
		contents[key] = content
	}
	if err := rows.Err(); err != nil {
		return prompts.Snapshot{}, err
	}
	return prompts.Resolve(contents)
}

func (s *Service) runTitles(ctx context.Context, run *runExecution) (err error) {
	defer func() {
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err(), context.Cause(ctx))
		}
		err = storageFailure(err)
	}()
	findings, captured, err := s.titleInputs(ctx, run)
	if err != nil {
		return err
	}
	for start := 0; start < len(findings); start += 20 {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(start+20, len(findings))
		task, batch, sources, err := s.currentTitleBatch(ctx, run.Ref, findings[start:end])
		if err != nil {
			return err
		}
		if task.JudgeMode != analysis.ModeAI {
			break
		}
		if len(batch) == 0 {
			continue
		}
		request, err := prompts.BuildTitle(run.Prompts, &run.Ref.TaskID, task.Profile, sources)
		if err != nil {
			return err
		}
		var result client.Result
		callErr := s.guardedWork(ctx, run, "标题整理", func(workCtx context.Context) error {
			if s.models == nil {
				return client.ErrNoProvider
			}
			var modelErr error
			result, modelErr = s.completeRunModel(workCtx, run.Ref.RunID, request)
			return modelErr
		})
		if web.IsStop(callErr) || errors.Is(callErr, web.ErrQuota) {
			return callErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if callErr != nil {
			if errors.Is(callErr, client.ErrNoProvider) || errors.Is(callErr, client.ErrDailyLimit) {
				cause := "no_provider"
				if errors.Is(callErr, client.ErrDailyLimit) {
					cause = "daily_limit"
				}
				waiting, err := s.awaitTitleWork(ctx, run, findings[start:], captured, cause)
				if err != nil {
					return err
				}
				if waiting {
					run.P = nil
					return nil
				}
				return s.loadTitleProcessing(ctx, run)
			}
			if errors.Is(callErr, client.ErrInfrastructure) || errors.Is(callErr, client.ErrConfigChanged) ||
				errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded) {
				return callErr
			}
			var failure *client.Failure
			if !errors.As(callErr, &failure) {
				return callErr
			}
			if err := s.failTitleBatch(ctx, run.Ref, batch, failure.Last, failure.Retryable); err != nil {
				return err
			}
			run.Layer2Count += int64(len(batch))
			continue
		}
		screen, warnings, err := analysis.ParseTitleScreen([]byte(result.Text), len(batch))
		if err != nil {
			// 正式客户端已用同一解析器校验；替换依赖也不能把坏结果写成通过。
			return fmt.Errorf("%w：标题整理返回未通过校验：%v", client.ErrInfrastructure, err)
		}
		conclusions, titleWarnings, err := s.saveTitleBatch(ctx, run.Ref, batch, task.Profile, screen.Items)
		if err != nil {
			return err
		}
		run.Layer2Count += int64(len(batch))
		run.Layer2Rejected += int64(len(conclusions))
		run.FirstConclusionIDs = appendTitleIDs(run.FirstConclusionIDs, conclusions)
		run.Warnings = append(run.Warnings, warnings...)
		run.Warnings = append(run.Warnings, titleWarnings...)
	}
	return s.loadTitleProcessing(ctx, run)
}

func (s *Service) completeRunModel(ctx context.Context, runID int64, request client.Request) (client.Result, error) {
	result, modelErr := s.models.Complete(ctx, request)
	if len(result.JSONDowngrades) == 0 {
		return result, modelErr
	}
	// 降级已经写入模型兼容设置，运行取消后仍要记下发生过的步骤。
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(logCtx, nil)
	if err != nil {
		return result, storageFailure(err)
	}
	defer tx.Rollback()
	_, clock, now, err := s.observeNowTx(logCtx, tx)
	if err != nil {
		return result, err
	}
	at, status := now.UnixMilli(), StepFail
	for _, downgrade := range result.JSONDowngrades {
		message := findingMessage("run.model_json_downgraded", map[string]any{
			"from": string(downgrade.From), "to": string(downgrade.To), "provider": downgrade.Provider, "model": downgrade.Model,
		})
		if _, err := appendRunStepTx(logCtx, tx, RunStep{RunID: runID, At: &at, Status: &status, Message: &message}); err != nil {
			return result, storageFailure(err)
		}
	}
	if err := appsettings.SaveClock(logCtx, tx, clock); err != nil {
		return result, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return result, storageFailure(err)
	}
	return result, modelErr
}

func (s *Service) titleInputs(ctx context.Context, run *runExecution) ([]Finding, map[int64]int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	task, err := titleTaskTx(ctx, tx, run.Ref)
	if err != nil {
		return nil, nil, err
	}
	findings := []Finding{}
	captured := map[int64]int64{}
	if task.JudgeMode != analysis.ModeAI {
		return findings, captured, nil
	}
	ids := appendTitleIDs(nil, run.SearchSaved.NewFindingIDs)
	ids = appendTitleIDs(ids, run.SearchSaved.TitleOnlyFindingIDs)
	only := map[int64]bool{}
	for _, id := range run.SearchSaved.TitleOnlyFindingIDs {
		only[id] = true
	}
	backlog := map[int64]Finding{}
	for _, finding := range run.Backlog {
		backlog[finding.ID] = finding
		ids = append(ids, finding.ID)
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		finding, err := loadFindingTx(ctx, tx, id)
		if isMissingTitleWork(err) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if finding.TaskID != run.Ref.TaskID || finding.SearchGeneration != run.Ref.SearchGeneration {
			continue
		}
		if captured, ok := backlog[id]; ok && captured.ProcessingRevision != finding.ProcessingRevision {
			continue
		}
		captured[id] = finding.ProcessingRevision
		if finding.Stage != StageTitle && !(only[id] && finding.Stage == StageDone) {
			continue
		}
		findings = append(findings, finding)
	}
	return findings, captured, nil
}

func (s *Service) currentTitleBatch(ctx context.Context, ref RunRef, findings []Finding) (Task, []Finding, []prompts.TitleSource, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, nil, nil, err
	}
	defer tx.Rollback()
	task, err := titleTaskTx(ctx, tx, ref)
	if err != nil {
		return Task{}, nil, nil, err
	}
	batch, sources := []Finding{}, []prompts.TitleSource{}
	if task.JudgeMode != analysis.ModeAI {
		return task, batch, sources, nil
	}
	for _, finding := range findings {
		current, err := titleWorkCurrentTx(ctx, tx, titleWorkRef(ref, finding), finding.Stage)
		if err != nil {
			return Task{}, nil, nil, err
		}
		if !current {
			continue
		}
		item, err := loadListingTx(ctx, tx, finding.ItemID)
		if err != nil {
			return Task{}, nil, nil, err
		}
		excluded, err := titleExcludedTx(ctx, tx, task, item)
		if err != nil {
			return Task{}, nil, nil, err
		}
		if !excluded {
			batch = append(batch, finding)
			sources = append(sources, prompts.TitleSource{Title: item.Title, PriceCents: item.Price, Tags: item.Tags})
		}
	}
	return task, batch, sources, nil
}

func titleExcludedTx(ctx context.Context, tx *sql.Tx, task Task, item Listing) (bool, error) {
	if item.IsDismissed || len(analysis.MatchingKeywords(item.Title, task.ExcludeKeywords)) != 0 {
		return true, nil
	}
	if item.SellerID != nil {
		return isSellerBlockedTx(ctx, tx, *item.SellerID)
	}
	return false, nil
}

func (s *Service) saveTitleBatch(ctx context.Context, ref RunRef, findings []Finding, profile analysis.Profile, items []analysis.TitleItem) ([]int64, []locales.Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	task, err := titleTaskTx(ctx, tx, ref)
	if err != nil {
		return nil, nil, err
	}
	conclusions, changed, warnings := []int64{}, []int64{}, []locales.Message{}
	for i, finding := range findings {
		work := titleWorkRef(ref, finding)
		current, err := titleWorkCurrentTx(ctx, tx, work, finding.Stage)
		if err != nil {
			return nil, nil, err
		}
		if !current {
			continue
		}
		item, err := loadListingTx(ctx, tx, finding.ItemID)
		if err != nil {
			return nil, nil, err
		}
		excluded, err := titleExcludedTx(ctx, tx, task, item)
		if err != nil {
			return nil, nil, err
		}
		if excluded {
			continue
		}
		result := analysis.JudgeTitle(profile, items[i])
		warnings = append(warnings, result.Warnings...)
		if !sameTitleRules(profile, task.Profile) {
			current := analysis.JudgeTitle(task.Profile, titleForCurrentProfile(profile, task.Profile, result.Item))
			warnings = append(warnings, current.Warnings...)
		}
		if finding.Stage == StageDone {
			err = saveTitleOnlyTx(ctx, tx, work, profile, result, s.now().UnixMilli())
		} else {
			var decided bool
			decided, err = saveTitleTx(ctx, tx, work, profile, result, s.now().UnixMilli())
			if decided {
				conclusions = append(conclusions, finding.ID)
			}
		}
		if err != nil {
			return nil, nil, err
		}
		changed = append(changed, finding.ID)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	if len(changed) != 0 {
		_ = s.emit("finding.changed", map[string]any{"ids": changed})
	}
	return conclusions, warnings, nil
}

func (s *Service) failTitleBatch(ctx context.Context, ref RunRef, findings []Finding, reason locales.Message, retryable bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	changed := []int64{}
	for _, finding := range findings {
		if finding.Stage != StageTitle {
			continue
		}
		work := titleWorkRef(ref, finding)
		current, err := titleWorkCurrentTx(ctx, tx, work, StageTitle)
		if err != nil {
			return err
		}
		if !current {
			continue
		}
		if err := saveFindingFailureTx(ctx, tx, work, StageTitle, reason, retryable, false, s.now().UnixMilli()); err != nil {
			return err
		}
		changed = append(changed, finding.ID)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if len(changed) != 0 {
		_ = s.emit("finding.changed", map[string]any{"ids": changed})
	}
	return nil
}

func (s *Service) awaitTitleWork(ctx context.Context, run *runExecution, findings []Finding, captured map[int64]int64, cause string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	task, err := titleTaskTx(ctx, tx, run.Ref)
	if err != nil {
		return false, err
	}
	if task.JudgeMode != analysis.ModeAI {
		return false, nil
	}
	works := []WorkRef{}
	stages := map[int64]FindingStage{}
	for _, finding := range findings {
		if finding.Stage == StageTitle {
			works = append(works, titleWorkRef(run.Ref, finding))
			stages[finding.ID] = StageTitle
		}
	}
	qIDs := make([]int64, 0, len(run.Backlog))
	for _, finding := range run.Backlog {
		qIDs = append(qIDs, finding.ID)
	}
	details, err := loadProcessingTx(ctx, tx, run.Ref, run.SearchSaved.NewFindingIDs, qIDs, math.MaxInt64)
	if err != nil {
		return false, err
	}
	for _, detail := range details {
		if captured[detail.FindingID] == detail.ProcessingRevision {
			works = append(works, detail)
			stages[detail.FindingID] = StageDetail
		}
	}
	reason := locales.Message{Code: "await_ai", Args: map[string]any{"cause": cause}}
	changed := []int64{}
	for _, work := range works {
		stage := stages[work.FindingID]
		current, err := titleWorkCurrentTx(ctx, tx, work, stage)
		if err != nil {
			return false, err
		}
		if !current {
			continue
		}
		finding, err := loadFindingTx(ctx, tx, work.FindingID)
		if err != nil {
			return false, err
		}
		item, err := loadListingTx(ctx, tx, finding.ItemID)
		if err != nil {
			return false, err
		}
		excluded, err := titleExcludedTx(ctx, tx, task, item)
		if err != nil {
			return false, err
		}
		if excluded {
			continue
		}
		if err := saveFindingFailureTx(ctx, tx, work, stage, reason, false, true, s.now().UnixMilli()); err != nil {
			return false, err
		}
		changed = append(changed, work.FindingID)
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if len(changed) != 0 {
		_ = s.emit("finding.changed", map[string]any{"ids": changed})
	}
	return true, nil
}

func (s *Service) loadTitleProcessing(ctx context.Context, run *runExecution) error {
	var limit int64
	if err := json.Unmarshal(run.Snapshot.Values["ai.max_items_per_run"], &limit); err != nil || limit < 1 {
		return errors.New("每次分析商品数量无效")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := titleTaskTx(ctx, tx, run.Ref); err != nil {
		return err
	}
	qIDs := make([]int64, 0, len(run.Backlog))
	for _, finding := range run.Backlog {
		qIDs = append(qIDs, finding.ID)
	}
	works, err := loadProcessingTx(ctx, tx, run.Ref, run.SearchSaved.NewFindingIDs, qIDs, limit)
	if err != nil {
		return err
	}
	run.P = works
	return nil
}

func titleWorkRef(ref RunRef, finding Finding) WorkRef {
	return WorkRef{RunRef: ref, FindingID: finding.ID, ProcessingRevision: finding.ProcessingRevision}
}

func titleTaskTx(ctx context.Context, tx *sql.Tx, ref RunRef) (Task, error) {
	task, err := loadTaskTx(ctx, tx, ref.TaskID)
	if isMissingTitleWork(err) {
		return Task{}, errTaskDeleted
	}
	if err != nil {
		return Task{}, err
	}
	if task.SearchGeneration != ref.SearchGeneration {
		return Task{}, errSearchChanged
	}
	return task, nil
}

func titleWorkCurrentTx(ctx context.Context, tx *sql.Tx, work WorkRef, stage FindingStage) (bool, error) {
	_, _, err := validateWorkTx(ctx, tx, work, stage)
	if err == nil {
		return true, nil
	}
	var business *ServiceError
	if !errors.As(err, &business) || business.Kind != ErrorConflict && business.Kind != ErrorNotFound {
		return false, err
	}
	task, taskErr := loadTaskTx(ctx, tx, work.TaskID)
	if isMissingTitleWork(taskErr) {
		return false, errTaskDeleted
	}
	if taskErr != nil {
		return false, taskErr
	}
	if task.SearchGeneration != work.SearchGeneration {
		return false, errSearchChanged
	}
	return false, nil
}

func isMissingTitleWork(err error) bool {
	var business *ServiceError
	return errors.As(err, &business) && business.Kind == ErrorNotFound
}

func appendTitleIDs(ids, added []int64) []int64 {
	seen := make(map[int64]bool, len(ids)+len(added))
	for _, id := range ids {
		seen[id] = true
	}
	for _, id := range added {
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids
}
