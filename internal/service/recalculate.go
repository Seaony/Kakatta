package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
)

type findingScoreSource struct {
	finding Finding
	listing Listing
	item    TaskItemSpecs
	seller  analysis.SellerResult
}

func sellerResultForFinding(listing Listing, seller *SellerProfile, facts *analysis.Facts, keyword string) analysis.SellerResult {
	input := analysis.SellerInput{Available: seller != nil, RecentItems: []analysis.SellerItem{}, RecentRates: []analysis.SellerRate{}}
	if listing.SellerRegistrationDays != nil {
		days := int(*listing.SellerRegistrationDays)
		input.RegistrationDays = &days
	}
	if seller != nil {
		input.RatesAvailable = seller.RecentRates != nil
		for _, item := range seller.RecentItems {
			input.RecentItems = append(input.RecentItems, analysis.SellerItem{Title: item.Title, OnSale: item.SaleStatus != nil && *item.SaleStatus == "on_sale"})
		}
		for _, rate := range seller.RecentRates {
			input.RecentRates = append(input.RecentRates, analysis.SellerRate{
				AsSeller: rate.Role != nil && *rate.Role == "seller",
				Negative: rate.Kind != nil && (*rate.Kind == "neutral" || *rate.Kind == "bad"),
			})
		}
		// 信用等级的取值还待真实页面确认，不能用展示文字猜成极好。
	}
	var signals []analysis.SellerSignal
	if facts != nil {
		signals = facts.SellerSignals
	}
	return analysis.JudgeSeller(input, signals, keyword)
}

func shortSellerEvidence(value string) string {
	runes := []rune(value)
	if len(runes) > 120 {
		return string(runes[:120])
	}
	return value
}

func compactSellerAssessment(value analysis.SellerResult) analysis.SellerResult {
	out := value
	out.Checks = make([]analysis.EffectiveCheck, len(value.Checks))
	for index, check := range value.Checks {
		out.Checks[index] = check
		if check.Evidence != nil {
			text := shortSellerEvidence(*check.Evidence)
			out.Checks[index].Evidence = &text
		}
	}
	out.Reasons = make([]locales.Message, len(value.Reasons))
	for index, reason := range value.Reasons {
		args := make(map[string]any, len(reason.Args))
		for key, argument := range reason.Args {
			if text, ok := argument.(string); ok && key == "evidence" {
				argument = shortSellerEvidence(text)
			}
			args[key] = argument
		}
		out.Reasons[index] = locales.Message{Code: reason.Code, Args: args}
	}
	return out
}

func validateSellerAssessment(value analysis.SellerResult) error {
	if value.Type != analysis.SellerPersonal && value.Type != analysis.SellerDealerSuspect && value.Type != analysis.SellerUnknown || len(value.Checks) != 4 || value.Reasons == nil || value.BonusEligible && value.Type != analysis.SellerPersonal {
		return errors.New("卖家判断记录损坏")
	}
	seen := map[string]bool{}
	for _, check := range value.Checks {
		switch check.Key {
		case "seller_dealer", "seller_new", "seller_bad_reviews", "seller_partial_refund":
		default:
			return errors.New("卖家检查项编号无效")
		}
		if seen[check.Key] || check.Status != analysis.CheckOK && check.Status != analysis.CheckRisk && check.Status != analysis.CheckSuspect && check.Status != analysis.CheckUnknown {
			return errors.New("卖家检查项结论无效")
		}
		if check.Status == analysis.CheckUnknown {
			if check.Source != analysis.EvidenceUnknown || check.Evidence != nil {
				return errors.New("未知的卖家检查项带有确认依据")
			}
		} else if check.Source != analysis.EvidenceProgram {
			return errors.New("卖家判断的依据来源无效")
		}
		seen[check.Key] = true
	}
	for _, reason := range value.Reasons {
		if reason.Code == "" || reason.Args == nil {
			return errors.New("卖家判断的原因记录无效")
		}
	}
	return nil
}

func sellerAssessmentJSON(value analysis.SellerResult) (string, error) {
	if err := validateSellerAssessment(value); err != nil {
		return "", err
	}
	raw, err := json.Marshal(compactSellerAssessment(value))
	return string(raw), err
}

func decodeSellerAssessment(raw string) (*analysis.SellerResult, error) {
	if !utf8.ValidString(raw) || !bytes.HasPrefix(bytes.TrimSpace([]byte(raw)), []byte("{")) {
		return nil, errors.New("卖家判断记录不是有效对象")
	}
	var value analysis.SellerResult
	if err := logJSON(raw, &value); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, err
	}
	for _, key := range []string{"type", "checks", "bonus_eligible", "reasons"} {
		if len(fields[key]) == 0 || bytes.Equal(bytes.TrimSpace(fields[key]), []byte("null")) {
			return nil, errors.New("卖家判断记录缺少字段")
		}
	}
	if err := validateSellerAssessment(value); err != nil {
		return nil, err
	}
	return &value, nil
}

