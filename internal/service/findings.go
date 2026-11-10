package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
)

const latestSearchPrefix = "task.latest_search."

type findingFactsRecord struct {
	analysis.Facts
	ParseWarnings []locales.Message `json:"parse_warnings"`
}

func validateAnalysisWarnings(warnings []locales.Message) error {
	for _, warning := range warnings {
		argument := "item"
		switch warning.Code {
		case "analysis.item_ignored", "analysis.evidence_missing":
		case "analysis.unknown_field":
			argument = "field"
		default:
			return errors.New("分析解析警告的代码无效")
		}
		value, ok := warning.Args[argument].(string)
		if !ok || len(warning.Args) != 1 || !utf8.ValidString(value) {
			return errors.New("分析解析警告的参数无效")
		}
		for _, language := range []locales.Language{locales.ZhCN, locales.En} {
			if _, err := locales.Format(language, warning); err != nil {
				return fmt.Errorf("分析解析警告无法显示：%w", err)
			}
		}
	}
	return nil
}

func encodeFindingFacts(facts analysis.Facts, warnings []locales.Message) (string, error) {
	if err := validateAnalysisWarnings(warnings); err != nil {
		return "", err
	}
	raw, err := json.Marshal(findingFactsRecord{Facts: facts, ParseWarnings: append([]locales.Message{}, warnings...)})
	return string(raw), err
}

func decodeFindingFacts(raw string) (*analysis.Facts, []locales.Message, error) {
	if !utf8.ValidString(raw) {
		return nil, nil, errors.New("发现事实记录不是有效文字")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, nil, err
	}
	if fields == nil {
		return nil, []locales.Message{}, nil
	}
	var facts analysis.Facts
	if err := json.Unmarshal([]byte(raw), &facts); err != nil {
		return nil, nil, err
	}
	warnings := []locales.Message{}
	if metadata, present := fields["parse_warnings"]; present {
		if !bytes.HasPrefix(bytes.TrimSpace(metadata), []byte("[")) {
			return nil, nil, errors.New("分析解析警告不是数组")
		}
		var entries []json.RawMessage
		if err := json.Unmarshal(metadata, &entries); err != nil {
			return nil, nil, err
		}
		for _, entry := range entries {
			if !bytes.HasPrefix(bytes.TrimSpace(entry), []byte("{")) {
				return nil, nil, errors.New("分析解析警告不是对象")
			}
			decoder := json.NewDecoder(bytes.NewReader(entry))
			decoder.DisallowUnknownFields()
			var warning locales.Message
			if err := decoder.Decode(&warning); err != nil {
				return nil, nil, err
			}
			warnings = append(warnings, warning)
		}
		if err := validateAnalysisWarnings(warnings); err != nil {
			return nil, nil, err
		}
	}
	return &facts, warnings, nil
}

type latestSearchSnapshot struct {
	RunID            int64    `json:"run_id"`
	SearchGeneration int64    `json:"search_generation"`
	ItemIDs          []string `json:"item_ids"`
}

