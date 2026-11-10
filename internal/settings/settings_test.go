package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
)

func defaults(t *testing.T) Snapshot {
	t.Helper()
	s, err := Resolve(nil, Environment{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func values(key, raw string) Values { return Values{key: json.RawMessage(raw)} }

func requireIssue(t *testing.T, err error, key, code string) {
	t.Helper()
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("没有返回字段错误：%v", err)
	}
	for _, i := range invalid.Issues {
		for _, lang := range []locales.Language{locales.ZhCN, locales.En} {
			if _, err := locales.Format(lang, i.Reason); err != nil {
				t.Fatalf("校验原因缺少翻译：%+v，%v", i, err)
			}
		}
		if i.Key == key && i.Reason.Code == "settings."+code {
			return
		}
	}
	t.Fatalf("没找到%s/%s：%+v", key, code, invalid.Issues)
}

func TestDefaultsMatchAllDocumentedSettings(t *testing.T) {
	const documented = `{
"crawl.task_interval_minutes":30,"crawl.interval_jitter_percent":30,
"crawl.account_min_gap_seconds":180,"crawl.account_min_gap_jitter_seconds":60,
"crawl.account_hourly_visits":30,"crawl.account_daily_search_visits":200,"crawl.account_daily_detail_visits":100,
"crawl.max_pages":1,"crawl.page_wait_seconds":{"min":10,"max":15},"crawl.max_details_per_run":5,
"crawl.detail_pre_wait_seconds":{"min":3,"max":6},"crawl.detail_stay_seconds":{"min":10,"max":20},
"crawl.action_wait_seconds":{"min":1,"max":3},"crawl.home_stay_seconds":{"min":2,"max":5},
"crawl.typing_interval_ms":{"min":50,"max":130},"crawl.seller_cache_hours":24,
"crawl.quiet_hours":{"enabled":true,"start":"01:00","end":"07:30"},"crawl.global_pages":1,
"crawl.verify_hold_minutes":30,"crawl.verify_cooldown_base_minutes":120,"crawl.verify_cooldown_max_minutes":1440,
"crawl.verify_cooldown_reset_hours":24,"crawl.deny_pause_hours":24,"crawl.failure_threshold":3,
"crawl.failure_pause_minutes":30,"crawl.run_timeout_minutes":15,
"ai.max_items_per_run":10,"ai.image_count":4,"ai.max_concurrent_requests":2,"ai.request_timeout_seconds":90,"ai.proxy_id":null,"ai.daily_token_limit":0,
"notify.link_target":"mobile","notify.max_per_run":5,"notify.system_channel_ids":[],
"general.language":"system","general.appearance":"system","general.timezone":"Asia/Shanghai",
"general.public_url":"","general.update_check":true,"general.onboarding_done":false,
"security.trusted_proxies":[],"retention.findings_days":30,"retention.run_steps_days":14,"retention.stats_days":31,
"retention.seller_profile_days":30,"retention.orphan_listing_days":30,"cache.image_max_mb":500,"cache.browser_cache_mb":256,"cleanup.interval_hours":24}`
	var want Values
	if err := json.Unmarshal([]byte(documented), &want); err != nil {
		t.Fatal(err)
	}
	s := defaults(t)
	if len(want) != 50 || len(s.Values) != 50 || len(s.Sources) != 50 || len(s.Warnings) != 0 {
		t.Fatalf("设置不完整或默认值报警：%d/%d/%d，%+v", len(want), len(s.Values), len(s.Sources), s.Warnings)
	}
	for key, raw := range want {
		if !bytes.Equal(raw, s.Values[key]) || s.Sources[key] != SourceDefault {
			t.Errorf("默认值%s=%s，期望%s", key, s.Values[key], raw)
		}
	}
	defs := Definitions(Environment{})
	if len(defs) != 50 {
		t.Fatal("元数据数量不符")
	}
	for _, d := range defs {
		if !bytes.Equal(d.Default, want[d.Key]) || d.Group != strings.SplitN(d.Key, ".", 2)[0] {
			t.Errorf("元数据默认值或分组不符：%s", d.Key)
		}
		for _, m := range []locales.Message{d.Label, d.Why, d.Risk, d.Recommendation.Text} {
			for _, lang := range []locales.Language{locales.ZhCN, locales.En} {
				if text, err := locales.Format(lang, m); err != nil || text == "" {
					t.Errorf("元数据文案缺失：%s/%s，%v", d.Key, lang, err)
				}
			}
		}
		if d.Unit != "" {
			if _, err := locales.Format(locales.En, locales.Message{Code: "units." + d.Unit, Args: map[string]any{"count": 2}}); err != nil {
				t.Error(err)
			}
		}
	}
}

func TestValidationRejectsInvalidValuesAtomically(t *testing.T) {
	s := defaults(t)
	for _, tc := range []struct{ key, raw, code string }{
		{"unknown", `1`, "unknown_key"},
		{"crawl.max_pages", `0`, "positive_required"}, {"crawl.max_pages", `-1`, "positive_required"},
		{"crawl.max_pages", `1.5`, "integer_required"}, {"crawl.max_pages", `"2"`, "integer_required"},
		{"crawl.max_pages", `9223372036854775808`, "overflow"}, {"crawl.max_pages", `null`, "null_forbidden"},
		{"crawl.max_pages", `2 3`, "invalid_json"},
		{"crawl.account_min_gap_seconds", `1`, "minimum"},
		{"crawl.interval_jitter_percent", `-1`, "nonnegative_required"},
		{"general.update_check", `1`, "boolean_required"}, {"general.update_check", `"false"`, "boolean_required"},
		{"general.language", `"fr"`, "invalid_option"}, {"general.language", `true`, "string_required"},
		{"general.appearance", `"auto"`, "invalid_option"},
		{"notify.link_target", `"app"`, "invalid_option"},
		{"ai.proxy_id", `0`, "positive_required"}, {"ai.proxy_id", `""`, "integer_required"},
		{"ai.image_count", `0`, "positive_required"},
		{"retention.findings_days", `10`, "invalid_option"},
		{"crawl.page_wait_seconds", `{"min":15,"max":10}`, "invalid_range"},
		{"crawl.page_wait_seconds", `{"min":0,"max":10}`, "invalid_range"},
		{"crawl.page_wait_seconds", `{"min":-1,"max":10}`, "invalid_range"},
		{"crawl.page_wait_seconds", `{"min":1,"max":null}`, "invalid_range"},
		{"crawl.page_wait_seconds", `{"min":1}`, "missing_field"},
		{"crawl.page_wait_seconds", `{"min":1,"max":2,"other":3}`, "unknown_field"},
		{"crawl.page_wait_seconds", `{"min":1,"min":2,"max":3}`, "duplicate_field"},
		{"crawl.page_wait_seconds", `{"min":"1","max":2}`, "range_required"},
		{"crawl.page_wait_seconds", `{"min":1e309,"max":1e309}`, "range_required"},
		{"crawl.page_wait_seconds", `{"min":NaN,"max":2}`, "invalid_json"},
		{"crawl.page_wait_seconds", `{"min":1e-10,"max":2}`, "overflow"},
		{"crawl.typing_interval_ms", `{"min":1e-7,"max":2}`, "overflow"},
		{"crawl.quiet_hours", `{"enabled":true,"start":"1:00","end":"07:30"}`, "invalid_clock"},
		{"crawl.quiet_hours", `{"enabled":true,"start":"01:0","end":"07:30"}`, "invalid_clock"},
		{"crawl.quiet_hours", `{"enabled":true,"start":"24:00","end":"07:30"}`, "invalid_clock"},
		{"crawl.quiet_hours", `{"enabled":true,"start":"01:00:00","end":"07:30"}`, "invalid_clock"},
		{"crawl.quiet_hours", `{"enabled":true,"start":null,"end":"07:30"}`, "invalid_clock"},
		{"crawl.quiet_hours", `{"start":"01:00","end":"07:30"}`, "missing_field"},
		{"crawl.quiet_hours", `{"enabled":null,"start":"01:00","end":"07:30"}`, "boolean_required"},
		{"general.timezone", `"Local"`, "invalid_timezone"}, {"general.timezone", `""`, "invalid_timezone"},
		{"general.timezone", `"Not/A_Zone"`, "invalid_timezone"},
		{"general.public_url", `"/relative"`, "invalid_url"}, {"general.public_url", `"ftp://host/"`, "invalid_url"},
		{"general.public_url", `"https://user:pass@host/"`, "invalid_url"},
		{"general.public_url", `"https://host/?token=value"`, "invalid_url"},
		{"general.public_url", `"https://host/#"`, "invalid_url"},
		{"general.public_url", `"https://host:65536/"`, "invalid_url"},
		{"notify.system_channel_ids", `[1,1]`, "duplicate_id"}, {"notify.system_channel_ids", `[0]`, "positive_id_required"},
		{"notify.system_channel_ids", `["1"]`, "positive_id_required"}, {"notify.system_channel_ids", `{}`, "list_required"},
		{"security.trusted_proxies", `["example.com"]`, "invalid_proxy_address"},
		{"security.trusted_proxies", `["127.0.0.1:8080"]`, "invalid_proxy_address"},
		{"security.trusted_proxies", `["fe80::1%eth0"]`, "invalid_proxy_address"},
		{"security.trusted_proxies", `["127.0.0.1","127.0.0.1/32"]`, "duplicate_address"},
	} {
		t.Run(tc.key+"/"+tc.raw, func(t *testing.T) {
			before, _ := json.Marshal(s)
			patch := values(tc.key, tc.raw)
			patch["general.onboarding_done"] = json.RawMessage(`true`)
			after, err := Apply(s, patch)
			requireIssue(t, err, tc.key, tc.code)
			if after.Values != nil || after.Sources != nil || after.Warnings != nil {
				t.Fatal("错误后返回了部分新设置")
			}
			unchanged, _ := json.Marshal(s)
			if !bytes.Equal(before, unchanged) {
				t.Fatal("校验失败修改了原快照")
			}
		})
	}
}

func TestAllowedZerosAndValuesOutsideRecommendation(t *testing.T) {
	s := defaults(t)
	patch := Values{
		"crawl.interval_jitter_percent": json.RawMessage(`0`), "crawl.account_min_gap_jitter_seconds": json.RawMessage(`0`),
		"ai.daily_token_limit": json.RawMessage(`0`), "retention.findings_days": json.RawMessage(`0`),
		"ai.proxy_id": json.RawMessage(`null`), "notify.system_channel_ids": json.RawMessage(`[]`),
		"notify.max_per_run": json.RawMessage(`1`), "ai.image_count": json.RawMessage(`5`),
		"crawl.task_interval_minutes": json.RawMessage(`1`), "crawl.account_min_gap_seconds": json.RawMessage(`2`),
		"crawl.account_hourly_visits": json.RawMessage(`100`), "retention.stats_days": json.RawMessage(`7`),
		"crawl.detail_stay_seconds": json.RawMessage(`{"min":0.5,"max":0.5}`),
		"general.language":          json.RawMessage(`"syst\u0065m"`),
		"general.public_url":        json.RawMessage(`"https://example.test/kakatta"`),
	}
	next, err := Apply(s, patch)
	if err != nil {
		t.Fatal(err)
	}
	for key, raw := range patch {
		if !bytes.Equal(next.Values[key], raw) || next.Sources[key] != SourceStored {
			t.Errorf("设置被截断、替换或来源错误：%s=%s", key, next.Values[key])
		}
	}
	warned := map[string]bool{}
	for _, i := range next.Warnings {
		warned[i.Key] = true
		for _, lang := range []locales.Language{locales.ZhCN, locales.En} {
			if _, err := locales.Format(lang, i.Reason); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, key := range []string{"ai.image_count", "crawl.task_interval_minutes", "crawl.account_min_gap_seconds", "crawl.account_hourly_visits", "retention.stats_days", "crawl.detail_stay_seconds"} {
		if !warned[key] {
			t.Errorf("推荐范围以外缺少警告：%s", key)
		}
	}
	for _, raw := range []string{`{"enabled":true,"start":"23:00","end":"07:30"}`, `{"enabled":true,"start":"07:30","end":"07:30"}`, `{"enabled":false,"start":"01:00","end":"07:30"}`} {
		if _, err := Apply(s, values("crawl.quiet_hours", raw)); err != nil {
			t.Errorf("跨午夜或明确全天运行应可保存：%v", err)
		}
	}
}

func TestEveryIntegerUnitAndCombinedWaitChecksOverflow(t *testing.T) {
	s := defaults(t)
	units := map[int64][]string{
		int64(time.Second):    {"crawl.account_min_gap_seconds", "crawl.account_min_gap_jitter_seconds", "ai.request_timeout_seconds"},
		int64(time.Minute):    {"crawl.task_interval_minutes", "crawl.verify_hold_minutes", "crawl.verify_cooldown_base_minutes", "crawl.verify_cooldown_max_minutes", "crawl.failure_pause_minutes", "crawl.run_timeout_minutes"},
		int64(time.Hour):      {"crawl.seller_cache_hours", "crawl.verify_cooldown_reset_hours", "crawl.deny_pause_hours", "cleanup.interval_hours"},
		int64(24 * time.Hour): {"retention.findings_days", "retention.run_steps_days", "retention.stats_days", "retention.seller_profile_days", "retention.orphan_listing_days"},
		1 << 20:               {"cache.image_max_mb", "cache.browser_cache_mb"},
	}
	for multiplier, keys := range units {
		for _, key := range keys {
			_, err := Apply(s, values(key, fmt.Sprint(math.MaxInt64/multiplier+1)))
			requireIssue(t, err, key, "overflow")
		}
	}
	for _, key := range []string{"crawl.page_wait_seconds", "crawl.detail_pre_wait_seconds", "crawl.detail_stay_seconds", "crawl.action_wait_seconds", "crawl.home_stay_seconds", "crawl.typing_interval_ms"} {
		_, err := Apply(s, values(key, `{"min":1,"max":1e20}`))
		requireIssue(t, err, key, "overflow")
	}
	_, err := Apply(s, values("crawl.account_min_gap_jitter_seconds", fmt.Sprint(math.MaxInt64/int64(time.Second))))
	requireIssue(t, err, "crawl.account_min_gap_jitter_seconds", "overflow")
	_, err = Apply(s, values("crawl.interval_jitter_percent", fmt.Sprint(math.MaxInt64)))
	requireIssue(t, err, "crawl.interval_jitter_percent", "overflow")
	_, err = Apply(s, values("crawl.verify_cooldown_base_minutes", `1441`))
	requireIssue(t, err, "crawl.verify_cooldown_base_minutes", "cooldown_order")
	boundary := Values{"crawl.task_interval_minutes": json.RawMessage(fmt.Sprint(math.MaxInt64 / int64(time.Minute))), "crawl.interval_jitter_percent": json.RawMessage(`0`)}
	if _, err := Apply(s, boundary); err != nil {
		t.Fatalf("可表示的最长整分钟不应被拦：%v", err)
	}
}

func TestEnvironmentPrecedenceAndReadOnly(t *testing.T) {
	stored := values("general.timezone", `"Europe/London"`)
	stored["security.trusted_proxies"] = json.RawMessage(`["10.0.0.1"]`)
	env := " 127.0.0.1 , 2001:db8::/32 "
	s, err := Resolve(stored, Environment{TZ: "UTC", TrustedProxies: &env})
	if err != nil {
		t.Fatal(err)
	}
	if string(s.Values["general.timezone"]) != `"Europe/London"` || s.Sources["general.timezone"] != SourceStored || s.Sources["security.trusted_proxies"] != SourceEnvironment {
		t.Fatal("环境覆盖优先级不符")
	}
	if string(s.Values["security.trusted_proxies"]) != `["127.0.0.1","2001:db8::/32"]` {
		t.Fatalf("环境列表未正确解析：%s", s.Values["security.trusted_proxies"])
	}
	_, err = Apply(s, values("security.trusted_proxies", string(s.Values["security.trusted_proxies"])))
	requireIssue(t, err, "security.trusted_proxies", "read_only")
	for _, empty := range []string{"", "  "} {
		s, err := Resolve(stored, Environment{TrustedProxies: &empty})
		if err != nil || string(s.Values["security.trusted_proxies"]) != `[]` || s.Sources["security.trusted_proxies"] != SourceEnvironment {
			t.Fatal("明确空环境值没有清空并锁定代理列表")
		}
	}
	for _, malformed := range []string{"127.0.0.1,", "example.com", "127.0.0.1,,10.0.0.1"} {
		_, err := Resolve(stored, Environment{TrustedProxies: &malformed})
		requireIssue(t, err, "security.trusted_proxies", "invalid_proxy_address")
	}
	without, err := Resolve(stored, Environment{})
	if err != nil || !bytes.Equal(without.Values["security.trusted_proxies"], stored["security.trusted_proxies"]) {
		t.Fatal("移除环境覆盖后未恢复原存储值")
	}
	defaultTZ, err := Resolve(nil, Environment{TZ: "UTC"})
	if err != nil || string(defaultTZ.Values["general.timezone"]) != `"UTC"` || defaultTZ.Sources["general.timezone"] != SourceDefault {
		t.Fatal("TZ不应锁定时区")
	}
	if _, err := Apply(defaultTZ, values("general.timezone", `"Asia/Tokyo"`)); err != nil {
		t.Fatal(err)
	}
	_, err = Resolve(nil, Environment{TZ: "Not/A_Zone"})
	requireIssue(t, err, "general.timezone", "invalid_timezone")
	if _, err := Resolve(stored, Environment{TZ: "Not/A_Zone"}); err != nil {
		t.Fatal("存储时区优先于未使用的默认TZ")
	}
	_, err = Resolve(values("security.trusted_proxies", `["bad"]`), Environment{TrustedProxies: &env})
	requireIssue(t, err, "security.trusted_proxies", "invalid_proxy_address")
}

func TestTaskOverridesAreExactlyTheDocumentedNine(t *testing.T) {
	s := defaults(t)
	want := []string{"interval_jitter_percent", "page_wait_seconds", "max_details_per_run", "detail_pre_wait_seconds", "detail_stay_seconds", "action_wait_seconds", "home_stay_seconds", "typing_interval_ms", "run_timeout_minutes"}
	resolved, warnings, err := ResolveTask(s, 30, json.RawMessage(`{}`))
	if err != nil || len(resolved) != 9 || len(warnings) != 0 {
		t.Fatalf("空覆盖没有完整继承9项：%+v，%v", resolved, err)
	}
	for _, key := range want {
		if !bytes.Equal(resolved[key], s.Values["crawl."+key]) {
			t.Errorf("没有继承：%s", key)
		}
	}
	var actual []string
	for _, d := range Definitions(Environment{}) {
		if d.TaskOverride {
			actual = append(actual, strings.TrimPrefix(d.Key, "crawl."))
		}
	}
	slices.Sort(want)
	slices.Sort(actual)
	if !reflect.DeepEqual(want, actual) {
		t.Fatalf("白名单不同：%v", actual)
	}
	full := `{"interval_jitter_percent":0,"page_wait_seconds":{"min":11,"max":12},"max_details_per_run":12,"detail_pre_wait_seconds":{"min":4,"max":5},"detail_stay_seconds":{"min":12,"max":13},"action_wait_seconds":{"min":2,"max":2},"home_stay_seconds":{"min":3,"max":3},"typing_interval_ms":{"min":70,"max":80},"run_timeout_minutes":20}`
	next, advice, err := ResolveTask(s, 30, json.RawMessage(full))
	if err != nil || len(next) != 9 || len(advice) != 1 || advice[0].Key != "max_details_per_run" || string(next["max_details_per_run"]) != `12` {
		t.Fatalf("全覆盖或推荐警告不符：%+v，%+v，%v", next, advice, err)
	}
	for _, key := range []string{"account_min_gap_seconds", "account_hourly_visits", "global_pages", "verify_cooldown_base_minutes", "task_interval_minutes", "max_pages", "crawl.page_wait_seconds", "unknown"} {
		_, _, err := ResolveTask(s, 30, json.RawMessage(fmt.Sprintf(`{%q:1}`, key)))
		requireIssue(t, err, key, "task_override_forbidden")
	}
	for _, raw := range []string{`null`, `[]`, `{`, `{} {}`, `{"run_timeout_minutes":1,"run_timeout_minutes":2}`, `{"run_timeout_minutes":null}`} {
		if next, warnings, err := ResolveTask(s, 30, json.RawMessage(raw)); err == nil || next != nil || warnings != nil {
			t.Errorf("非法覆盖被部分接受：%s，%v", raw, err)
		}
	}
	_, _, err = ResolveTask(s, 0, json.RawMessage(`{}`))
	requireIssue(t, err, "interval_minutes", "positive_required")
	_, _, err = ResolveTask(s, math.MaxInt64, json.RawMessage(`{}`))
	requireIssue(t, err, "interval_minutes", "overflow")
	_, _, err = ResolveTask(s, math.MaxInt64/int64(time.Minute), json.RawMessage(`{}`))
	requireIssue(t, err, "interval_jitter_percent", "overflow")
}

func TestReturnedSettingsAndMetadataAreIndependent(t *testing.T) {
	stored := values("general.timezone", `"UTC"`)
	s, err := Resolve(stored, Environment{})
	if err != nil {
		t.Fatal(err)
	}
	s.Values["general.timezone"][1] = 'X'
	if string(stored["general.timezone"]) != `"UTC"` {
		t.Fatal("返回快照共享了输入字节")
	}
	s = defaults(t)
	patch := values("ai.image_count", `5`)
	next, err := Apply(s, patch)
	if err != nil {
		t.Fatal(err)
	}
	next.Values["ai.image_count"][0] = '6'
	next.Values["general.language"][1] = 'X'
	next.Sources["general.language"] = SourceEnvironment
	if string(patch["ai.image_count"]) != `5` || string(s.Values["general.language"]) != `"system"` || s.Sources["general.language"] != SourceDefault {
		t.Fatal("新快照污染了输入或原快照")
	}
	defs := Definitions(Environment{})
	defs[0].Default[0] = '9'
	*defs[0].Recommendation.Min = 999
	for i := range defs {
		if len(defs[i].Options) != 0 {
			defs[i].Options[0][0] = '!'
		}
	}
	if _, err := Resolve(nil, Environment{}); err != nil || string(Definitions(Environment{})[0].Default) != `30` || *Definitions(Environment{})[0].Recommendation.Min != 30 {
		t.Fatal("修改元数据污染了默认值")
	}
}

func TestConcurrentSnapshotsDoNotShareMutableResults(t *testing.T) {
	s := defaults(t)
	var jobs sync.WaitGroup
	for range 16 {
		jobs.Go(func() {
			next, err := Apply(s, values("ai.image_count", `5`))
			if err != nil {
				t.Error(err)
				return
			}
			next.Values["general.language"][1] = 'X'
			next.Sources["general.language"] = SourceStored
			next.Warnings[0].Reason.Args["max"] = 99
			lang := locales.Select("system", "en-US")
			if _, err := locales.Format(lang, next.Warnings[0].Reason); err != nil {
				t.Error(err)
			}
			b, err := locales.JSON(lang)
			if err != nil {
				t.Error(err)
			} else {
				b[0] = '!'
			}
		})
	}
	jobs.Wait()
	if string(s.Values["general.language"]) != `"system"` || s.Sources["general.language"] != SourceDefault {
		t.Fatal("并发更新污染了共享的原快照")
	}
}
