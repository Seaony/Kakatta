package settings

import (
	"encoding/json"
	"strings"

	"github.com/Seaony/Kakatta/internal/locales"
)

type Kind string

const (
	KindInteger    Kind = "integer"
	KindBoolean    Kind = "boolean"
	KindString     Kind = "string"
	KindRange      Kind = "range"
	KindQuietHours Kind = "quiet_hours"
	KindIDs        Kind = "ids"
	KindStrings    Kind = "strings"
	KindNullableID Kind = "nullable_id"
)

type Recommendation struct {
	Min  *float64        `json:"min"`
	Max  *float64        `json:"max"`
	Text locales.Message `json:"text"`
}

type Definition struct {
	Key            string            `json:"key"`
	Group          string            `json:"group"`
	Kind           Kind              `json:"kind"`
	Default        json.RawMessage   `json:"default"`
	Unit           string            `json:"unit"`
	Options        []json.RawMessage `json:"options"`
	Recommendation *Recommendation   `json:"recommendation"`
	Label          locales.Message   `json:"label"`
	Why            locales.Message   `json:"why"`
	Risk           locales.Message   `json:"risk"`
	TaskOverride   bool              `json:"task_override"`
}

func definition(key string, kind Kind, value, unit string, min, max float64, task bool, options ...string) Definition {
	prefix := "settings." + key + "."
	r := &Recommendation{Text: locales.Message{Code: prefix + "recommendation"}}
	if min >= 0 {
		r.Min = &min
	}
	if max >= 0 {
		r.Max = &max
	}
	d := Definition{Key: key, Group: strings.SplitN(key, ".", 2)[0], Kind: kind, Default: json.RawMessage(value), Unit: unit,
		Options: []json.RawMessage{}, Recommendation: r, Label: locales.Message{Code: prefix + "label"},
		Why: locales.Message{Code: prefix + "why"}, Risk: locales.Message{Code: prefix + "risk"}, TaskOverride: task}
	for _, option := range options {
		d.Options = append(d.Options, json.RawMessage(option))
	}
	return d
}

// Definitions 每次都返回新值，设置页改动元数据不能改变默认值。
func Definitions(env Environment) []Definition {
	tz := env.TZ
	if tz == "" {
		tz = "Asia/Shanghai"
	}
	timezone, _ := json.Marshal(tz)
	return []Definition{
		definition("crawl.task_interval_minutes", KindInteger, `30`, "minutes", 30, 120, false),
		definition("crawl.interval_jitter_percent", KindInteger, `30`, "percent", 0, -1, true),
		definition("crawl.account_min_gap_seconds", KindInteger, `180`, "seconds", 120, -1, false),
		definition("crawl.account_min_gap_jitter_seconds", KindInteger, `60`, "seconds", 0, -1, false),
		definition("crawl.account_hourly_visits", KindInteger, `30`, "count", -1, 40, false),
		definition("crawl.account_daily_search_visits", KindInteger, `200`, "count", -1, 200, false),
		definition("crawl.account_daily_detail_visits", KindInteger, `100`, "count", -1, 150, false),
		definition("crawl.max_pages", KindInteger, `1`, "count", 1, 3, false),
		definition("crawl.page_wait_seconds", KindRange, `{"min":10,"max":15}`, "seconds", 10, -1, true),
		definition("crawl.max_details_per_run", KindInteger, `5`, "count", 3, 10, true),
		definition("crawl.detail_pre_wait_seconds", KindRange, `{"min":3,"max":6}`, "seconds", 2, -1, true),
		definition("crawl.detail_stay_seconds", KindRange, `{"min":10,"max":20}`, "seconds", 8, -1, true),
		definition("crawl.action_wait_seconds", KindRange, `{"min":1,"max":3}`, "seconds", 1, -1, true),
		definition("crawl.home_stay_seconds", KindRange, `{"min":2,"max":5}`, "seconds", 2, -1, true),
		definition("crawl.typing_interval_ms", KindRange, `{"min":50,"max":130}`, "milliseconds", -1, -1, true),
		definition("crawl.seller_cache_hours", KindInteger, `24`, "hours", 12, -1, false),
		definition("crawl.quiet_hours", KindQuietHours, `{"enabled":true,"start":"01:00","end":"07:30"}`, "", -1, -1, false),
		definition("crawl.global_pages", KindInteger, `1`, "count", 1, 2, false),
		definition("crawl.verify_hold_minutes", KindInteger, `30`, "minutes", 10, 60, false),
		definition("crawl.verify_cooldown_base_minutes", KindInteger, `120`, "minutes", -1, -1, false),
		definition("crawl.verify_cooldown_max_minutes", KindInteger, `1440`, "minutes", -1, -1, false),
		definition("crawl.verify_cooldown_reset_hours", KindInteger, `24`, "hours", -1, -1, false),
		definition("crawl.deny_pause_hours", KindInteger, `24`, "hours", 24, -1, false),
		definition("crawl.failure_threshold", KindInteger, `3`, "count", -1, -1, false),
		definition("crawl.failure_pause_minutes", KindInteger, `30`, "minutes", -1, -1, false),
		definition("crawl.run_timeout_minutes", KindInteger, `15`, "minutes", -1, -1, true),
		definition("ai.max_items_per_run", KindInteger, `10`, "count", 5, 20, false),
		definition("ai.image_count", KindInteger, `4`, "count", 1, 4, false),
		definition("ai.max_concurrent_requests", KindInteger, `2`, "count", -1, -1, false),
		definition("ai.request_timeout_seconds", KindInteger, `90`, "seconds", -1, -1, false),
		definition("ai.proxy_id", KindNullableID, `null`, "", -1, -1, false),
		definition("ai.daily_token_limit", KindInteger, `0`, "tokens", -1, -1, false),
		definition("notify.link_target", KindString, `"mobile"`, "", -1, -1, false, `"mobile"`, `"desktop"`),
		definition("notify.max_per_run", KindInteger, `5`, "count", -1, -1, false),
		definition("notify.system_channel_ids", KindIDs, `[]`, "", -1, -1, false),
		definition("general.language", KindString, `"system"`, "", -1, -1, false, `"system"`, `"zh-CN"`, `"en"`),
		definition("general.appearance", KindString, `"system"`, "", -1, -1, false, `"system"`, `"light"`, `"dark"`),
		definition("general.timezone", KindString, string(timezone), "", -1, -1, false),
		definition("general.public_url", KindString, `""`, "", -1, -1, false),
		definition("general.update_check", KindBoolean, `true`, "", -1, -1, false),
		definition("general.onboarding_done", KindBoolean, `false`, "", -1, -1, false),
		definition("security.trusted_proxies", KindStrings, `[]`, "", -1, -1, false),
		definition("retention.findings_days", KindInteger, `30`, "days", -1, -1, false, `0`, `7`, `30`, `90`),
		definition("retention.run_steps_days", KindInteger, `14`, "days", -1, -1, false),
		definition("retention.stats_days", KindInteger, `31`, "days", -1, -1, false),
		definition("retention.seller_profile_days", KindInteger, `30`, "days", -1, -1, false),
		definition("retention.orphan_listing_days", KindInteger, `30`, "days", -1, -1, false),
		definition("cache.image_max_mb", KindInteger, `500`, "mb", -1, -1, false),
		definition("cache.browser_cache_mb", KindInteger, `256`, "mb", -1, -1, false),
		definition("cleanup.interval_hours", KindInteger, `24`, "hours", -1, -1, false),
	}
}