func deleteTaskSearchTx(ctx context.Context, tx *sql.Tx, taskID int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key=?`, latestSearchPrefix+strconv.FormatInt(taskID, 10))
	return err
}

func loadLatestSearchItemIDsTx(ctx context.Context, tx *sql.Tx, taskID, generation int64) ([]string, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, latestSearchPrefix+strconv.FormatInt(taskID, 10)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot latestSearchSnapshot
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("最近搜索记录损坏：%w", err)
	}
	if decoder.Decode(new(any)) != io.EOF || snapshot.SearchGeneration < 1 || snapshot.ItemIDs == nil {
		return nil, errors.New("最近搜索记录损坏")
	}
	seen := map[string]bool{}
	for _, id := range snapshot.ItemIDs {
		if id == "" || seen[id] {
			return nil, errors.New("最近搜索记录损坏")
		}
		seen[id] = true
	}
	if snapshot.SearchGeneration != generation {
		return []string{}, nil
	}
	return snapshot.ItemIDs, nil
}

func (s *Service) persistSearchTx(ctx context.Context, tx *sql.Tx, ref RunRef, observations []SearchObservation, nowMS int64) (SearchSaved, error) {
	result := SearchSaved{NewItemIDs: []string{}, NewFindingIDs: []int64{}, TitleOnlyFindingIDs: []int64{}, PriceChanges: []PriceChange{}}
	task, err := validateRunTx(ctx, tx, ref)
	if err != nil {
		return result, err
	}
	snapshot, err := s.settings.SnapshotTx(ctx, tx)
	if err != nil {
		return result, err
	}
	var timezone string
	if err := json.Unmarshal(snapshot.Values["general.timezone"], &timezone); err != nil {
		return result, err
	}
	type newFinding struct {
		item      Listing
		decision  analysis.Decision
		stage     FindingStage
		reject    *analysis.RejectKind
		layer     *int64
		reason    *locales.Message
		candidate bool
		junk      analysis.Kind
	}
	allIDs := []string{}
	newFindings := []newFinding{}
	seen := map[string]bool{}
	cities := regionCities()
	for _, observation := range observations {
		if seen[observation.ItemID] {
			continue
		}
		seen[observation.ItemID] = true
		item, change, err := upsertSearchListingTx(ctx, tx, observation)
		if err != nil {
			return result, err
		}
		allIDs = append(allIDs, item.ItemID)
		result.ScannedCount++
		if change != nil {
			result.PriceChanges = append(result.PriceChanges, *change)
		}
		if err := recordSightingTx(ctx, tx, task.ID, item.ItemID, observation.Price, observation.ObservedAt, timezone); err != nil {
			return result, err
		}
		insert, err := tx.ExecContext(ctx, `INSERT INTO seen_items(task_id,item_id,first_seen_at) VALUES(?,?,?)
			ON CONFLICT(task_id,item_id) DO NOTHING`, task.ID, item.ItemID, nowMS)
		if err != nil {
			return result, err
		}
		count, err := insert.RowsAffected()
		if err != nil {
			return result, err
		}
		if count == 0 {
			continue
		}
		result.NewCount++
		result.NewItemIDs = append(result.NewItemIDs, item.ItemID)
		finding := newFinding{item: item, decision: DecisionPending, stage: StageDetail, candidate: true}
		if task.JudgeMode == analysis.ModeAI {
			finding.stage = StageTitle
		}
		blocked := false
		if item.SellerID != nil {
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM blocked_sellers WHERE seller_id=?)`, *item.SellerID).Scan(&blocked); err != nil {
				return result, err
			}
		}
		input := firstLayerInput(task, item, nowMS, cities)
		input.FreeShipping = observation.FreeShipping
		layer := analysis.FirstLayer(input)
		switch {
		case item.IsDismissed:
			finding.decision, finding.stage, finding.candidate = DecisionSkipped, StageDone, false
			message := findingMessage("findings.dismissed", nil)
			finding.reason = &message
		case blocked:
			finding.decision, finding.stage, finding.candidate = DecisionSkipped, StageDone, false
			message := findingMessage("findings.blocked_seller", nil)
			finding.reason = &message
		case layer.Rejected:
			finding.decision, finding.stage, finding.candidate = analysis.DecisionRejected, StageDone, false
			finding.reject, finding.reason, finding.layer = &layer.RejectKind, layer.Reason, findingInt64(1)
			result.Layer1Rejected++
		case task.JudgeMode == analysis.ModeKeyword && analysis.KeywordKind(item.Title) != "":
			finding.decision, finding.stage, finding.candidate = analysis.DecisionRejected, StageDone, false
			finding.junk = analysis.KeywordKind(item.Title)
			kind := analysis.RejectJunk
			finding.reject, finding.layer = &kind, findingInt64(2)
			message := findingMessage("analysis.junk."+string(finding.junk), nil)
			finding.reason = &message
		}
		newFindings = append(newFindings, finding)
	}
	selected := map[int]bool{}
	if task.BaselinePending && task.AnalyzeOnFirstRun {
		candidateIDs := []int{}
		for index, finding := range newFindings {
			if finding.candidate {
				candidateIDs = append(candidateIDs, index)
			}
		}
		sort.SliceStable(candidateIDs, func(i, j int) bool {
			a, b := newFindings[candidateIDs[i]].item.PublishedAt, newFindings[candidateIDs[j]].item.PublishedAt
			if a == nil || b == nil {
				return a != nil && b == nil
			}
			return *a > *b
		})
		for _, index := range candidateIDs[:min(10, len(candidateIDs))] {
			selected[index] = true
		}
	}
	for index, finding := range newFindings {
		if task.BaselinePending && finding.candidate && !selected[index] {
			finding.decision, finding.stage = DecisionSkipped, StageDone
			message := findingMessage("findings.baseline", nil)
			finding.reason = &message
		}
		var reason any
		if finding.reason != nil {
			raw, err := json.Marshal(finding.reason)
			if err != nil {
				return result, err
			}
			reason = string(raw)
		}
		insert, err := tx.ExecContext(ctx, `INSERT INTO findings(task_id,item_id,search_generation,
			processing_revision,found_at,is_baseline,decision,stage,reject_kind,decided_layer,reason,is_read)
			VALUES(?,?,?,1,?,?,?,?,?,?,?,?)`, task.ID, finding.item.ItemID, ref.SearchGeneration,
			nowMS, task.BaselinePending, finding.decision, finding.stage, finding.reject, finding.layer,
			reason, task.BaselinePending)
		if err != nil {
			return result, err
		}
		id, err := insert.LastInsertId()
		if err != nil {
			return result, err
		}
		result.NewFindingIDs = append(result.NewFindingIDs, id)
		if task.JudgeMode == analysis.ModeAI && finding.stage == StageDone &&
			((finding.reason != nil && finding.reason.Code == "findings.baseline") ||
				(finding.decision == analysis.DecisionRejected && finding.reject != nil && *finding.reject != analysis.RejectExcluded)) {
			result.TitleOnlyFindingIDs = append(result.TitleOnlyFindingIDs, id)
		}
		if finding.junk != "" {
			specs := analysis.ResolveSpecs(task.Profile, nil, nil, analysis.SpecTitle)
			specs.Kind = finding.junk
			if _, err := saveItemSpecsTx(ctx, tx, task.ID, finding.item.ItemID, specs, nowMS); err != nil {
				return result, err
			}
		}
	}
	raw, err := json.Marshal(latestSearchSnapshot{RunID: ref.RunID, SearchGeneration: ref.SearchGeneration, ItemIDs: allIDs})
	if err != nil {
		return result, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		latestSearchPrefix+strconv.FormatInt(task.ID, 10), string(raw))
	return result, err
}

