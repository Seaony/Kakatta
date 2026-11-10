package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
)

const findingSelectColumns = `f.id,f.task_id,f.item_id,f.search_generation,f.processing_revision,
	f.found_at,f.is_baseline,f.decision,f.stage,f.reject_kind,f.decided_layer,f.reason,
	f.analysis_json,f.user_overrides_json,f.score,f.grade,f.score_breakdown_json,
	f.fair_price,f.fair_sample,f.fair_basis,f.scored_price,f.scored_at,f.summary,
	f.title_screen_reason,f.analysis_provider_id,f.analysis_model,f.analyzed_at,f.seller_type,
	f.matched_keywords_json,f.attempts,f.retryable,f.reanalyze_requested_at,f.is_read,
	f.notify_kind,f.notify_skip_reason,f.notified_at,f.recommended_price,
	f.last_drop_notified_price,f.drop_notified_at,f.seller_checks_json`

type findingScanner interface{ Scan(...any) error }

func scanFinding(row findingScanner) (Finding, error) {
	var finding Finding
	var reason, facts, overrides, breakdown, fairBasis, keywords, skipReason, sellerAssessment sql.NullString
	err := row.Scan(&finding.ID, &finding.TaskID, &finding.ItemID, &finding.SearchGeneration,
		&finding.ProcessingRevision, &finding.FoundAt, &finding.IsBaseline, &finding.Decision,
		&finding.Stage, &finding.RejectKind, &finding.DecidedLayer, &reason, &facts, &overrides,
		&finding.Score, &finding.Grade, &breakdown, &finding.FairPrice, &finding.FairSample,
		&fairBasis, &finding.ScoredPrice, &finding.ScoredAt, &finding.Summary,
		&finding.TitleScreenReason, &finding.AnalysisProviderID, &finding.AnalysisModel,
		&finding.AnalyzedAt, &finding.SellerType, &keywords, &finding.Attempts,
		&finding.Retryable, &finding.ReanalyzeRequestedAt, &finding.IsRead,
		&finding.NotifyKind, &skipReason, &finding.NotifiedAt, &finding.RecommendedPrice,
		&finding.LastDropNotifiedPrice, &finding.DropNotifiedAt, &sellerAssessment)
	if err != nil {
		return Finding{}, err
	}
	if sellerAssessment.Valid {
		finding.SellerAssessment, err = decodeSellerAssessment(sellerAssessment.String)
		if err != nil {
			return Finding{}, fmt.Errorf("发现的卖家判断损坏：%w", err)
		}
	}
	finding.UserOverrides.Risks = map[string]analysis.RiskOverride{}
	finding.MatchedKeywords = []string{}
	finding.AnalysisWarnings = []locales.Message{}
	if facts.Valid {
		finding.Analysis, finding.AnalysisWarnings, err = decodeFindingFacts(facts.String)
		if err != nil {
			return Finding{}, fmt.Errorf("发现事实记录损坏：%w", err)
		}
	}
	for _, field := range []struct {
		raw    sql.NullString
		target any
	}{{reason, &finding.Reason}, {overrides, &finding.UserOverrides},
		{breakdown, &finding.ScoreBreakdown}, {fairBasis, &finding.FairBasis},
		{keywords, &finding.MatchedKeywords}, {skipReason, &finding.NotifySkipReason}} {
		if field.raw.Valid {
			if err := decodeJSONNumber(field.raw.String, field.target); err != nil {
				return Finding{}, fmt.Errorf("发现记录损坏：%w", err)
			}
		}
	}
	if finding.UserOverrides.Risks == nil {
		finding.UserOverrides.Risks = map[string]analysis.RiskOverride{}
	}
	if finding.MatchedKeywords == nil {
		finding.MatchedKeywords = []string{}
	}
	if finding.Analysis != nil {
		facts := finding.Analysis
		if facts.Specs == nil {
			facts.Specs = []analysis.SpecValue{}
		}
		if facts.Musts == nil {
			facts.Musts = []analysis.RequirementFact{}
		}
		if facts.Bonuses == nil {
			facts.Bonuses = []analysis.RequirementFact{}
		}
		if facts.Checks == nil {
			facts.Checks = []analysis.CheckFact{}
		}
		if facts.SellerSignals == nil {
			facts.SellerSignals = []analysis.SellerSignal{}
		}
		if facts.ExtraFindings == nil {
			facts.ExtraFindings = []analysis.ExtraFinding{}
		}
	}
	if finding.ScoreBreakdown != nil {
		breakdown := finding.ScoreBreakdown
		if breakdown.Items == nil {
			breakdown.Items = []analysis.ScoreItem{}
		}
		if breakdown.Caps == nil {
			breakdown.Caps = []analysis.ScoreCap{}
		}
		if breakdown.Gates == nil {
			breakdown.Gates = []locales.Message{}
		}
	}
	return finding, nil
}

