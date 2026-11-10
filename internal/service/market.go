package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
)

func loadMarketTx(ctx context.Context, tx *sql.Tx, task Task, nowMS int64, timezone string) (analysis.MarketResult, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil || timezone == "" || timezone == "Local" {
		return analysis.MarketResult{}, errors.New("行情时区无效")
	}
	firstDay := time.UnixMilli(nowMS).In(location).AddDate(0, 0, -29).Format(time.DateOnly)
	rows, err := tx.QueryContext(ctx, `SELECT s.item_id,s.day,s.price,s.last_seen_at,
		l.title,l.seller_id,l.is_dismissed,p.kind,p.specs_json,p.brand_new,p.excluded
		FROM task_sightings s JOIN listings l ON l.item_id=s.item_id
		LEFT JOIN task_item_specs p ON p.task_id=s.task_id AND p.item_id=s.item_id
		WHERE s.task_id=? AND s.day>=? ORDER BY s.day,s.item_id`, task.ID, firstDay)
	if err != nil {
		return analysis.MarketResult{}, err
	}
	observations := []analysis.MarketObservation{}
	for rows.Next() {
		var observation analysis.MarketObservation
		var seller, kind, specs sql.NullString
		var price, brandNew, excluded sql.NullInt64
		if err := rows.Scan(&observation.ItemID, &observation.Day, &price, &observation.LastSeenAt,
			&observation.Title, &seller, &observation.Dismissed, &kind, &specs, &brandNew, &excluded); err != nil {
			rows.Close()
			return analysis.MarketResult{}, err
		}
		if price.Valid {
			value := price.Int64
			observation.Price = &value
		}
		observation.SellerID = seller.String
		observation.Kind = analysis.Kind(kind.String)
		observation.Excluded = excluded.Valid && excluded.Int64 != 0
		observation.Specs = []analysis.SpecValue{}
		if specs.Valid && json.Unmarshal([]byte(specs.String), &observation.Specs) != nil {
			rows.Close()
			return analysis.MarketResult{}, errors.New("行情规格记录损坏")
		}
		if brandNew.Valid {
			value := brandNew.Int64 != 0
			observation.BrandNew = &value
		}
		observations = append(observations, observation)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return analysis.MarketResult{}, err
	}
	if err := rows.Close(); err != nil {
		return analysis.MarketResult{}, err
	}
	latest, err := loadLatestSearchItemIDsTx(ctx, tx, task.ID, task.SearchGeneration)
	if err != nil {
		return analysis.MarketResult{}, err
	}
	return analysis.CalculateMarket(analysis.MarketInput{
		Mode: task.JudgeMode, Profile: task.Profile, ExcludeKeywords: task.ExcludeKeywords,
		Observations: observations, LatestRunItemIDs: latest, NowMS: nowMS, TimeZone: timezone,
	}), nil
}

func (s *Service) marketTimezoneTx(ctx context.Context, tx *sql.Tx) (string, error) {
	snapshot, err := s.settings.SnapshotTx(ctx, tx)
	if err != nil {
		return "", err
	}
	var timezone string
	if err := json.Unmarshal(snapshot.Values["general.timezone"], &timezone); err != nil {
		return "", err
	}
	return timezone, nil
}

func (s *Service) GetMarket(ctx context.Context, taskID int64) (view MarketView, err error) {
	s.initRuntime()
	defer func() { err = storageFailure(err) }()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MarketView{}, err
	}
	defer tx.Rollback()
	task, err := loadTaskTx(ctx, tx, taskID)
	if err != nil {
		return MarketView{}, err
	}
	timezone, err := s.marketTimezoneTx(ctx, tx)
	if err != nil {
		return MarketView{}, err
	}
	now, err := readObservedNowTx(ctx, tx, s.now())
	if err != nil {
		return MarketView{}, err
	}
	market, err := loadMarketTx(ctx, tx, task, now.UnixMilli(), timezone)
	if err != nil {
		return MarketView{}, err
	}
	insight, err := loadMarketInsightTx(ctx, tx, taskID)
	if err != nil {
		return MarketView{}, err
	}
	if err := tx.Commit(); err != nil {
		return MarketView{}, err
	}
	return MarketView{Market: market, Insight: insight}, nil
}