func findingInt64(value int64) *int64 { return &value }

func titleForCurrentProfile(request, current analysis.Profile, item analysis.TitleItem) analysis.TitleItem {
	// 旧清单的“不符合”不是新清单的事实；规格和商品类型仍可按新条件重判。
	if item.Verdict == "unmatched" && !sameTitleRules(request, current) {
		item.Verdict, item.Reason = "", ""
	}
	return item
}

func sameTitleRules(a, b analysis.Profile) bool {
	type spec struct {
		Name            string
		Options, Values []string
	}
	type check struct {
		ID, Name string
		Rev      int64
	}
	type rules struct {
		Specs   []spec
		Musts   []string
		Checks  []check
		Allowed []analysis.Kind
	}
	project := func(profile analysis.Profile) rules {
		result := rules{Specs: []spec{}, Musts: []string{}, Checks: []check{}, Allowed: append([]analysis.Kind{}, profile.AllowedKinds...)}
		for _, item := range profile.Specs {
			options, values := append([]string{}, item.Options...), append([]string{}, item.Values...)
			sort.Strings(options)
			sort.Strings(values)
			result.Specs = append(result.Specs, spec{item.Name, options, values})
		}
		for _, item := range profile.Musts {
			result.Musts = append(result.Musts, item.Text)
		}
		for _, item := range analysis.ResolveRisks(profile) {
			if item.Attitude == analysis.Confirm || item.Attitude == analysis.Reject {
				result.Checks = append(result.Checks, check{item.ID, item.Name, item.Rev})
			}
		}
		sort.Slice(result.Specs, func(i, j int) bool { return result.Specs[i].Name < result.Specs[j].Name })
		sort.Strings(result.Musts)
		sort.Slice(result.Checks, func(i, j int) bool { return result.Checks[i].ID < result.Checks[j].ID })
		sort.Slice(result.Allowed, func(i, j int) bool { return result.Allowed[i] < result.Allowed[j] })
		return result
	}
	return reflect.DeepEqual(project(a), project(b))
}

