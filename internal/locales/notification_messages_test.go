package locales

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func TestNotificationMessagesHaveBothLanguages(t *testing.T) {
	for _, message := range []Message{
		{Code: "notify.baseline"},
		{Code: "notify.policy_none"},
		{Code: "notify.dismissed"},
		{Code: "notify.blocked"},
		{Code: "notify.over_budget", Args: map[string]any{"price": "¥3,400", "budget": "¥3,000"}},
		{Code: "notify.below_steal", Args: map[string]any{"score": int64(89)}},
		{Code: "notify.below_good", Args: map[string]any{"score": int64(72)}},
		{Code: "notify.below_fair", Args: map[string]any{"score": int64(64)}},
		{Code: "notify.no_comparison_steal"},
		{Code: "notify.keyword_miss"},
		{Code: "notify.no_channel"},
		{Code: "notify.reanalysis"},
		{Code: "notify.retry_exhausted"},
		{Code: "notify.cancelled_unavailable"},
		{Code: "notify.cancelled_policy"},
		{Code: "notify.cancelled_search"},
		{Code: "channels.not_found"},
		{Code: "channels.invalid"},
		{Code: "channels.name_in_use"},
		{Code: "channels.disabled"},
		{Code: "channels.changed"},
		{Code: "channels.system_target_required"},
		{Code: "deliveries.not_found"},
		{Code: "notification.paused"},
		{Code: "analysis.seller_legacy"},
		{Code: "analysis.seller_legacy_partial"},
		{Code: "notify.price_unknown_budget"},
		{Code: "notify.reanalysis_below_recommendation", Args: map[string]any{"score": int64(72)}},
		{Code: "notification.closed"},
		{Code: "notification.storage_failure"},
		{Code: "notification.delivery.invalid_config", Args: map[string]any{"channel": "测试渠道"}},
		{Code: "notification.delivery.invalid_message", Args: map[string]any{"channel": "测试渠道"}},
		{Code: "notification.delivery.cancelled", Args: map[string]any{"channel": "测试渠道"}},
		{Code: "notification.delivery.timeout", Args: map[string]any{"channel": "测试渠道"}},
		{Code: "notification.delivery.transport", Args: map[string]any{"channel": "测试渠道"}},
		{Code: "notification.delivery.http_status", Args: map[string]any{"channel": "测试渠道", "status": 401}},
		{Code: "notification.delivery.invalid_response", Args: map[string]any{"channel": "测试渠道"}},
		{Code: "notification.delivery.provider_rejected", Args: map[string]any{"channel": "测试渠道", "code": "40014"}},
		{Code: "notification.delivery.provider_rejected_generic", Args: map[string]any{"channel": "测试渠道"}},
		{Code: "notification.delivery.smtp_rejected", Args: map[string]any{"channel": "测试渠道"}},
		{Code: "notification.price_unknown"},
		{Code: "notification.open_xianyu"},
		{Code: "notification.open_kakatta"},
		{Code: "notification.footer"},
		{Code: "notification.action_match"},
		{Code: "notification.action_unpriced"},
		{Code: "notification.action_upgrade", Args: map[string]any{"grade": "Good deal"}},
		{Code: "notification.action_new"},
		{Code: "notification.action_drop"},
		{Code: "notification.action_score", Args: map[string]any{"action": "Good deal", "score": int64(84)}},
		{Code: "notification.price_from_to", Args: map[string]any{"previous": "¥1,300", "current": "¥1,150"}},
		{Code: "notification.price_arrow", Args: map[string]any{"previous": "¥1,300", "current": "¥1,150"}},
		{Code: "notification.unmatched", Args: map[string]any{"reason": "{raw} 原话"}},
		{Code: "notification.no_configuration"},
		{Code: "notification.no_comparable_sample", Args: map[string]any{"count": int64(1)}},
		{Code: "notification.no_comparable_sample", Args: map[string]any{"count": int64(3)}},
		{Code: "notification.seller_personal"},
		{Code: "notification.seller_dealer_suspect"},
		{Code: "notification.seller_unavailable"},
		{Code: "notification.seller_with_new_account", Args: map[string]any{"seller": "Personal seller"}},
		{Code: "notification.pending_checks", Args: map[string]any{"count": 1, "names": "ID lock"}},
		{Code: "notification.pending_checks", Args: map[string]any{"count": 6, "names": "ID lock、MDM"}},
		{Code: "notification.price_equal"},
		{Code: "notification.price_lower_under_one_percent"},
		{Code: "notification.price_higher_under_one_percent"},
		{Code: "notification.price_lower", Args: map[string]any{"percent": "23"}},
		{Code: "notification.price_higher", Args: map[string]any{"percent": "23"}},
		{Code: "notification.summary_recommendations", Args: map[string]any{"count": 1}},
		{Code: "notification.summary_recommendations", Args: map[string]any{"count": 13}},
		{Code: "notification.summary_alerts", Args: map[string]any{"count": 1}},
		{Code: "notification.summary_alerts", Args: map[string]any{"count": 13}},
		{Code: "notification.task_prefix", Args: map[string]any{"task": "{raw} 任务"}},
		{Code: "notification.title_action", Args: map[string]any{"task": "[相机] ", "action": "Good deal 84"}},
		{Code: "notification.title_basic", Args: map[string]any{"task": "[相机] ", "action": "Good deal 84", "price": "¥1,300"}},
		{Code: "notification.title_spec", Args: map[string]any{"task": "[相机] ", "action": "Good deal 84", "price": "¥1,300", "spec": "mini 6 256G"}},
		{Code: "notification.test_title"},
		{Code: "notification.test_body"},
		{Code: "notification.system.account_verify.title", Args: map[string]any{"name": "账号"}},
		{Code: "notification.system.account_verify.body", Args: map[string]any{"count": int64(1)}},
		{Code: "notification.system.account_verify.body", Args: map[string]any{"count": int64(3)}},
		{Code: "notification.system.account_deny.title", Args: map[string]any{"name": "账号"}},
		{Code: "notification.system.account_deny.body", Args: map[string]any{"hours": int64(1), "until": "14:32", "count": int64(1)}},
		{Code: "notification.system.account_deny.body", Args: map[string]any{"hours": int64(3), "until": "14:32", "count": int64(3)}},
		{Code: "notification.system.account_login.title", Args: map[string]any{"name": "账号"}},
		{Code: "notification.system.account_login.body"},
		{Code: "notification.system.ai_error.title", Args: map[string]any{"provider": "服务商"}},
		{Code: "notification.system.ai_error.body", Args: map[string]any{"reason": "API Key 无效"}},
		{Code: "notification.system.ai_budget.title"},
		{Code: "notification.system.ai_budget.body", Args: map[string]any{"tokens": int64(1), "count": int64(1)}},
		{Code: "notification.system.ai_budget.body", Args: map[string]any{"tokens": int64(3), "count": int64(3)}},
		{Code: "notification.system.task_failure_pause.title", Args: map[string]any{"task": "任务", "count": int64(1)}},
		{Code: "notification.system.task_failure_pause.title", Args: map[string]any{"task": "任务", "count": int64(3)}},
		{Code: "notification.system.task_failure_pause.body", Args: map[string]any{"minutes": int64(1), "reason": "页面超时", "count": int64(1)}},
		{Code: "notification.system.task_failure_pause.body", Args: map[string]any{"minutes": int64(3), "reason": "页面超时", "count": int64(3)}},
		{Code: "notification.system.xianyu_layout.title"},
		{Code: "notification.system.xianyu_layout.body", Args: map[string]any{"reason": "缺少商品列表"}},
		{Code: "notification.system.login_failures.title"},
		{Code: "notification.system.login_failures.body", Args: map[string]any{"count": int64(1), "ip": "127.0.0.1"}},
		{Code: "notification.system.login_failures.body", Args: map[string]any{"count": int64(3), "ip": "127.0.0.1"}},
		{Code: "notification.system.browser_held", Args: map[string]any{"until": "14:32"}},
		{Code: "notification.system.environment"},
		{Code: "notification.system.reassigned", Args: map[string]any{"count": int64(1), "reassigned_to": "账号2"}},
		{Code: "notification.system.reassigned", Args: map[string]any{"count": int64(3), "reassigned_to": "账号2"}},
		{Code: "notification.check_names", Args: map[string]any{"first": "ID lock", "second": "MDM"}},
		{Code: "analysis.grade.steal"},
		{Code: "analysis.grade.good"},
		{Code: "analysis.grade.fair"},
		{Code: "analysis.grade.pricey"},
		{Code: "analysis.grade.avoid"},
		{Code: "analysis.risk_name.fake"},
		{Code: "analysis.risk_name.refurbished"},
		{Code: "analysis.risk_name.official_replacement"},
		{Code: "analysis.risk_name.resource_device"},
		{Code: "analysis.risk_name.expanded"},
		{Code: "analysis.risk_name.assembled"},
		{Code: "analysis.risk_name.unknown_source"},
		{Code: "analysis.risk_name.id_lock"},
		{Code: "analysis.risk_name.mdm"},
		{Code: "analysis.risk_name.carrier_lock"},
		{Code: "analysis.risk_name.account_bound"},
		{Code: "analysis.risk_name.console_banned"},
		{Code: "analysis.risk_name.repaired"},
		{Code: "analysis.risk_name.water_damage"},
		{Code: "analysis.risk_name.screen_replaced"},
		{Code: "analysis.risk_name.battery_replaced"},
		{Code: "analysis.risk_name.cosmetic_damage"},
		{Code: "analysis.risk_name.malfunction"},
		{Code: "analysis.risk_name.missing_parts"},
		{Code: "analysis.risk_name.stains"},
		{Code: "analysis.risk_name.pilling"},
		{Code: "analysis.risk_name.odor"},
		{Code: "analysis.risk_name.washed"},
		{Code: "analysis.risk_name.mold"},
		{Code: "analysis.risk_name.mining"},
		{Code: "analysis.risk_name.notes_marks"},
		{Code: "analysis.risk_name.size_mismatch"},
		{Code: "analysis.risk_name.photo_mismatch"},
		{Code: "analysis.risk_name.stock_photo"},
		{Code: "analysis.risk_name.vague"},
		{Code: "analysis.risk_name.off_platform"},
		{Code: "analysis.risk_name.deposit"},
		{Code: "analysis.risk_name.pickup_only"},
		{Code: "analysis.risk_name.no_return"},
		{Code: "analysis.risk_name.no_inspection"},
		{Code: "analysis.risk_name.seller_dealer"},
		{Code: "analysis.risk_name.seller_new"},
		{Code: "analysis.risk_name.seller_bad_reviews"},
		{Code: "analysis.risk_name.seller_partial_refund"},
	} {
		for _, lang := range []Language{ZhCN, En, Ja} {
			got, err := Format(lang, message)
			if err != nil || strings.TrimSpace(got) == "" || got == message.Code {
				t.Errorf("通知消息%s/%s无法显示：%q，%v", message.Code, lang, got, err)
			}
		}
	}
}

