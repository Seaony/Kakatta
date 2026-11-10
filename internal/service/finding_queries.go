package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/limits"
)

const findingVisibleSQL = `l.is_dismissed=0 AND NOT EXISTS(SELECT 1 FROM blocked_sellers bs WHERE bs.seller_id=l.seller_id)`

// 列表只排除已知超预算；推荐资格还要确认有价格可核对。
const findingWithinBudgetSQL = `NOT(t.budget_max IS NOT NULL AND l.price IS NOT NULL AND l.price>t.budget_max)`
const findingRecommendationBudgetSQL = `(t.budget_max IS NULL OR (l.price IS NOT NULL AND l.price<=t.budget_max))`
const findingRecommendedSQL = `(` + findingVisibleSQL + `) AND (` + findingRecommendationBudgetSQL + `) AND (f.decision='matched' OR (f.decision='scored' AND (f.grade IN ('steal','good') OR (f.score IS NULL AND f.grade IS NULL))))`
const findingDefaultSQL = `((f.decision='matched' OR (f.decision='scored' AND (COALESCE(f.grade IN ('steal','good','fair'),0) OR (f.score IS NULL AND f.grade IS NULL)))) AND ` + findingWithinBudgetSQL + `)`
const findingScoreSQL = `CASE WHEN f.decision IN ('pending','failed') THEN NULL ELSE f.score END`
const findingFromSQL = ` FROM findings f JOIN listings l ON l.item_id=f.item_id JOIN tasks t ON t.id=f.task_id `

type findingCursor struct {
	ID      int64  `json:"id"`
	FoundAt int64  `json:"found_at"`
	Score   *int64 `json:"score"`
}

func normalizeFindingQuery(q FindingQuery) (FindingQuery, error) {
	var err error
	q.ListQuery, err = normalizeListQuery(q.ListQuery)
	if err != nil {
		return FindingQuery{}, err
	}
	if q.TaskID != nil && (*q.TaskID <= 0 || *q.TaskID > limits.MaxSafeInteger) {
		return FindingQuery{}, queryError()
	}
	if q.View == "" {
		q.View = "default"
	}
	switch q.View {
	case "default", "other", "all", "hidden":
	default:
		return FindingQuery{}, queryError()
	}
	if q.Sort == "" {
		q.Sort = "score"
	}
	if q.Sort != "score" && q.Sort != "latest" {
		return FindingQuery{}, queryError()
	}
	if q.Decision != nil {
		switch *q.Decision {
		case DecisionPending, DecisionFailed, DecisionSkipped, analysis.DecisionScored, analysis.DecisionMatched, analysis.DecisionRejected:
		default:
			return FindingQuery{}, queryError()
		}
	}
	if !utf8.ValidString(q.Q) {
		return FindingQuery{}, queryError()
	}
	q.Q = strings.TrimSpace(q.Q)
	return q, nil
}

func findingFilter(q FindingQuery) (string, []any) {
	where, args := []string{}, []any{}
	if q.View == "hidden" {
		where = append(where, `NOT (`+findingVisibleSQL+`)`)
	} else {
		where = append(where, findingVisibleSQL)
	}
	switch q.View {
	case "default":
		where = append(where, findingDefaultSQL)
	case "other":
		where = append(where, `f.decision IN ('scored','matched','rejected','skipped')`, `NOT `+findingDefaultSQL)
	}
	if q.TaskID != nil {
		where, args = append(where, "f.task_id=?"), append(args, *q.TaskID)
	}
	if q.Decision != nil {
		where, args = append(where, "f.decision=?"), append(args, string(*q.Decision))
	}
	if q.PriceDrop != nil {
		drop := `(l.last_drop_amount IS NOT NULL AND l.last_drop_amount>0 AND l.last_drop_at IS NOT NULL)`
		if !*q.PriceDrop {
			drop = "NOT " + drop
		}
		where = append(where, drop)
	}
	if q.OverBudget != nil {
		budget := findingWithinBudgetSQL
		if *q.OverBudget {
			budget = "NOT (" + budget + ")"
		}
		where = append(where, budget)
	}
	if q.Q != "" {
		where = append(where, `(instr(lower(l.title),lower(?))>0 OR instr(lower(COALESCE(l.description,'')),lower(?))>0)`)
		args = append(args, q.Q, q.Q)
	}
	return strings.Join(where, " AND "), args
}

func findingOrder(sort string, reverse bool) string {
	if reverse {
		if sort == "latest" {
			return "f.found_at ASC,f.id ASC"
		}
		return "(" + findingScoreSQL + " IS NULL) DESC," + findingScoreSQL + " ASC,f.found_at ASC,f.id ASC"
	}
	if sort == "latest" {
		return "f.found_at DESC,f.id DESC"
	}
	return "(" + findingScoreSQL + " IS NULL)," + findingScoreSQL + " DESC,f.found_at DESC,f.id DESC"
}