func saveTitleOnlyTx(ctx context.Context, tx *sql.Tx, work WorkRef, requestProfile analysis.Profile, result analysis.TitleResult, nowMS int64) error {
	task, finding, err := validateWorkTx(ctx, tx, work, StageDone)
	if err != nil {
		return err
	}
	if task.JudgeMode != analysis.ModeAI {
		return findingChangedError()
	}
	baseline := finding.Decision == DecisionSkipped && finding.Reason != nil && finding.Reason.Code == "findings.baseline"
	filtered := finding.Decision == analysis.DecisionRejected && finding.RejectKind != nil &&
		(*finding.RejectKind == analysis.RejectFilter || *finding.RejectKind == analysis.RejectKeywordMiss)
	if !baseline && !filtered {
		return findingChangedError()
	}
	judged := analysis.JudgeTitle(task.Profile, titleForCurrentProfile(requestProfile, task.Profile, result.Item))
	_, err = saveItemSpecsTx(ctx, tx, task.ID, finding.ItemID, judged.Specs, nowMS)
	return err
}

func saveTitleTx(ctx context.Context, tx *sql.Tx, work WorkRef, requestProfile analysis.Profile, result analysis.TitleResult, nowMS int64) (bool, error) {
	task, finding, err := validateWorkTx(ctx, tx, work, StageTitle)
	if err != nil {
		return false, err
	}
	if task.JudgeMode != analysis.ModeAI || (finding.Decision != DecisionPending && finding.Decision != DecisionFailed) {
		return false, findingChangedError()
	}
	if finding.Attempts == math.MaxInt64 {
		return false, errors.New("发现处理次数溢出")
	}
	item := titleForCurrentProfile(requestProfile, task.Profile, result.Item)
	judged := analysis.JudgeTitle(task.Profile, item)
	actual, err := saveItemSpecsTx(ctx, tx, task.ID, finding.ItemID, judged.Specs, nowMS)
	if err != nil {
		return false, err
	}
	if actual.Source == analysis.SpecUser {
		item.Kind, item.Specs, item.BrandNew = actual.Kind, actual.Specs, actual.BrandNew
		judged = analysis.JudgeTitle(task.Profile, item)
	}
	decision, stage := DecisionPending, StageDetail
	var reject, layer, reason any
	if judged.Result.Rejected {
		decision, stage, reject, layer = analysis.DecisionRejected, StageDone, judged.Result.RejectKind, int64(2)
		raw, err := json.Marshal(judged.Result.Reason)
		if err != nil {
			return false, err
		}
		reason = string(raw)
	}
	updated, err := tx.ExecContext(ctx, `UPDATE findings SET decision=?,stage=?,reject_kind=?,decided_layer=?,reason=?,
		title_screen_reason=?,attempts=attempts+1,retryable=0,
		reanalyze_requested_at=CASE WHEN ?='done' THEN NULL ELSE reanalyze_requested_at END
		WHERE id=? AND task_id=? AND search_generation=? AND processing_revision=? AND stage='title'`,
		decision, stage, reject, layer, reason, judged.Item.Reason, stage, finding.ID, task.ID, work.SearchGeneration, work.ProcessingRevision)
	return judged.Result.Rejected, findingUpdateError(updated, err)
}

