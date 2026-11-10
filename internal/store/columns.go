package store

import (
	"context"
	"fmt"
	"strings"
)

// 只检查程序所需的表列，不拒绝用户额外建立的查询索引。
var requiredColumns = map[string]string{
	"admin":                "id username password_hash totp_secret_enc totp_enabled_at totp_last_step created_at password_changed_at",
	"auth_tokens":          "id kind token_hash name created_at last_used_at expires_at revoked_at",
	"settings":             "key value_json updated_at",
	"prompts":              "key content updated_at",
	"proxies":              "id uuid name url_enc created_at",
	"accounts":             "id name proxy_id nickname avatar_url state state_reason state_changed_at cooldown_until verify_level verify_url last_cooldown_end_at cooldown_manual_used last_run_ended_at next_gap_seconds last_used_at created_at",
	"quota_events":         "id account_id kind run_id at",
	"tasks":                "id name keyword enabled account_id account_pinned min_price max_price personal_only free_shipping_only publish_window region_province region_city region_district max_pages interval_minutes crawl_overrides_json judge_mode requirement profile_json budget_min budget_max match_keywords_json exclude_keywords_json analyze_on_first_run notify_policy notify_price_drop search_generation baseline_pending sort_index created_at updated_at last_run_at last_success_at next_run_at last_run_outcome last_error consecutive_failures failure_paused_until no_good_hint_dismissed_at",
	"task_channels":        "task_id channel_id",
	"listings":             "item_id title price price_text original_price_text thumbnail_url image_urls_json desktop_url area seller_id seller_nick published_at want_count browse_count tags_json description seller_registration_days zhima_level first_seen_at last_seen_at detail_fetched_at is_favorite favorited_at is_dismissed last_drop_amount last_drop_at sale_status",
	"price_points":         "id item_id price observed_at",
	"task_sightings":       "task_id item_id day price last_seen_at",
	"task_item_specs":      "task_id item_id kind specs_json brand_new config_key excluded source updated_at",
	"seller_profiles":      "seller_id nick avatar_url introduction seller_credit buyer_credit seller_credit_level buyer_credit_level item_count rate_count on_sale_count sold_count seller_good_rate buyer_good_rate recent_items_json recent_rates_json fetched_at seller_good_count seller_rate_count buyer_good_count buyer_rate_count",
	"blocked_sellers":      "seller_id nick created_at",
	"seen_items":           "task_id item_id first_seen_at",
	"findings":             "id task_id item_id search_generation found_at is_baseline decision reject_kind stage decided_layer reason analysis_json user_overrides_json score grade score_breakdown_json fair_price fair_sample fair_basis scored_price scored_at summary seller_type title_screen_reason matched_keywords_json analysis_model analysis_provider_id analyzed_at attempts retryable reanalyze_requested_at is_read notify_kind notify_skip_reason notified_at recommended_price last_drop_notified_price drop_notified_at reanalysis_origin processing_revision",
	"ai_providers":         "id uuid name preset base_url api_key_enc vision_model text_model enabled sort_index error_state error_at",
	"ai_compat":            "provider_id model json_mode send_temperature max_tokens_param max_tokens_floor updated_at",
	"ai_calls":             "id at provider_id model purpose task_id ok http_status error_kind json_mode input_tokens output_tokens reasoning_tokens finish_reason duration_ms",
	"channels":             "id uuid type name enabled config_json secret_enc last_test_at last_test_ok last_error",
	"deliveries":           "id at channel_id task_id kind item_count ok error",
	"delivery_items":       "delivery_id finding_id",
	"notification_intents": "id finding_id task_id kind state created_at updated_at price previous_price failure_count first_failed_at sent_at reason",
	"delivery_intents":     "delivery_id intent_id",
	"run_records":          "id task_id task_name trigger account_id account_name proxy_name started_at ended_at outcome failure_kind message repeat_count page_count scanned_count new_count layer1_rejected layer2_count layer2_rejected detail_count seller_page_count analyzed_count recommended_count notified_count warnings_json",
	"run_steps":            "run_id seq at status message detail",
	"run_skip_events":      "run_id seq at",
	"system_events":        "id kind subject_id message created_at resolved_at notified_at",
	"market_insights":      "id task_id generated_at stats_json text provider_id model",
	"meta":                 "key value",
}

func checkColumns(ctx context.Context, db queryer) error {
	for table, columns := range requiredColumns {
		found, err := columnNames(ctx, db, table)
		if err != nil {
			return err
		}
		for _, column := range strings.Fields(columns) {
			if !found[column] {
				return fmt.Errorf("数据库结构损坏，缺少 %s.%s", table, column)
			}
		}
	}
	return nil
}

func columnNames(ctx context.Context, db queryer, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM pragma_table_info(?) WHERE EXISTS (SELECT 1 FROM sqlite_schema WHERE type='table' AND name=?)", table, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		found[name] = true
	}
	return found, rows.Err()
}