func sellerAssessmentForFinding(finding Finding, listing Listing, cached *SellerProfile, keyword string) analysis.SellerResult {
	if finding.SellerAssessment != nil {
		return *finding.SellerAssessment
	}
	if cached != nil {
		value := sellerResultForFinding(listing, cached, finding.Analysis, keyword)
		value.Reasons = append(value.Reasons, locales.Message{Code: "analysis.seller_legacy", Args: map[string]any{}})
		return compactSellerAssessment(value)
	}
	value := sellerResultForFinding(listing, nil, nil, keyword)
	if finding.SellerType != nil {
		value.Type = *finding.SellerType
	}
	setCheck := func(key string, status analysis.CheckStatus, evidence *string) {
		for index := range value.Checks {
			if value.Checks[index].Key == key {
				value.Checks[index].Status = status
				value.Checks[index].Source = analysis.EvidenceProgram
				value.Checks[index].Evidence = evidence
			}
		}
	}
	if value.Type == analysis.SellerPersonal {
		setCheck("seller_dealer", analysis.CheckOK, nil)
	} else if value.Type == analysis.SellerDealerSuspect {
		setCheck("seller_dealer", analysis.CheckRisk, nil)
	}
	if finding.Analysis != nil {
		for _, signal := range finding.Analysis.SellerSignals {
			text := shortSellerEvidence(signal.Evidence)
			key, reason := "", ""
			switch signal.Key {
			case "dealer_words":
				key, reason, value.Type = "seller_dealer", "analysis.seller_dealer_words", analysis.SellerDealerSuspect
			case "desc_mismatch", "after_sale", "slow_ship":
				key, reason = "seller_bad_reviews", "analysis.seller_bad_reviews"
			case "partial_refund":
				key, reason = "seller_partial_refund", "analysis.seller_partial_refund"
			}
			if key != "" && strings.TrimSpace(text) != "" {
				setCheck(key, analysis.CheckRisk, &text)
				value.Reasons = append(value.Reasons, locales.Message{Code: reason, Args: map[string]any{"evidence": text}})
			}
		}
	}
	if finding.ScoreBreakdown != nil {
		for _, item := range finding.ScoreBreakdown.Items {
			if item.ID == "seller_credit" && item.Value == 2 {
				if value.Type == analysis.SellerUnknown {
					value.Type = analysis.SellerPersonal
					setCheck("seller_dealer", analysis.CheckOK, nil)
				}
				value.BonusEligible = value.Type == analysis.SellerPersonal
				break
			}
		}
	}
	value.Reasons = append(value.Reasons, locales.Message{Code: "analysis.seller_legacy_partial", Args: map[string]any{}})
	return compactSellerAssessment(value)
}

