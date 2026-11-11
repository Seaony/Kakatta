package locales

import (
	"strconv"
	"strings"
	"testing"
)

func TestJudgmentModeChangedMessage(t *testing.T) {
	for _, tc := range []struct {
		lang Language
		want string
	}{{ZhCN, "判断方式已修改"}, {En, "Judgment method changed"}} {
		got, err := Format(tc.lang, Message{Code: "notify.mode_changed", Args: map[string]any{}})
		if err != nil || got != tc.want {
			t.Errorf("判断方式变更原因%s：%q，期望%q，%v", tc.lang, got, tc.want, err)
		}
	}
}

func TestBusinessMessagesHaveBothLanguages(t *testing.T) {
	for _, message := range []Message{
		{Code: "service.invalid_query"},
		{Code: "notification.legacy_retry_unknown"},
		{Code: "analysis.prompts.invalid_input", Args: map[string]any{"field": "product.price_cents"}},
		{Code: "analysis.prompts.missing_keys", Args: map[string]any{"kind": "analysis", "keys": `"checks", "adjust"`}},
		{Code: "analysis.images.invalid_configuration"},
		{Code: "analysis.images.invalid_count"},
		{Code: "analysis.images.invalid_url"},
		{Code: "analysis.images.download_failed"},
		{Code: "analysis.images.http_failed", Args: map[string]any{"status": 503}},
		{Code: "analysis.images.timeout"},
		{Code: "analysis.images.invalid_image"},
		{Code: "analysis.images.resource_limit", Args: map[string]any{"field": "source_bytes"}},
		{Code: "service.storage_failure"},
		{Code: "listings.not_found"},
		{Code: "listings.invalid_patch"},
		{Code: "findings.invalid_selection"},
		{Code: "findings.not_found"},
		{Code: "findings.changed"},
		{Code: "findings.backlog_expired"},
		{Code: "tasks.invalid"},
		{Code: "tasks.invalid_id"},
		{Code: "tasks.not_found"},
		{Code: "tasks.text_length", Args: map[string]any{"min": 1, "max": 50}},
		{Code: "profile.check_exists"},
		{Code: "accounts.invalid", Args: map[string]any{"field": "proxy_id"}},
		{Code: "accounts.not_found"},
		{Code: "accounts.id_capacity"},
		{Code: "accounts.cleanup_failed"},
		{Code: "accounts.signed_out"},
		{Code: "accounts.access_denied", Args: map[string]any{"name": "家用账号", "hours": int64(24), "count": int64(24)}},
		{Code: "accounts.environment_denied", Args: map[string]any{"name": "家用账号", "hours": int64(24), "count": int64(24)}},
		{Code: "accounts.login_expired", Args: map[string]any{"name": "家用账号", "count": 1}},
		{Code: "accounts.verification_required", Args: map[string]any{"name": "家用账号", "count": int64(2)}},
		{Code: "proxies.invalid", Args: map[string]any{"field": "name"}},
		{Code: "proxies.invalid_url", Args: map[string]any{"field": "url"}},
		{Code: "proxies.name_in_use", Args: map[string]any{"field": "name"}},
		{Code: "proxies.not_found"},
		{Code: "proxies.in_use"},
		{Code: "proxies.test_failed"},
		{Code: "blocked_sellers.invalid_id"},
		{Code: "blocked_sellers.invalid_nick"},
		{Code: "notify.cancelled_deleted"},
		{Code: "notify.cancelled_dismissed"},
		{Code: "notify.cancelled_blocked"},
		{Code: "runtime.session_not_found"},
		{Code: "runtime.session_changed"},
		{Code: "run.account_expired"},
		{Code: "run.account_needs_verification"},
		{Code: "run.account_cooling"},
		{Code: "run.hourly_quota", Args: map[string]any{"used": 28, "limit": 30}},
		{Code: "run.daily_search_quota", Args: map[string]any{"used": int64(195), "limit": int64(200)}},
		{Code: "run.daily_detail_quota", Args: map[string]any{"used": int64(100), "limit": int64(100)}},
		{Code: "service.search_page_failed", Args: map[string]any{"page": int64(2), "cause": "解析第2页搜索结果失败"}},
		{Code: "await_ai", Args: map[string]any{"cause": "daily_limit"}},
		{Code: "run.restart"},
		{Code: "findings.dismissed"},
		{Code: "findings.blocked_seller"},
		{Code: "findings.baseline"},
		{Code: "runs.invalid_record"},
		{Code: "runs.not_found"},
		{Code: "runs.changed"},
		{Code: "runs.invalid_step"},
		{Code: "maintenance.finding_expired"},
		{Code: "run.quota_capacity", Args: map[string]any{"count": int64(4), "required": int64(4), "hourly_limit": 2, "daily_limit": int64(200)}},
		{Code: "run.completed", Args: map[string]any{"count": int64(2), "page_count": int64(2), "analyzed_count": int64(5)}},
		{Code: "run.no_new_items"},
		{Code: "run.cancelled"},
		{Code: "run.timeout"},
		{Code: "run.risk_verify"},
		{Code: "run.risk_deny"},
		{Code: "run.risk_env"},
		{Code: "run.login_expired"},
		{Code: "run.quota"},
		{Code: "run.network_failed"},
		{Code: "run.page_failed"},
		{Code: "run.layout_changed", Args: map[string]any{"reason": Message{Code: "run.layout_response_invalid"}}},
		{Code: "run.market_failed"},
		{Code: "run.failure_pause", Args: map[string]any{"task": "手机", "count": int64(3), "minutes": int64(30), "reason": Message{Code: "run.page_failed"}}},
		{Code: "run.no_account"},
		{Code: "tasks.invalid_region"},
		{Code: "tasks.exclusions_changed"},
		{Code: "run.disabled"},
		{Code: "run.not_due"},
		{Code: "run.failure_paused"},
		{Code: "run.account_signed_out"},
		{Code: "run.account_gap"},
		{Code: "run.account_denied"},
		{Code: "run.ai_daily_limit"},
		{Code: "run.quiet_hours"},
		{Code: "run.empty_profile"},
		{Code: "run.no_ai_provider"},
		{Code: "run.already_requested"},
		{Code: "runtime.closed"},
		{Code: "runtime.verification_not_pending"},
		{Code: "run.search_changed"},
		{Code: "runtime.remote_not_ready"},
		{Code: "run.detail_failed"},
		{Code: "maintenance.confirmation_required"},
		{Code: "maintenance.image_cleanup_failed"},
		{Code: "run.model_config_changed"},
		{Code: "run.seller_page_failed"},
		{Code: "run.seller_head_failed"},
		{Code: "run.seller_items_failed"},
		{Code: "run.seller_ratings_failed"},
		{Code: "run.seller_ratings_missing"},
		{Code: "run.seller_ratings_quota"},
		{Code: "run.seller_page_limit"},
		{Code: "run.seller_quota"},
		{Code: "run.seller_failed"},
		{Code: "run.seller_missing"},
		{Code: "run.layout_response_invalid"},
		{Code: "run.layout_empty_repeated", Args: map[string]any{"count": int64(1)}},
		{Code: "run.layout_empty_repeated", Args: map[string]any{"count": int64(3)}},
	} {
		for _, lang := range []Language{ZhCN, En, Ja} {
			got, err := Format(lang, message)
			if err != nil || strings.TrimSpace(got) == "" || got == message.Code {
				t.Errorf("业务消息%s/%s无法显示：%q，%v", message.Code, lang, got, err)
			}
		}
	}
}

