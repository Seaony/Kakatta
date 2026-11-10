package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
)

type reanalysisSelection struct {
	task    Task
	finding Finding
	item    Listing
	layer   analysis.LayerResult
}

func loadReanalysisSelectionTx(ctx context.Context, tx *sql.Tx, id, now int64) (reanalysisSelection, error) {
	finding, err := loadFindingTx(ctx, tx, id)
	if err != nil {
		return reanalysisSelection{}, err
	}
	task, err := loadTaskTx(ctx, tx, finding.TaskID)
	if err != nil {
		return reanalysisSelection{}, err
	}
	item, err := loadListingTx(ctx, tx, finding.ItemID)
	if err != nil {
		return reanalysisSelection{}, err
	}
	layer := analysis.FirstLayer(firstLayerInput(task, item, now, regionCities()))
	return reanalysisSelection{task: task, finding: finding, item: item, layer: layer}, nil
}

func (s *Service) PreviewReanalysis(ctx context.Context, ids []int64) (ReanalysisPreview, error) {
	ids, err := selectedFindingIDs(ids)
	if err != nil {
		return ReanalysisPreview{}, err
	}
	s.initRuntime()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReanalysisPreview{}, storageFailure(err)
	}
	defer tx.Rollback()
	_, _, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return ReanalysisPreview{}, storageFailure(err)
	}
	preview := ReanalysisPreview{AsOf: now.UnixMilli(), RequestedCount: int64(len(ids))}
	for _, id := range ids {
		selection, err := loadReanalysisSelectionTx(ctx, tx, id, preview.AsOf)
		if err != nil {
			return ReanalysisPreview{}, storageFailure(err)
		}
		if selection.layer.Rejected {
			continue
		}
		if detailCached(selection.item.DetailFetchedAt, preview.AsOf) {
			preview.CachedDetailCount++
		} else {
			preview.NeedsDetailCount++
		}
	}
	if err := tx.Commit(); err != nil {
		return ReanalysisPreview{}, storageFailure(err)
	}
	return preview, nil
}

// 批量只入正常队列，即使只有一件也不立即调用模型。
func (s *Service) Reanalyze(ctx context.Context, ids []int64) (ReanalysisResult, error) {
	result, _, err := s.prepareReanalysis(ctx, ids)
	if err == nil {
		s.wakeScheduler()
	}
	return result, err
}

func (s *Service) ReanalyzeOne(ctx context.Context, id int64) (ReanalysisResult, error) {
	result, work, err := s.prepareReanalysis(ctx, []int64{id})
	if err != nil {
		return ReanalysisResult{}, err
	}
	defer s.wakeScheduler()
	if result.Items[0].Status != ReanalysisQueued || result.Items[0].NeedsDetail {
		return result, nil
	}
	if err := s.reanalyzeCached(ctx, work[0]); err != nil {
		return ReanalysisResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReanalysisResult{}, storageFailure(err)
	}
	defer tx.Rollback()
	finding, err := loadFindingTx(ctx, tx, id)
	if err != nil {
		return ReanalysisResult{}, storageFailure(err)
	}
	task, err := loadTaskTx(ctx, tx, finding.TaskID)
	if err != nil {
		return ReanalysisResult{}, storageFailure(err)
	}
	if task.SearchGeneration != work[0].SearchGeneration || finding.SearchGeneration != work[0].SearchGeneration || finding.ProcessingRevision != work[0].ProcessingRevision {
		return ReanalysisResult{}, findingChangedError()
	}
	result.Items[0].Reason = finding.Reason
	if finding.Decision != DecisionPending {
		result.Items[0].Status = ReanalysisCached
	}
	if err := tx.Commit(); err != nil {
		return ReanalysisResult{}, storageFailure(err)
	}
	return result, nil
}

