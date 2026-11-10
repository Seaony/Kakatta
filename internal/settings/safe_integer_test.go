package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/limits"
)

func TestBusinessIntegerLimitsAcceptExactMaximumAndRejectOneMoreAtomically(t *testing.T) {
	current := defaults(t)
	maximum := fmt.Sprint(limits.MaxSafeInteger)
	for _, key := range []string{
		"crawl.max_pages", "crawl.account_hourly_visits", "ai.image_count",
		"ai.daily_token_limit", "ai.proxy_id",
	} {
		t.Run(key, func(t *testing.T) {
			accepted, err := Apply(current, values(key, maximum))
			if err != nil || string(accepted.Values[key]) != maximum {
				t.Fatalf("真实整数上限被拒绝或改写：%s，%v", accepted.Values[key], err)
			}
			if key == "ai.image_count" || key == "crawl.account_hourly_visits" || key == "crawl.max_pages" {
				warned := false
				for _, advice := range accepted.Warnings {
					warned = warned || advice.Key == key && advice.Reason.Code == "settings.outside_recommendation"
				}
				if !warned {
					t.Fatal("推荐范围外合法整数没有警告")
				}
			}
			before, _ := json.Marshal(current)
			for _, invalid := range []int64{limits.MaxSafeInteger + 1, limits.MinSafeInteger - 1} {
				patch := values(key, fmt.Sprint(invalid))
				patch["general.language"] = json.RawMessage(`"en"`)
				got, err := Apply(current, patch)
				requireIssue(t, err, key, "overflow")
				if got.Values != nil || got.Sources != nil || got.Warnings != nil {
					t.Fatal("不安全整数导致部分新设置被返回")
				}
				after, _ := json.Marshal(current)
				if !bytes.Equal(before, after) || string(patch[key]) != fmt.Sprint(invalid) {
					t.Fatal("拒绝输入时改写了原设置或手输原文")
				}
				if _, err := Resolve(values(key, fmt.Sprint(invalid)), Environment{}); err == nil {
					t.Fatal("已有存储中的不安全整数被静默接受")
				}
			}
		})
	}
}

func TestNullableAndListIDsKeepTheirOriginalMeaningWithinSafeRange(t *testing.T) {
	current := defaults(t)
	for _, raw := range []string{"null", "1", fmt.Sprint(limits.MaxSafeInteger)} {
		if got, err := Apply(current, values("ai.proxy_id", raw)); err != nil || string(got.Values["ai.proxy_id"]) != raw {
			t.Fatalf("合法可空编号被改写：%s，%v", raw, err)
		}
	}
	for _, raw := range []string{"[]", "[1]", fmt.Sprintf("[1,%d]", limits.MaxSafeInteger)} {
		if got, err := Apply(current, values("notify.system_channel_ids", raw)); err != nil || string(got.Values["notify.system_channel_ids"]) != raw {
			t.Fatalf("合法编号列表被拒绝或改写：%s，%v", raw, err)
		}
	}
	for _, invalid := range []int64{limits.MaxSafeInteger + 1, limits.MinSafeInteger - 1} {
		_, err := Apply(current, values("notify.system_channel_ids", fmt.Sprintf("[1,%d]", invalid)))
		requireIssue(t, err, "notify.system_channel_ids", "overflow")
	}
	for _, raw := range []string{"0", "-1"} {
		_, err := Apply(current, values("ai.proxy_id", raw))
		requireIssue(t, err, "ai.proxy_id", "positive_required")
		_, err = Apply(current, values("notify.system_channel_ids", "["+raw+"]"))
		requireIssue(t, err, "notify.system_channel_ids", "positive_id_required")
	}
}

func TestTaskIntegerInputsUseSafeRangeAndKeepStricterDurationBounds(t *testing.T) {
	current := defaults(t)
	maximum := fmt.Sprint(limits.MaxSafeInteger)
	got, advice, err := ResolveTask(current, 30, json.RawMessage(`{"max_details_per_run":`+maximum+`}`))
	if err != nil || string(got["max_details_per_run"]) != maximum || len(advice) != 1 || advice[0].Key != "max_details_per_run" {
		t.Fatalf("合法整数覆盖值被截断或没有推荐警告：%v，%v，%v", got, advice, err)
	}
	for _, invalid := range []int64{limits.MaxSafeInteger + 1, limits.MinSafeInteger - 1} {
		got, advice, err = ResolveTask(current, 30, json.RawMessage(fmt.Sprintf(`{"max_details_per_run":%d}`, invalid)))
		requireIssue(t, err, "max_details_per_run", "overflow")
		if got != nil || advice != nil {
			t.Fatal("无效覆盖返回了部分设置")
		}
		_, _, err = ResolveTask(current, invalid, json.RawMessage(`{}`))
		requireIssue(t, err, "interval_minutes", "overflow")
	}
	longestMinutes := int64(math.MaxInt64 / int64(time.Minute))
	if _, _, err := ResolveTask(current, longestMinutes, json.RawMessage(`{"interval_jitter_percent":0}`)); err != nil {
		t.Fatalf("更严格的分钟上限被额外缩小：%v", err)
	}
	for _, minutes := range []int64{longestMinutes + 1, limits.MaxSafeInteger} {
		_, _, err := ResolveTask(current, minutes, json.RawMessage(`{"interval_jitter_percent":0}`))
		requireIssue(t, err, "interval_minutes", "overflow")
	}
	for _, key := range []string{"cache.image_max_mb", "cache.browser_cache_mb"} {
		_, err := Apply(current, values(key, fmt.Sprint(math.MaxInt64/(1<<20)+1)))
		requireIssue(t, err, key, "overflow")
	}
	_, _, err = ResolveTask(current, 30, json.RawMessage(`{"run_timeout_minutes":`+fmt.Sprint(longestMinutes+1)+`}`))
	requireIssue(t, err, "run_timeout_minutes", "overflow")
	_, _, err = ResolveTask(current, longestMinutes, json.RawMessage(`{"interval_jitter_percent":1}`))
	requireIssue(t, err, "interval_jitter_percent", "overflow")
}

func TestDecimalWaitRangesRemainDecimal(t *testing.T) {
	current := defaults(t)
	raw := json.RawMessage(`{"min":0.125,"max":0.5}`)
	next, err := Apply(current, Values{"crawl.page_wait_seconds": raw})
	if err != nil || !bytes.Equal(next.Values["crawl.page_wait_seconds"], raw) {
		t.Fatalf("小数等待范围被当整数拒绝或改写：%s，%v", next.Values["crawl.page_wait_seconds"], err)
	}
	got, _, err := ResolveTask(current, 30, json.RawMessage(`{"page_wait_seconds":`+string(raw)+`}`))
	if err != nil || !bytes.Equal(got["page_wait_seconds"], raw) {
		t.Fatalf("任务小数等待范围被改写：%s，%v", got["page_wait_seconds"], err)
	}
}