func TestBrowserStepMessagesHaveOptionalDiagnostics(t *testing.T) {
	for _, message := range []Message{
		{Code: "run.browser_home_opened"},
		{Code: "run.browser_search_input_found"},
		{Code: "run.browser_search_input_cleared"},
		{Code: "run.browser_search_entered"},
		{Code: "run.browser_advertisement_closed"},
		{Code: "run.browser_published_selected"},
		{Code: "run.browser_wait", Args: map[string]any{"duration_ms": int64(1500)}},
		{Code: "run.browser_element_lookup", Args: map[string]any{"found": false, "rule_version": 1}},
		{Code: "run.browser_response", Args: map[string]any{"duration_ms": int64(300)}},
		{Code: "run.browser_detail_opened", Args: map[string]any{"duration_ms": int64(1000), "image_count": 4, "has_description": true}},
		{Code: "run.browser_region_filter_found", Args: map[string]any{"rule_version": 1}},
		{Code: "run.browser_price_filter_found", Args: map[string]any{"rule_version": 1}},
		{Code: "run.browser_filter_found", Args: map[string]any{"rule_version": 1}},
		{Code: "run.browser_login_box_seen"},
		{Code: "run.browser_login_cookie_warning"},
		{Code: "run.browser_operation"},
		{Code: "run.browser_retry"},
	} {
		for _, args := range []map[string]any{message.Args, {}} {
			for _, lang := range []Language{ZhCN, En, Ja} {
				got, err := Format(lang, Message{Code: message.Code, Args: args})
				if err != nil || strings.TrimSpace(got) == "" {
					t.Errorf("浏览器步骤%s/%s缺少可选诊断值就无法显示：%q，%v", message.Code, lang, got, err)
				}
			}
		}
	}
}

