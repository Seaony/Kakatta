package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"
)

type seedCounts struct{ tasks, listings, findings int }

func jsonColumn(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if string(encoded) == "null" {
		return nil, nil
	}
	return string(encoded), nil
}

func populate(ctx context.Context, db *sql.DB, now time.Time, timezone string) (seedCounts, error) {
	data, err := makeSamples(now, timezone)
	if err != nil {
		return seedCounts{}, err
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return seedCounts{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return seedCounts{}, err
	}
	defer tx.Rollback()
	for _, task := range data.tasks {
		jsonValues := []any{task.CrawlOverrides, task.Profile, task.MatchKeywords, task.ExcludeKeywords}
		for index, value := range jsonValues {
			jsonValues[index], err = jsonColumn(value)
			if err != nil {
				return seedCounts{}, err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO tasks(id,name,keyword,enabled,personal_only,publish_window,
			max_pages,interval_minutes,crawl_overrides_json,judge_mode,requirement,profile_json,budget_max,
			match_keywords_json,exclude_keywords_json,analyze_on_first_run,notify_policy,notify_price_drop,
			search_generation,baseline_pending,sort_index,created_at,updated_at,last_run_at,last_success_at,last_run_outcome)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			task.ID, task.Name, task.Keyword, task.Enabled, task.PersonalOnly, task.PublishWindow,
			task.MaxPages, task.IntervalMinutes, jsonValues[0], task.JudgeMode, task.Requirement, jsonValues[1], task.BudgetMax,
			jsonValues[2], jsonValues[3], task.AnalyzeOnFirstRun, task.NotifyPolicy, task.NotifyPriceDrop,
			task.SearchGeneration, task.BaselinePending, task.SortIndex, task.CreatedAt, task.UpdatedAt, task.LastRunAt, task.LastSuccessAt, task.LastRunOutcome)
		if err != nil {
			return seedCounts{}, err
		}
	}
	oldest := now.UnixMilli()
	for _, listing := range data.listings {
		images, err := jsonColumn(listing.ImageURLs)
		if err != nil {
			return seedCounts{}, err
		}
		tags, err := jsonColumn(listing.Tags)
		if err != nil {
			return seedCounts{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO listings(item_id,title,price,price_text,desktop_url,first_seen_at,last_seen_at,
			image_urls_json,tags_json,area,description,detail_fetched_at,is_favorite,favorited_at,is_dismissed,last_drop_amount,last_drop_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, listing.ItemID, listing.Title, listing.Price, listing.PriceText, listing.DesktopURL,
			listing.FirstSeenAt, listing.LastSeenAt, images, tags, listing.Area, listing.Description, listing.DetailFetchedAt,
			listing.IsFavorite, listing.FavoritedAt, listing.IsDismissed, listing.LastDropAmount, listing.LastDropAt)
		if err != nil {
			return seedCounts{}, err
		}
		oldest = min(oldest, listing.FirstSeenAt)
	}
	for _, point := range data.prices {
		if _, err := tx.ExecContext(ctx, `INSERT INTO price_points(item_id,price,observed_at) VALUES(?,?,?)`, point.ItemID, point.Price, point.ObservedAt); err != nil {
			return seedCounts{}, err
		}
	}
	for _, finding := range data.findings {
		values := []any{finding.Reason, finding.Analysis, finding.UserOverrides, finding.ScoreBreakdown, finding.FairBasis,
			finding.MatchedKeywords, finding.NotifySkipReason, finding.SellerAssessment}
		for index, value := range values {
			values[index], err = jsonColumn(value)
			if err != nil {
				return seedCounts{}, err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO findings(id,task_id,item_id,search_generation,found_at,is_baseline,decision,stage,
			reject_kind,decided_layer,reason,analysis_json,user_overrides_json,score,grade,score_breakdown_json,fair_price,fair_sample,
			fair_basis,scored_price,scored_at,summary,seller_type,matched_keywords_json,analyzed_at,attempts,retryable,is_read,notify_skip_reason,seller_checks_json)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			finding.ID, finding.TaskID, finding.ItemID, finding.SearchGeneration, finding.FoundAt, finding.IsBaseline, finding.Decision, finding.Stage,
			finding.RejectKind, finding.DecidedLayer, values[0], values[1], values[2], finding.Score, finding.Grade, values[3], finding.FairPrice, finding.FairSample,
			values[4], finding.ScoredPrice, finding.ScoredAt, finding.Summary, finding.SellerType, values[5], finding.AnalyzedAt, finding.Attempts, finding.Retryable, finding.IsRead, values[6], values[7])
		if err != nil {
			return seedCounts{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO seen_items(task_id,item_id,first_seen_at) VALUES(?,?,?)`, finding.TaskID, finding.ItemID, finding.FoundAt); err != nil {
			return seedCounts{}, err
		}
		listing := data.listings[finding.ID-1]
		day := time.UnixMilli(listing.LastSeenAt).In(location).Format(time.DateOnly)
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_sightings(task_id,item_id,day,price,last_seen_at) VALUES(?,?,?,?,?)`, finding.TaskID, finding.ItemID, day, listing.Price, listing.LastSeenAt); err != nil {
			return seedCounts{}, err
		}
		if listing.LastDropAt != nil {
			day := time.UnixMilli(listing.FirstSeenAt).In(location).Format(time.DateOnly)
			if _, err := tx.ExecContext(ctx, `INSERT INTO task_sightings(task_id,item_id,day,price,last_seen_at) VALUES(?,?,?,?,?)`, finding.TaskID, finding.ItemID, day, 310000, listing.FirstSeenAt); err != nil {
				return seedCounts{}, err
			}
		}
	}
	for _, item := range data.specs {
		specs, err := jsonColumn(item.Specs)
		if err != nil {
			return seedCounts{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_item_specs(task_id,item_id,kind,specs_json,brand_new,config_key,excluded,source,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?)`, item.TaskID, item.ItemID, item.Kind, specs, item.BrandNew, item.ConfigKey, item.Excluded, item.Source, item.UpdatedAt); err != nil {
			return seedCounts{}, err
		}
	}
	for _, run := range data.runs {
		message, err := jsonColumn(run.Message)
		if err != nil {
			return seedCounts{}, err
		}
		warnings, err := jsonColumn(run.Warnings)
		if err != nil {
			return seedCounts{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO run_records(id,task_id,task_name,trigger,started_at,ended_at,outcome,message,repeat_count,
			page_count,scanned_count,new_count,layer1_rejected,layer2_count,layer2_rejected,detail_count,seller_page_count,analyzed_count,recommended_count,notified_count,warnings_json)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, run.ID, run.TaskID, run.TaskName, run.Trigger, run.StartedAt, run.EndedAt, run.Outcome, message, run.RepeatCount,
			run.PageCount, run.ScannedCount, run.NewCount, run.Layer1Rejected, run.Layer2Count, run.Layer2Rejected, run.DetailCount, run.SellerPageCount, run.AnalyzedCount, run.RecommendedCount, run.NotifiedCount, warnings)
		if err != nil {
			return seedCounts{}, err
		}
		itemIDs := []string{}
		for _, finding := range data.findings {
			if finding.TaskID == *run.TaskID {
				itemIDs = append(itemIDs, finding.ItemID)
			}
		}
		snapshot, err := jsonColumn(map[string]any{"run_id": run.ID, "search_generation": int64(1), "item_ids": itemIDs})
		if err != nil {
			return seedCounts{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?)`, "task.latest_search."+strconv.FormatInt(*run.TaskID, 10), snapshot); err != nil {
			return seedCounts{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('stats.started_at',?)`, strconv.FormatInt(oldest, 10)); err != nil {
		return seedCounts{}, err
	}
	if err := tx.Commit(); err != nil {
		return seedCounts{}, err
	}
	return seedCounts{tasks: len(data.tasks), listings: len(data.listings), findings: len(data.findings)}, nil
}