func findingAfter(anchor findingCursor, sort string, after bool) (string, []any) {
	operator := "<"
	if !after {
		operator = ">"
	}
	timeCondition := `(f.found_at` + operator + `? OR (f.found_at=? AND f.id` + operator + `?))`
	timeArgs := []any{anchor.FoundAt, anchor.FoundAt, anchor.ID}
	if sort == "latest" {
		return timeCondition, timeArgs
	}
	if anchor.Score == nil {
		if after {
			return `(` + findingScoreSQL + ` IS NULL AND ` + timeCondition + `)`, timeArgs
		}
		return `(` + findingScoreSQL + ` IS NOT NULL OR (` + findingScoreSQL + ` IS NULL AND ` + timeCondition + `))`, timeArgs
	}
	condition := `(` + findingScoreSQL + operator + `? OR (` + findingScoreSQL + `=? AND ` + timeCondition + `))`
	if after {
		condition = `(` + findingScoreSQL + ` IS NULL OR ` + condition + `)`
	}
	return condition, append([]any{*anchor.Score, *anchor.Score}, timeArgs...)
}

func (s *Service) ListFindings(ctx context.Context, q FindingQuery) (Page[FindingView], error) {
	page := Page[FindingView]{Items: []FindingView{}}
	q, err := normalizeFindingQuery(q)
	if err != nil {
		return page, err
	}
	binding := q
	binding.ListQuery = ListQuery{}
	var anchor findingCursor
	if err := decodeCursor(q.Cursor, "findings", binding, &anchor); err != nil {
		return page, err
	}
	if q.Cursor != "" && (anchor.ID <= 0 || anchor.Score != nil && (*anchor.Score < 0 || *anchor.Score > 100)) {
		return page, queryError()
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, storageFailure(err)
	}
	defer tx.Rollback()
	where, args := findingFilter(q)
	if err := tx.QueryRowContext(ctx, "SELECT count(*)"+findingFromSQL+"WHERE "+where, args...).Scan(&page.Total); err != nil {
		return page, storageFailure(err)
	}
	if q.Cursor != "" {
		condition, values := findingAfter(anchor, q.Sort, true)
		where += " AND " + condition
		args = append(args, values...)
	}
	args = append(args, q.Limit+1)
	rows, err := tx.QueryContext(ctx, `SELECT f.id,f.found_at,`+findingScoreSQL+findingFromSQL+`WHERE `+where+
		` ORDER BY `+findingOrder(q.Sort, false)+` LIMIT ?`, args...)
	if err != nil {
		return page, storageFailure(err)
	}
	anchors := []findingCursor{}
	for rows.Next() {
		var row findingCursor
		if err := rows.Scan(&row.ID, &row.FoundAt, &row.Score); err != nil {
			rows.Close()
			return page, storageFailure(err)
		}
		anchors = append(anchors, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, storageFailure(err)
	}
	if int64(len(anchors)) > q.Limit {
		anchors = anchors[:int(q.Limit)]
		token, err := encodeCursor("findings", binding, anchors[len(anchors)-1])
		if err != nil {
			return page, err
		}
		page.NextCursor = &token
	}
	for _, item := range anchors {
		view, err := getFindingViewTx(ctx, tx, item.ID)
		if err != nil {
			return Page[FindingView]{Items: []FindingView{}}, storageFailure(err)
		}
		page.Items = append(page.Items, view)
	}
	if err := tx.Commit(); err != nil {
		return Page[FindingView]{Items: []FindingView{}}, storageFailure(err)
	}
	return page, nil
}

func (s *Service) GetFinding(ctx context.Context, id int64, q FindingQuery) (FindingView, error) {
	if id <= 0 || q.Limit != 0 || q.Cursor != "" {
		return FindingView{}, queryError()
	}
	hasContext := q.TaskID != nil || q.View != "" || q.Sort != "" || q.Decision != nil || q.PriceDrop != nil || q.OverBudget != nil || strings.TrimSpace(q.Q) != ""
	q, err := normalizeFindingQuery(q)
	if err != nil {
		return FindingView{}, err
	}
	s.initRuntime()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return FindingView{}, storageFailure(err)
	}
	defer tx.Rollback()
	view, err := getFindingViewTx(ctx, tx, id)
	if err != nil {
		return FindingView{}, storageFailure(err)
	}
	if !hasContext {
		q.TaskID = &view.TaskID
	}
	view.MarketComparison, err = s.findingMarketComparisonTx(ctx, tx, view)
	if err != nil {
		return FindingView{}, storageFailure(err)
	}
	where, args := findingFilter(q)
	anchor := findingCursor{ID: id, FoundAt: view.FoundAt, Score: view.Score}
	for _, direction := range []struct {
		after bool
		id    **int64
	}{{false, &view.PreviousID}, {true, &view.NextID}} {
		condition, values := findingAfter(anchor, q.Sort, direction.after)
		var neighbor int64
		err := tx.QueryRowContext(ctx, "SELECT f.id"+findingFromSQL+"WHERE "+where+" AND "+condition+
			" ORDER BY "+findingOrder(q.Sort, !direction.after)+" LIMIT 1", append(append([]any{}, args...), values...)...).Scan(&neighbor)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return FindingView{}, storageFailure(err)
		}
		*direction.id = &neighbor
	}
	if err := tx.Commit(); err != nil {
		return FindingView{}, storageFailure(err)
	}
	return view, nil
}