func TestNotificationQuantityMessages(t *testing.T) {
	for _, tc := range []struct {
		code       string
		args       map[string]any
		countField string
		one, other string
	}{
		{"notification.no_comparable_sample", nil, "", "%d comparable listing", "%d comparable listings"},
		{"notification.pending_checks", map[string]any{"names": "ID lock"}, "", "%d check needs confirmation", "%d checks need confirmation"},
		{"notification.summary_recommendations", nil, "", "%d more recommendation", "%d more recommendations"},
		{"notification.summary_alerts", nil, "", "%d more alert", "%d more alerts"},
		{"notification.system.account_verify.body", nil, "", "%d task on this account is paused", "%d tasks on this account are paused"},
		{"notification.system.account_deny.body", map[string]any{"until": "14:32"}, "hours", "%d hour and will resume", "%d hours and will resume"},
		{"notification.system.ai_budget.body", nil, "tokens", "%d token used", "%d tokens used"},
		{"notification.system.task_failure_pause.title", map[string]any{"task": "Camera"}, "", "Failed %d time in a row", "Failed %d times in a row"},
		{"notification.system.task_failure_pause.body", map[string]any{"reason": "Page timed out"}, "minutes", "Paused for %d minute", "Paused for %d minutes"},
		{"notification.system.login_failures.body", map[string]any{"ip": "192.0.2.1"}, "", "%d attempt", "%d attempts"},
		{"notification.system.reassigned", map[string]any{"reassigned_to": "Home"}, "", "%d task has moved", "%d tasks have moved"},
	} {
		if tc.args == nil {
			tc.args = map[string]any{}
		}
		for _, count := range []int64{0, 1, 2} {
			tc.args["count"] = count
			if tc.code == "notification.pending_checks" || strings.HasPrefix(tc.code, "notification.summary_") {
				tc.args["count"] = int(count)
			}
			if tc.countField != "" {
				tc.args[tc.countField] = count
			}
			message := Message{Code: tc.code, Args: tc.args}
			for _, lang := range []Language{ZhCN, En, Ja} {
				got, err := Format(lang, message)
				if err != nil || !strings.Contains(got, fmt.Sprint(count)) {
					t.Fatalf("通知数量%s/%s/%d：%q，%v", tc.code, lang, count, got, err)
				}
				if lang == En {
					want := tc.other
					if count == 1 {
						want = tc.one
					}
					pattern := regexp.QuoteMeta(fmt.Sprintf(want, count)) + `\b`
					if !regexp.MustCompile(pattern).MatchString(got) {
						t.Errorf("通知数量%s/%d的单复数不对：%q", tc.code, count, got)
					}
				}
			}
		}
		delete(tc.args, "count")
		for _, lang := range []Language{ZhCN, En, Ja} {
			if got, err := Format(lang, Message{Code: tc.code, Args: tc.args}); err == nil || got != "" {
				t.Errorf("通知数量%s/%s缺count仍显示：%q，%v", tc.code, lang, got, err)
			}
		}
	}
}