func loadMarketInsightTx(ctx context.Context, tx *sql.Tx, taskID int64) (*MarketInsight, error) {
	var insight MarketInsight
	var rawStats sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id,task_id,generated_at,stats_json,text,provider_id,model
		FROM market_insights WHERE task_id=? ORDER BY generated_at DESC,id DESC LIMIT 1`, taskID).
		Scan(&insight.ID, &insight.TaskID, &insight.GeneratedAt, &rawStats, &insight.Text, &insight.ProviderID, &insight.Model)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if rawStats.Valid {
		if err := decodeJSONNumber(rawStats.String, &insight.Stats); err != nil {
			return nil, fmt.Errorf("行情解读快照损坏：%w", err)
		}
	}
	return &insight, nil
}

type marketSampleAnchor struct {
	Price  int64  `json:"price"`
	ItemID string `json:"item_id"`
}

func (s *Service) ListMarketSamples(ctx context.Context, taskID int64, q MarketSampleQuery) (page Page[MarketSampleView], err error) {
	s.initRuntime()
	defer func() { err = storageFailure(err) }()
	page = Page[MarketSampleView]{Items: []MarketSampleView{}}
	list, err := normalizeListQuery(q.ListQuery)
	if err != nil || taskID < 1 {
		return page, queryError()
	}
	q.ListQuery = list
	q.ConfigKey = strings.TrimSpace(q.ConfigKey)
	binding := q
	binding.ListQuery = ListQuery{}
	var anchor marketSampleAnchor
	scope := "market_samples:" + strconv.FormatInt(taskID, 10)
	if err := decodeCursor(q.Cursor, scope, binding, &anchor); err != nil {
		return page, err
	}
	if q.Cursor != "" && (anchor.ItemID == "" || anchor.Price < 0) {
		return page, queryError()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	task, err := loadTaskTx(ctx, tx, taskID)
	if err != nil {
		return page, err
	}
	timezone, err := s.marketTimezoneTx(ctx, tx)
	if err != nil {
		return page, err
	}
	now, err := readObservedNowTx(ctx, tx, s.now())
	if err != nil {
		return page, err
	}
	market, err := loadMarketTx(ctx, tx, task, now.UnixMilli(), timezone)
	if err != nil {
		return page, err
	}
	byItem := map[string]analysis.MarketSample{}
	for _, group := range market.Groups {
		if q.ConfigKey != "" && group.ConfigKey != q.ConfigKey || q.ConfigKey == "" && !group.Selected || q.BrandNew != nil && group.BrandNew != *q.BrandNew {
			continue
		}
		for _, sample := range group.Samples {
			byItem[sample.ItemID] = sample
		}
	}
	samples := make([]analysis.MarketSample, 0, len(byItem))
	for _, sample := range byItem {
		samples = append(samples, sample)
	}
	sort.Slice(samples, func(i, j int) bool {
		if samples[i].Price != samples[j].Price {
			return samples[i].Price < samples[j].Price
		}
		return samples[i].ItemID < samples[j].ItemID
	})
	page.Total = int64(len(samples))
	for _, sample := range samples {
		if q.Cursor != "" && (sample.Price < anchor.Price || sample.Price == anchor.Price && sample.ItemID <= anchor.ItemID) {
			continue
		}
		if int64(len(page.Items)) == q.Limit {
			last := page.Items[len(page.Items)-1]
			cursor, err := encodeCursor(scope, binding, marketSampleAnchor{Price: last.Price, ItemID: last.ItemID})
			if err != nil {
				return Page[MarketSampleView]{}, err
			}
			page.NextCursor = &cursor
			break
		}
		listing, err := loadListingTx(ctx, tx, sample.ItemID)
		if err != nil {
			return Page[MarketSampleView]{}, err
		}
		view := MarketSampleView{ItemID: sample.ItemID, Title: listing.Title, Price: sample.Price,
			SellerID: listing.SellerID, ObservedAt: sample.LastSeenAt, DesktopURL: listing.DesktopURL}
		if task.JudgeMode == analysis.ModeKeyword {
			view.ConfigKey = "*"
		} else {
			specs, err := loadItemSpecsTx(ctx, tx, task.ID, sample.ItemID)
			if err != nil {
				return Page[MarketSampleView]{}, err
			}
			if specs != nil {
				resolved := analysis.ResolveSpecs(task.Profile, specs.Specs, specs.BrandNew, specs.Source)
				if resolved.ConfigKey != nil {
					view.ConfigKey = *resolved.ConfigKey
				}
				view.BrandNew = specs.BrandNew != nil && *specs.BrandNew
			}
		}
		page.Items = append(page.Items, view)
	}
	if err := tx.Commit(); err != nil {
		return Page[MarketSampleView]{}, err
	}
	return page, nil
}