func getFindingViewTx(ctx context.Context, tx *sql.Tx, id int64) (FindingView, error) {
	finding, err := loadFindingTx(ctx, tx, id)
	if err != nil {
		return FindingView{}, err
	}
	item, err := loadListingTx(ctx, tx, finding.ItemID)
	if err != nil {
		return FindingView{}, err
	}
	task, err := loadTaskTx(ctx, tx, finding.TaskID)
	if err != nil {
		return FindingView{}, err
	}
	view := FindingView{Finding: finding, Listing: item,
		Task:      TaskSummary{ID: task.ID, Name: task.Name, JudgeMode: task.JudgeMode, Profile: task.Profile, BudgetMin: task.BudgetMin, BudgetMax: task.BudgetMax},
		Questions: []analysis.ContactQuestion{}, Checks: []analysis.EffectiveCheck{}, Deliveries: []DeliverySummary{}}
	view.ItemSpecs, err = loadItemSpecsTx(ctx, tx, task.ID, item.ItemID)
	if err != nil {
		return FindingView{}, err
	}
	if item.SellerID != nil {
		view.Seller, err = loadSellerTx(ctx, tx, *item.SellerID)
		if err != nil {
			return FindingView{}, err
		}
	}
	sellerAssessment := sellerAssessmentForFinding(finding, item, view.Seller, task.Keyword)
	view.SellerAssessment = &sellerAssessment
	if finding.Analysis != nil {
		specs := analysis.ItemSpecs{Specs: []analysis.SpecValue{}}
		if view.ItemSpecs != nil {
			specs = view.ItemSpecs.ItemSpecs
		}
		result := analysis.Score(analysis.ScoreInput{Profile: task.Profile, Facts: *finding.Analysis, Item: specs,
			Price: item.Price, Seller: sellerAssessment, Overrides: finding.UserOverrides})
		view.Questions = result.Questions
		view.Checks = result.Checks
	}
	if finding.Decision == DecisionPending || finding.Decision == DecisionFailed {
		view.Score, view.Grade, view.ScoreBreakdown = nil, nil, nil
	}
	view.Deliveries, err = findingDeliveriesTx(ctx, tx, id)
	return view, err
}

// 当前行情只用于详情对比，不冒充上一次算分时的采样快照。
func (s *Service) findingMarketComparisonTx(ctx context.Context, tx *sql.Tx, view FindingView) (*FindingMarketComparison, error) {
	if view.ItemSpecs == nil {
		return nil, nil
	}
	task, err := loadTaskTx(ctx, tx, view.TaskID)
	if err != nil {
		return nil, err
	}
	item := analysis.ResolveSpecs(task.Profile, view.ItemSpecs.Specs, view.ItemSpecs.BrandNew, view.ItemSpecs.Source)
	if item.ConfigKey == nil {
		return nil, nil
	}
	if s.settings == nil {
		return nil, errors.New("详情行情缺少设置存储")
	}
	timezone, err := s.marketTimezoneTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	now, err := readObservedNowTx(ctx, tx, s.now())
	if err != nil {
		return nil, err
	}
	market, err := loadMarketTx(ctx, tx, task, now.UnixMilli(), timezone)
	if err != nil {
		return nil, err
	}
	fair := fairForItem(task.Profile, item, market)
	if fair == nil {
		return nil, nil
	}
	comparison := &FindingMarketComparison{AsOf: now.UnixMilli(), ConfigKey: *item.ConfigKey,
		BrandNew: item.BrandNew != nil && *item.BrandNew, Fair: *fair, AdjacentPrices: []analysis.AdjacentPrice{}}
	if view.Listing.Price != nil {
		comparison.AdjacentPrices = analysis.AdjacentPrices(task.Profile, item, *view.Listing.Price, market.Groups)
	}
	return comparison, nil
}

func findingDeliveriesTx(ctx context.Context, tx *sql.Tx, id int64) ([]DeliverySummary, error) {
	rows, err := tx.QueryContext(ctx, `SELECT d.id,d.at,d.channel_id,c.name,d.task_id,d.kind,d.item_count,d.ok,d.error
		FROM delivery_items di JOIN deliveries d ON d.id=di.delivery_id LEFT JOIN channels c ON c.id=d.channel_id
		WHERE di.finding_id=? ORDER BY (d.at IS NULL),d.at DESC,d.id DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[struct {
		known bool
		id    int64
	}]bool{}
	deliveries := []DeliverySummary{}
	for rows.Next() {
		var row DeliverySummary
		var failure sql.NullString
		if err := rows.Scan(&row.ID, &row.At, &row.ChannelID, &row.ChannelName, &row.TaskID, &row.Kind, &row.ItemCount, &row.OK, &failure); err != nil {
			return nil, err
		}
		key := struct {
			known bool
			id    int64
		}{known: row.ChannelID != nil}
		if row.ChannelID != nil {
			key.id = *row.ChannelID
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if failure.Valid {
			if err := json.Unmarshal([]byte(failure.String), &row.Error); err != nil {
				return nil, err
			}
		}
		deliveries = append(deliveries, row)
	}
	return deliveries, rows.Err()
}