func loadSellerAssessmentTx(ctx context.Context, tx *sql.Tx, task Task, finding Finding, listing Listing) (analysis.SellerResult, error) {
	if finding.SellerAssessment != nil {
		return *finding.SellerAssessment, nil
	}
	var cached *SellerProfile
	var err error
	if listing.SellerID != nil {
		cached, err = loadSellerTx(ctx, tx, *listing.SellerID)
		if err != nil {
			return analysis.SellerResult{}, err
		}
	}
	value := sellerAssessmentForFinding(finding, listing, cached, task.Keyword)
	raw, err := sellerAssessmentJSON(value)
	if err != nil {
		return analysis.SellerResult{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE findings SET seller_checks_json=? WHERE id=? AND task_id=?
		AND search_generation=? AND processing_revision=? AND seller_checks_json IS NULL`,
		raw, finding.ID, task.ID, finding.SearchGeneration, finding.ProcessingRevision)
	if err != nil {
		return analysis.SellerResult{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return analysis.SellerResult{}, err
	}
	if count != 1 {
		return analysis.SellerResult{}, findingChangedError()
	}
	return value, nil
}

func loadFindingScoreSourceTx(ctx context.Context, tx *sql.Tx, task Task, findingID int64) (findingScoreSource, error) {
	finding, err := loadFindingTx(ctx, tx, findingID)
	if err != nil {
		return findingScoreSource{}, err
	}
	if finding.TaskID != task.ID {
		return findingScoreSource{}, findingMissingError()
	}
	if finding.Analysis == nil {
		return findingScoreSource{}, errors.New("第三层事实记录缺失")
	}
	listing, err := loadListingTx(ctx, tx, finding.ItemID)
	if err != nil {
		return findingScoreSource{}, err
	}
	item, err := loadItemSpecsTx(ctx, tx, task.ID, finding.ItemID)
	if err != nil {
		return findingScoreSource{}, err
	}
	if item == nil {
		return findingScoreSource{}, errors.New("第三层商品规格记录缺失")
	}
	seller, err := loadSellerAssessmentTx(ctx, tx, task, finding, listing)
	if err != nil {
		return findingScoreSource{}, err
	}
	return findingScoreSource{finding: finding, listing: listing, item: *item,
		seller: seller}, nil
}

func (source findingScoreSource) input(profile analysis.Profile, fair *analysis.FairPrice) analysis.ScoreInput {
	item := source.item.ItemSpecs
	resolved := analysis.ResolveSpecs(profile, item.Specs, item.BrandNew, item.Source)
	resolved.Kind, resolved.Excluded = item.Kind, item.Excluded
	return analysis.ScoreInput{Profile: profile, Facts: *source.finding.Analysis, Item: resolved,
		Price: source.listing.Price, Fair: fair, Seller: source.seller, Overrides: source.finding.UserOverrides}
}

func recalculableFinding(finding Finding) bool {
	return finding.DecidedLayer != nil && *finding.DecidedLayer == 3 &&
		(finding.Decision == analysis.DecisionScored || finding.Decision == analysis.DecisionRejected && finding.RejectKind != nil && *finding.RejectKind == analysis.RejectUnmatched)
}

func loadRecalculableIDsTx(ctx context.Context, tx *sql.Tx, taskID int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM findings WHERE task_id=? AND decided_layer=3
		AND (decision='scored' OR (decision='rejected' AND reject_kind='unmatched')) ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func refreshTaskConfigKeysTx(ctx context.Context, tx *sql.Tx, task Task) error {
	rows, err := tx.QueryContext(ctx, `SELECT item_id FROM task_item_specs WHERE task_id=? ORDER BY item_id`, task.ID)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		item, err := loadItemSpecsTx(ctx, tx, task.ID, id)
		if err != nil {
			return err
		}
		if item == nil {
			return errors.New("行情商品规格记录缺失")
		}
		resolved := analysis.ResolveSpecs(task.Profile, item.Specs, item.BrandNew, item.Source)
		if err := updateItemSpecsDerivedTx(ctx, tx, task.ID, id, resolved.ConfigKey, nil); err != nil {
			return err
		}
	}
	return nil
}

func refreshFindingEligibilityTx(ctx context.Context, tx *sql.Tx, task Task, source findingScoreSource) error {
	resolved := analysis.ResolveSpecs(task.Profile, source.item.Specs, source.item.BrandNew, source.item.Source)
	excluded := analysis.Score(source.input(task.Profile, nil)).Excluded
	return updateItemSpecsDerivedTx(ctx, tx, task.ID, source.item.ItemID, resolved.ConfigKey, &excluded)
}

func (s *Service) recalculateTaskTx(ctx context.Context, tx *sql.Tx, task Task, nowMS int64) (int64, error) {
	if task.JudgeMode != analysis.ModeAI {
		return 0, nil
	}
	if err := refreshTaskConfigKeysTx(ctx, tx, task); err != nil {
		return 0, err
	}
	ids, err := loadRecalculableIDsTx(ctx, tx, task.ID)
	if err != nil {
		return 0, err
	}
	sources := make([]findingScoreSource, 0, len(ids))
	for _, id := range ids {
		source, err := loadFindingScoreSourceTx(ctx, tx, task, id)
		if err != nil {
			return 0, err
		}
		if err := refreshFindingEligibilityTx(ctx, tx, task, source); err != nil {
			return 0, err
		}
		sources = append(sources, source)
	}
	timezone, err := s.marketTimezoneTx(ctx, tx)
	if err != nil {
		return 0, err
	}
	market, err := loadMarketTx(ctx, tx, task, nowMS, timezone)
	if err != nil {
		return 0, err
	}
	for _, source := range sources {
		if err := saveRecalculatedFindingTx(ctx, tx, task, source, market, nowMS); err != nil {
			return 0, err
		}
	}
	return int64(len(sources)), nil
}

func (s *Service) recalculateFindingTx(ctx context.Context, tx *sql.Tx, task Task, findingID, nowMS int64) (Finding, error) {
	finding, err := loadFindingTx(ctx, tx, findingID)
	if err != nil {
		return Finding{}, err
	}
	if finding.TaskID != task.ID {
		return Finding{}, findingMissingError()
	}
	if task.JudgeMode != analysis.ModeAI || !recalculableFinding(finding) {
		return finding, nil
	}
	source, err := loadFindingScoreSourceTx(ctx, tx, task, findingID)
	if err != nil {
		return Finding{}, err
	}
	if err := refreshFindingEligibilityTx(ctx, tx, task, source); err != nil {
		return Finding{}, err
	}
	timezone, err := s.marketTimezoneTx(ctx, tx)
	if err != nil {
		return Finding{}, err
	}
	market, err := loadMarketTx(ctx, tx, task, nowMS, timezone)
	if err != nil {
		return Finding{}, err
	}
	if err := saveRecalculatedFindingTx(ctx, tx, task, source, market, nowMS); err != nil {
		return Finding{}, err
	}
	return loadFindingTx(ctx, tx, findingID)
}

func fairForItem(profile analysis.Profile, item analysis.ItemSpecs, market analysis.MarketResult) *analysis.FairPrice {
	resolved := analysis.ResolveSpecs(profile, item.Specs, item.BrandNew, item.Source)
	if resolved.ConfigKey == nil {
		return nil
	}
	brandNew := item.BrandNew != nil && *item.BrandNew
	for _, group := range market.Groups {
		if group.ConfigKey == *resolved.ConfigKey && group.BrandNew == brandNew {
			fair := group.Fair
			return &fair
		}
	}
	return nil
}

func saveRecalculatedFindingTx(ctx context.Context, tx *sql.Tx, task Task, source findingScoreSource, market analysis.MarketResult, nowMS int64) error {
	fair := fairForItem(task.Profile, source.item.ItemSpecs, market)
	result := analysis.Score(source.input(task.Profile, fair))
	reason, err := json.Marshal(result.Reason)
	if err != nil {
		return err
	}
	breakdown, err := json.Marshal(result.Breakdown)
	if err != nil {
		return err
	}
	var score, fairPrice, fairSample *int64
	var fairBasis *string
	var rejectKind *analysis.RejectKind
	if result.Score != nil {
		value := int64(*result.Score)
		score = &value
	}
	if result.RejectKind != "" {
		rejectKind = &result.RejectKind
	}
	if fair != nil {
		fairPrice = fair.Cents
		value := int64(fair.Sample)
		fairSample = &value
		encoded, err := json.Marshal(fair.Basis)
		if err != nil {
			return err
		}
		text := string(encoded)
		fairBasis = &text
	}
	scoredPrice := source.finding.ScoredPrice
	if source.finding.ScoredAt == nil {
		scoredPrice = source.listing.Price
	}
	updated, err := tx.ExecContext(ctx, `UPDATE findings SET decision=?,stage='done',reject_kind=?,decided_layer=3,
		reason=?,score=?,grade=?,score_breakdown_json=?,fair_price=?,fair_sample=?,fair_basis=?,
		scored_price=?,scored_at=?,seller_type=? WHERE id=? AND task_id=? AND search_generation=? AND processing_revision=?`,
		result.Decision, rejectKind, string(reason), score, result.Grade, string(breakdown), fairPrice, fairSample, fairBasis,
		scoredPrice, nowMS, source.seller.Type, source.finding.ID, task.ID, source.finding.SearchGeneration, source.finding.ProcessingRevision)
	if err != nil {
		return err
	}
	count, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return findingChangedError()
	}
	return nil
}

func loadNoGoodHintTx(ctx context.Context, tx *sql.Tx, task Task) (*NoGoodHint, error) {
	if task.JudgeMode != analysis.ModeAI {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM findings WHERE task_id=? AND is_baseline=0 AND decided_layer=3
		AND decision IN ('scored','rejected') AND analyzed_at IS NOT NULL ORDER BY analyzed_at DESC,id DESC LIMIT 30`, task.ID)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(ids) < 30 {
		return nil, nil
	}
	recent := make([]analysis.RecentScoredFinding, 0, 30)
	var latest int64
	for _, id := range ids {
		finding, err := loadFindingTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if finding.Grade != nil && (*finding.Grade == analysis.GradeGood || *finding.Grade == analysis.GradeSteal) {
			return nil, nil
		}
		result := analysis.ScoreResult{Decision: finding.Decision, Grade: finding.Grade}
		if finding.Reason != nil {
			result.Reason = *finding.Reason
		}
		if finding.ScoreBreakdown != nil {
			result.Breakdown = *finding.ScoreBreakdown
		}
		if finding.RejectKind != nil {
			result.RejectKind = *finding.RejectKind
		}
		if finding.AnalyzedAt != nil && *finding.AnalyzedAt > latest {
			latest = *finding.AnalyzedAt
		}
		recent = append(recent, analysis.RecentScoredFinding{AnalyzedAt: *finding.AnalyzedAt, DecidedLayer: 3, Result: result})
	}
	if task.NoGoodHintDismissedAt != nil && latest <= *task.NoGoodHintDismissedAt {
		return nil, nil
	}
	return &NoGoodHint{SampleCount: 30, Reasons: analysis.NoGoodReasons(recent), LatestAnalyzedAt: latest}, nil
}