func TestNotificationArgumentsRemainPlainText(t *testing.T) {
	for _, tc := range []struct {
		message    Message
		zh, en, ja string
	}{
		{Message{Code: "notify.over_budget", Args: map[string]any{"price": "¥3,400", "budget": "¥3,000"}}, "当前价 ¥3,400 超过预算上限 ¥3,000", "The price of ¥3,400 exceeds the budget limit of ¥3,000", "現在の価格 ¥3,400 が予算上限 ¥3,000 を超えています"},
		{Message{Code: "notification.unmatched", Args: map[string]any{"reason": "原话 {name}"}}, "不符合需求：原话 {name}", "Does not meet your requirements: 原话 {name}", "購入条件に合いません：原话 {name}"},
		{Message{Code: "notification.check_names", Args: map[string]any{"first": "{name}", "second": "ID lock"}}, "{name}、ID lock", "{name}, ID lock", "{name}、ID lock"},
		{Message{Code: "notification.system.ai_error.body", Args: map[string]any{"reason": "原话 {status}"}}, "原话 {status}", "原话 {status}", "原话 {status}"},
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
				t.Errorf("通知参数%s/%s：%q，期望%q，%v", tc.message.Code, lang, got, want, err)
			}
		}
	}
}

func TestDeliveryMessagesReuseLocalizedCauses(t *testing.T) {
	for _, cause := range []string{
		"invalid_config", "invalid_message", "cancelled", "timeout", "transport",
		"http_status", "invalid_response", "provider_rejected", "provider_rejected_generic", "smtp_rejected",
	} {
		args := map[string]any{"channel": "群{status}"}
		if cause == "http_status" {
			args["status"] = 401
		}
		if cause == "provider_rejected" {
			args["code"] = "40014"
		}
		for _, lang := range []Language{ZhCN, En, Ja} {
			reason, err := Format(lang, Message{Code: "notify.error." + cause, Args: args})
			if err != nil {
				t.Fatal(err)
			}
			prefix := "群{status} 发送失败："
			if lang == En {
				prefix = "群{status} failed: "
			} else if lang == Ja {
				prefix = "群{status} への送信に失敗："
			}
			got, err := Format(lang, Message{Code: "notification.delivery." + cause, Args: args})
			if err != nil || got != prefix+reason {
				t.Errorf("投递原因%s/%s没有完整本地化：%q，%v", cause, lang, got, err)
			}
		}
	}
}