func loadFindingTx(ctx context.Context, tx *sql.Tx, findingID int64) (Finding, error) {
	finding, err := scanFinding(tx.QueryRowContext(ctx,
		`SELECT `+findingSelectColumns+` FROM findings f WHERE f.id=?`, findingID))
	if errors.Is(err, sql.ErrNoRows) {
		return Finding{}, findingMissingError()
	}
	return finding, err
}

func findingMissingError() error {
	return &ServiceError{Kind: ErrorNotFound, Reason: findingMessage("findings.not_found", nil)}
}

func findingChangedError() error {
	return &ServiceError{Kind: ErrorConflict, Reason: findingMessage("findings.changed", nil)}
}

func findingMessage(code string, args map[string]any) locales.Message {
	if args == nil {
		args = map[string]any{}
	}
	return locales.Message{Code: code, Args: args}
}

func validateRunTx(ctx context.Context, tx *sql.Tx, ref RunRef) (Task, error) {
	task, err := loadTaskTx(ctx, tx, ref.TaskID)
	if err != nil {
		return Task{}, err
	}
	if task.SearchGeneration != ref.SearchGeneration {
		return Task{}, findingChangedError()
	}
	return task, nil
}

func validateWorkTx(ctx context.Context, tx *sql.Tx, work WorkRef, expectedStage FindingStage) (Task, Finding, error) {
	task, err := validateRunTx(ctx, tx, work.RunRef)
	if err != nil {
		return Task{}, Finding{}, err
	}
	finding, err := loadFindingTx(ctx, tx, work.FindingID)
	if err != nil {
		return Task{}, Finding{}, err
	}
	if finding.TaskID != work.TaskID || finding.SearchGeneration != work.SearchGeneration ||
		finding.ProcessingRevision != work.ProcessingRevision || finding.Stage != expectedStage {
		return Task{}, Finding{}, findingChangedError()
	}
	return task, finding, nil
}

func loadBacklogTx(ctx context.Context, tx *sql.Tx, ref RunRef, nowMS int64) ([]Finding, error) {
	if _, err := validateRunTx(ctx, tx, ref); err != nil {
		return nil, err
	}
	const dayMS int64 = 24 * 60 * 60 * 1000
	reason, _ := json.Marshal(findingMessage("findings.backlog_expired", nil))
	_, err := tx.ExecContext(ctx, `UPDATE findings SET decision='skipped',stage='done',
			decided_layer=NULL,reject_kind=NULL,reason=?,retryable=0,reanalyze_requested_at=NULL
			WHERE task_id=? AND search_generation=? AND decision='pending'
			AND COALESCE(reanalyze_requested_at,found_at)<?
			AND COALESCE(json_extract(reason,'$.code'),'')<>'await_ai'`,
		string(reason), ref.TaskID, ref.SearchGeneration, nowMS-dayMS)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+findingSelectColumns+` FROM findings f
		WHERE f.task_id=? AND f.search_generation=?
		AND (f.decision='pending' OR (f.decision='failed' AND f.retryable=1 AND f.attempts<3))
		ORDER BY f.found_at,f.id`, ref.TaskID, ref.SearchGeneration)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Finding{}
	for rows.Next() {
		finding, err := scanFinding(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, finding)
	}
	return result, rows.Err()
}