func TestBusinessMessageArgumentsPreserveTheirMeaning(t *testing.T) {
	for _, tc := range []struct {
		message    Message
		zh, en, ja string
	}{
		{Message{Code: "service.invalid_query"}, "查询条件或游标无效", "The query or cursor is invalid", "検索条件またはカーソルが無効です。"},
		{Message{Code: "notification.legacy_retry_unknown"}, "旧提醒的首次失败时间无法确认，已停止重发", "The original failure time is unknown; retries have stopped", "以前の通知で最初に失敗した時刻を確認できないため、再送を停止しました"},
		{Message{Code: "maintenance.confirmation_required"}, "请输入「清除」确认清除业务数据", `Enter "清除" to confirm clearing business data`, "業務データの消去を確認するため、「清除」と入力してください。"},
		{Message{Code: "analysis.prompts.invalid_input", Args: map[string]any{"field": "{price}"}}, "模型输入无效：{price}", "Invalid model input: {price}", "モデルへの入力が無効です：{price}"},
		{Message{Code: "analysis.prompts.missing_keys", Args: map[string]any{"kind": "analysis", "keys": `"checks", "adjust"`}}, `提示词缺少带引号的键名："checks", "adjust"`, `Prompt is missing quoted keys: "checks", "adjust"`, "プロンプトに引用符付きのキーがありません：\"checks\", \"adjust\""},
		{Message{Code: "analysis.images.http_failed", Args: map[string]any{"status": 503}}, "图片下载失败：HTTP 503", "Image download failed: HTTP 503", "画像のダウンロードに失敗しました：HTTP 503"},
		{Message{Code: "analysis.images.resource_limit", Args: map[string]any{"field": "source_bytes"}}, "图片超出资源限制：source_bytes", "Image exceeds the resource limit: source_bytes", "画像がリソースの上限を超えています：source_bytes"},
		{Message{Code: "service.search_page_failed", Args: map[string]any{"page": int64(2), "cause": "page {code} failed"}}, "第 2 页搜索失败：page {code} failed", "Search page 2 failed: page {code} failed", "検索の2ページ目で失敗しました：page {code} failed"},
		{Message{Code: "run.daily_detail_quota", Args: map[string]any{"used": int64(100), "limit": int64(100), "next_allowed_at": int64(1_800_000_000_000)}}, "今日详情及卖家页访问额度不足（已用 100/100）", "Not enough daily detail and seller-page visits remain (100/100 used)", "今日の商品詳細・出品者ページの残りアクセス回数が不足しています（使用済み100/100）。"},
	} {
		for _, lang := range []Language{ZhCN, En, Ja} {
			want := tc.zh
			if lang == En {
				want = tc.en
			} else if lang == Ja {
				want = tc.ja
			}
			got, err := Format(lang, tc.message)
			if err != nil || got != want {
				t.Errorf("业务消息%s/%s：%q，期望%q，%v", tc.message.Code, lang, got, want, err)
			}
		}
	}
}

func TestBusinessMessageCountForms(t *testing.T) {
	for _, tc := range []struct{ code, unit string }{
		{"accounts.access_denied", "hour"},
		{"accounts.environment_denied", "hour"},
		{"accounts.login_expired", "task"},
		{"accounts.verification_required", "task"},
		{"run.quota_capacity", "visit"},
		{"run.completed", "page"},
		{"run.failure_pause", "time"},
		{"run.layout_empty_repeated", "time"},
	} {
		for _, count := range []int64{0, 1, 2} {
			args := map[string]any{"count": count}
			switch tc.code {
			case "accounts.access_denied", "accounts.environment_denied":
				args["name"], args["hours"] = "家用账号", count
			case "accounts.login_expired":
				args["name"], args["count"] = "家用账号", int(count)
			case "accounts.verification_required":
				args["name"] = "家用账号"
			case "run.quota_capacity":
				args["required"], args["hourly_limit"], args["daily_limit"] = count, 2, int64(200)
			case "run.completed":
				args["page_count"], args["analyzed_count"] = count, int64(5)
			case "run.failure_pause":
				args["task"], args["minutes"] = "手机", int64(1)
				args["reason"] = Message{Code: "run.page_failed"}
			}
			for _, lang := range []Language{ZhCN, En, Ja} {
				got, err := Format(lang, Message{Code: tc.code, Args: args})
				if err != nil || !strings.Contains(got, strconv.FormatInt(count, 10)) {
					t.Fatalf("数量消息%s/%s/%d：%q，%v", tc.code, lang, count, got, err)
				}
				if lang == En {
					want := tc.unit
					if count != 1 {
						want += "s"
					}
					words := strings.Fields(got)
					found := false
					for i := 0; i+1 < len(words); i++ {
						if words[i] == strconv.FormatInt(count, 10) && strings.Trim(words[i+1], ".,;()") == want {
							found = true
						}
					}
					if !found {
						t.Errorf("数量消息%s/%d应使用%s：%q", tc.code, count, want, got)
					}
				}
			}
			delete(args, "count")
			for _, lang := range []Language{ZhCN, En, Ja} {
				if got, err := Format(lang, Message{Code: tc.code, Args: args}); err == nil || got != "" {
					t.Errorf("数量消息%s/%s缺count仍显示：%q，%v", tc.code, lang, got, err)
				}
			}
		}
	}
}