func (s *Service) prepareReanalysis(ctx context.Context, ids []int64) (ReanalysisResult, []WorkRef, error) {
	ids, err := selectedFindingIDs(ids)
	if err != nil {
		return ReanalysisResult{}, nil, err
	}
	done, err := s.beginMutation()
	if err != nil {
		return ReanalysisResult{}, nil, err
	}
	defer done()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReanalysisResult{}, nil, storageFailure(err)
	}
	defer tx.Rollback()
	_, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return ReanalysisResult{}, nil, storageFailure(err)
	}
	selections := make([]reanalysisSelection, 0, len(ids))
	for _, id := range ids {
		selection, err := loadReanalysisSelectionTx(ctx, tx, id, now.UnixMilli())
		if err != nil {
			return ReanalysisResult{}, nil, storageFailure(err)
		}
		selections = append(selections, selection)
	}
	result := ReanalysisResult{Items: make([]ReanalysisItem, 0, len(ids))}
	work := make([]WorkRef, 0, len(ids))
	for _, selection := range selections {
		response, ref, err := prepareReanalysisTx(ctx, tx, selection.task, selection.finding, selection.item, selection.layer, now.UnixMilli())
		if err != nil {
			return ReanalysisResult{}, nil, storageFailure(err)
		}
		result.Items = append(result.Items, response)
		work = append(work, ref)
		if response.NeedsDetail {
			result.NeedsDetailCount++
		}
	}
	if err := appsettings.SaveClock(ctx, tx, clock); err != nil {
		return ReanalysisResult{}, nil, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return ReanalysisResult{}, nil, storageFailure(err)
	}
	_ = s.emit("finding.changed", map[string]any{"ids": ids})
	return result, work, nil
}

func prepareReanalysisTx(ctx context.Context, tx *sql.Tx, task Task, finding Finding, item Listing, layer analysis.LayerResult, now int64) (ReanalysisItem, WorkRef, error) {
	if finding.ProcessingRevision < 1 {
		return ReanalysisItem{}, WorkRef{}, errors.New("发现处理版本无效")
	}
	decision, stage := DecisionPending, StageTitle
	if task.JudgeMode == analysis.ModeKeyword {
		stage = StageDetail
	}
	var rejected *analysis.RejectKind
	var decidedLayer, requestedAt *int64
	var rawReason *string
	response := ReanalysisItem{FindingID: finding.ID, Status: ReanalysisQueued, NeedsDetail: !detailCached(item.DetailFetchedAt, now)}
	if layer.Rejected {
		decision, stage = analysis.DecisionRejected, StageDone
		level := int64(1)
		decidedLayer, rejected = &level, &layer.RejectKind
		response.Status, response.NeedsDetail, response.Reason = ReanalysisRejected, false, layer.Reason
		if layer.Reason != nil {
			raw, err := json.Marshal(layer.Reason)
			if err != nil {
				return ReanalysisItem{}, WorkRef{}, err
			}
			text := string(raw)
			rawReason = &text
		}
	} else {
		requestedAt = &now
	}
	work := WorkRef{RunRef: RunRef{TaskID: task.ID, SearchGeneration: task.SearchGeneration}, FindingID: finding.ID, ProcessingRevision: finding.ProcessingRevision + 1}
	result, err := tx.ExecContext(ctx, `UPDATE findings SET decision=?,stage=?,reject_kind=?,decided_layer=?,reason=?,
		search_generation=?,processing_revision=?,attempts=0,retryable=0,reanalyze_requested_at=?,reanalysis_origin=1
		WHERE id=? AND task_id=? AND search_generation=? AND processing_revision=?`,
		decision, stage, rejected, decidedLayer, rawReason, task.SearchGeneration, work.ProcessingRevision, requestedAt,
		finding.ID, task.ID, finding.SearchGeneration, finding.ProcessingRevision)
	if err != nil {
		return ReanalysisItem{}, WorkRef{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return ReanalysisItem{}, WorkRef{}, err
	}
	if count != 1 {
		return ReanalysisItem{}, WorkRef{}, &ServiceError{Kind: ErrorConflict, Reason: locales.Message{Code: "findings.changed", Args: map[string]any{}}}
	}
	return response, work, nil
}