func loadProcessingTx(ctx context.Context, tx *sql.Tx, ref RunRef, newIDs, qIDs []int64, limit int64) ([]WorkRef, error) {
	if _, err := validateRunTx(ctx, tx, ref); err != nil {
		return nil, err
	}
	if limit < 1 {
		return nil, &ServiceError{Kind: ErrorInvalid, Reason: findingMessage("findings.invalid_selection", nil)}
	}
	type candidate struct {
		finding   Finding
		published *int64
		group     int
		order     int
	}
	newSet, qOrder, ids := map[int64]bool{}, map[int64]int{}, []int64{}
	seen := map[int64]bool{}
	for _, id := range newIDs {
		newSet[id] = true
		if !seen[id] {
			ids, seen[id] = append(ids, id), true
		}
	}
	for order, id := range qIDs {
		if _, found := qOrder[id]; !found {
			qOrder[id] = order
		}
		if !seen[id] {
			ids, seen[id] = append(ids, id), true
		}
	}
	candidates := []candidate{}
	for order, id := range ids {
		finding, err := loadFindingTx(ctx, tx, id)
		if err != nil {
			var problem *ServiceError
			if errors.As(err, &problem) && problem.Kind == ErrorNotFound {
				continue
			}
			return nil, err
		}
		if finding.TaskID != ref.TaskID || finding.SearchGeneration != ref.SearchGeneration ||
			finding.Stage != StageDetail || (finding.Decision != DecisionPending &&
			(finding.Decision != DecisionFailed || !finding.Retryable || finding.Attempts >= 3)) {
			continue
		}
		var published *int64
		if err := tx.QueryRowContext(ctx, `SELECT published_at FROM listings WHERE item_id=?`, finding.ItemID).Scan(&published); err != nil {
			return nil, err
		}
		item := candidate{finding: finding, published: published, order: order}
		switch {
		case finding.ReanalyzeRequestedAt != nil:
			item.group = 0
		case !finding.IsBaseline && newSet[id]:
			item.group = 1
		case !finding.IsBaseline:
			item.group, item.order = 2, qOrder[id]
		default:
			item.group = 3
		}
		candidates = append(candidates, item)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.group != b.group {
			return a.group < b.group
		}
		if a.group == 0 && *a.finding.ReanalyzeRequestedAt != *b.finding.ReanalyzeRequestedAt {
			return *a.finding.ReanalyzeRequestedAt < *b.finding.ReanalyzeRequestedAt
		}
		if a.group == 1 || a.group == 3 {
			if a.published == nil || b.published == nil {
				if (a.published == nil) != (b.published == nil) {
					return a.published != nil
				}
			} else if *a.published != *b.published {
				return *a.published > *b.published
			}
		}
		return a.order < b.order
	})
	result := []WorkRef{}
	for _, item := range candidates {
		if int64(len(result)) >= limit {
			break
		}
		result = append(result, WorkRef{RunRef: ref, FindingID: item.finding.ID, ProcessingRevision: item.finding.ProcessingRevision})
	}
	return result, nil
}

func firstLayerInput(task Task, item Listing, nowMS int64, cities []analysis.RegionCity) analysis.FirstLayerInput {
	var days int64
	switch task.PublishWindow {
	case "day1":
		days = 1
	case "day3":
		days = 3
	case "day7":
		days = 7
	case "day14":
		days = 14
	}
	freeShipping := false
	for _, tag := range item.Tags {
		freeShipping = freeShipping || tag == "包邮"
	}
	return analysis.FirstLayerInput{
		Mode: task.JudgeMode, Title: item.Title, Price: item.Price, Area: findingString(item.Area),
		PublishedAt: item.PublishedAt, FreeShipping: freeShipping, MinPrice: task.MinPrice,
		MaxPrice: task.MaxPrice, FreeShippingOnly: task.FreeShippingOnly, PublishDays: days,
		Province: findingString(task.RegionProvince), City: findingString(task.RegionCity),
		Matches: task.MatchKeywords, Excludes: task.ExcludeKeywords, NowMS: nowMS, Cities: cities,
	}
}

func findingString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