func saveFindingFailureTx(ctx context.Context, tx *sql.Tx, work WorkRef, stage FindingStage, reason locales.Message, retryable, awaitAI bool, nowMS int64) error {
	if stage != StageTitle && stage != StageDetail {
		return findingChangedError()
	}
	_, finding, err := validateWorkTx(ctx, tx, work, stage)
	if err != nil {
		return err
	}
	if finding.Decision != DecisionPending && finding.Decision != DecisionFailed {
		return findingChangedError()
	}
	decision, attempts := DecisionFailed, finding.Attempts
	if awaitAI {
		if reason.Code != "await_ai" {
			return errors.New("等待分析必须保存明确原因")
		}
		decision, retryable = DecisionPending, false
	} else {
		if attempts == math.MaxInt64 {
			return errors.New("发现处理次数溢出")
		}
		attempts++
		retryable = retryable && attempts < 3
	}
	raw, err := json.Marshal(reason)
	if err != nil {
		return err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE findings SET decision=?,reason=?,attempts=?,retryable=?,
		reject_kind=NULL,decided_layer=NULL,reanalyze_requested_at=NULL
		WHERE id=? AND task_id=? AND search_generation=? AND processing_revision=? AND stage=?`,
		decision, string(raw), attempts, retryable, finding.ID, finding.TaskID,
		work.SearchGeneration, work.ProcessingRevision, stage)
	return findingUpdateError(updated, err)
}

func (s *Service) saveConclusionTx(ctx context.Context, tx *sql.Tx, work WorkRef, conclusion Conclusion, nowMS int64) (bool, error) {
	task, finding, err := validateWorkTx(ctx, tx, work, StageDetail)
	if err != nil {
		return false, err
	}
	if finding.Decision != DecisionPending && finding.Decision != DecisionFailed {
		return false, findingChangedError()
	}
	choices := 0
	for _, present := range []bool{conclusion.Facts != nil, conclusion.Layer != nil, conclusion.Skipped != nil} {
		if present {
			choices++
		}
	}
	if choices != 1 {
		return false, errors.New("发现结论必须有且只有一种结果")
	}
	if finding.Attempts == math.MaxInt64 {
		return false, errors.New("发现处理次数溢出")
	}
	item, err := loadListingTx(ctx, tx, finding.ItemID)
	if err != nil {
		return false, err
	}
	blocked := false
	if item.SellerID != nil {
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM blocked_sellers WHERE seller_id=?)`, *item.SellerID).Scan(&blocked); err != nil {
			return false, err
		}
	}
	if item.IsDismissed || blocked {
		code := "findings.blocked_seller"
		if item.IsDismissed {
			code = "findings.dismissed"
		}
		reason := findingMessage(code, nil)
		conclusion = Conclusion{Skipped: &reason}
	} else if conclusion.Skipped != nil {
		return false, findingChangedError()
	} else if conclusion.Facts != nil && len(analysis.MatchingKeywords(item.Title+"\n"+findingString(item.Description), task.ExcludeKeywords)) > 0 {
		layer := analysis.JudgeKeyword(item.Title, findingString(item.Description), nil, task.ExcludeKeywords)
		conclusion = Conclusion{Layer: &layer}
	}
	if conclusion.Facts != nil {
		if task.JudgeMode != analysis.ModeAI {
			return false, findingChangedError()
		}
		facts := conclusion.Facts
		raw, err := encodeFindingFacts(*facts, conclusion.Warnings)
		if err != nil {
			return false, err
		}
		var seller *SellerProfile
		if item.SellerID != nil {
			seller, err = loadSellerTx(ctx, tx, *item.SellerID)
			if err != nil {
				return false, err
			}
		}
		assessment := sellerResultForFinding(item, seller, facts, task.Keyword)
		sellerJSON, err := sellerAssessmentJSON(assessment)
		if err != nil {
			return false, err
		}
		specs := analysis.ResolveSpecs(task.Profile, facts.Specs, facts.BrandNew, analysis.SpecDetail)
		previous, err := loadItemSpecsTx(ctx, tx, task.ID, finding.ItemID)
		if err != nil {
			return false, err
		}
		specs.Kind = analysis.KindSale
		if previous != nil {
			specs.Kind = previous.Kind
		}
		if _, err := saveItemSpecsTx(ctx, tx, task.ID, finding.ItemID, specs, nowMS); err != nil {
			return false, err
		}
		updated, err := tx.ExecContext(ctx, `UPDATE findings SET decision='scored',stage='done',decided_layer=3,
			reject_kind=NULL,analysis_json=?,seller_checks_json=?,summary=?,analysis_provider_id=?,analysis_model=?,analyzed_at=?,
			attempts=attempts+1,retryable=0,reanalyze_requested_at=NULL
			WHERE id=? AND task_id=? AND search_generation=? AND processing_revision=? AND stage='detail'`,
			raw, sellerJSON, facts.Summary, conclusion.ProviderID, conclusion.Model, nowMS,
			finding.ID, task.ID, work.SearchGeneration, work.ProcessingRevision)
		if err := findingUpdateError(updated, err); err != nil {
			return false, err
		}
		_, err = s.recalculateFindingTx(ctx, tx, task, finding.ID, nowMS)
		return true, err
	}
	decision, layer := analysis.DecisionMatched, any(int64(1))
	var reject, reason any
	matched := []string{}
	if conclusion.Skipped != nil {
		decision, layer = DecisionSkipped, nil
		raw, err := json.Marshal(conclusion.Skipped)
		if err != nil {
			return false, err
		}
		reason = string(raw)
	} else {
		result := *conclusion.Layer
		if task.JudgeMode == analysis.ModeKeyword {
			result = analysis.JudgeKeyword(item.Title, findingString(item.Description), task.MatchKeywords, task.ExcludeKeywords)
		} else {
			hits := analysis.MatchingKeywords(item.Title+"\n"+findingString(item.Description), task.ExcludeKeywords)
			if !result.Rejected || result.RejectKind != analysis.RejectExcluded || len(hits) == 0 {
				return false, findingChangedError()
			}
			result = analysis.JudgeKeyword(item.Title, findingString(item.Description), nil, task.ExcludeKeywords)
		}
		matched = result.MatchedKeywords
		if result.Rejected {
			decision, reject = analysis.DecisionRejected, result.RejectKind
		}
		if result.Reason != nil {
			raw, err := json.Marshal(result.Reason)
			if err != nil {
				return false, err
			}
			reason = string(raw)
		}
	}
	rawKeywords, err := json.Marshal(matched)
	if err != nil {
		return false, err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE findings SET decision=?,stage='done',decided_layer=?,reject_kind=?,
		reason=?,matched_keywords_json=?,score=NULL,grade=NULL,score_breakdown_json=NULL,
		attempts=attempts+1,retryable=0,reanalyze_requested_at=NULL
		WHERE id=? AND task_id=? AND search_generation=? AND processing_revision=? AND stage='detail'`,
		decision, layer, reject, reason, string(rawKeywords), finding.ID, task.ID, work.SearchGeneration, work.ProcessingRevision)
	return decision != DecisionSkipped, findingUpdateError(updated, err)
}

func findingUpdateError(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return findingChangedError()
	}
	return nil
}
