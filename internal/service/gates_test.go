package service

import (
	"reflect"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/quota"
)

func gateAt(value string) time.Time {
	now, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return now
}

func gateClock(t *testing.T, now time.Time, zone string) quota.ClockState {
	t.Helper()
	state, _, err := quota.AdvanceClock(quota.ClockState{}, now, zone)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestRequiredEntryVisitsCountsOnlyRequiredActions(t *testing.T) {
	text := func(value string) *string { return &value }
	price := int64(0)
	for _, test := range []struct {
		name string
		task TaskFields
		want int64
	}{
		{"首页搜索和最新", TaskFields{}, 3},
		{"两个开关", TaskFields{PersonalOnly: true, FreeShippingOnly: true}, 5},
		{"省", TaskFields{RegionProvince: text("广东省")}, 6},
		{"省市区", TaskFields{RegionProvince: text("广东省"), RegionCity: text("深圳市"), RegionDistrict: text("南山区")}, 8},
		{"零价格也要提交", TaskFields{MinPrice: &price}, 4},
		{"翻页不算入口", TaskFields{MaxPages: 200}, 3},
		{"完整条件", TaskFields{PersonalOnly: true, FreeShippingOnly: true, RegionProvince: text("广东省"), RegionCity: text("深圳市"), RegionDistrict: text("南山区"), MaxPrice: &price}, 11},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := requiredEntryVisits(test.task); got != test.want {
				t.Fatalf("入口访问数 = %d，期望 %d", got, test.want)
			}
		})
	}
}

func TestQuotaReadyAtWaitsForEnoughHourlySlots(t *testing.T) {
	now := gateAt("2026-11-10T10:00:00Z")
	state := gateClock(t, now, "UTC")
	visits := []quota.Visit{
		{At: gateAt("2026-11-10T09:50:00Z"), Kind: quota.Detail},
		{At: gateAt("2026-11-10T09:10:00Z"), Kind: quota.Search},
		{At: gateAt("2026-11-10T09:40:00Z"), Kind: quota.Search},
		{At: gateAt("2026-11-10T09:20:00Z"), Kind: quota.Detail},
		{At: gateAt("2026-11-10T09:30:00Z"), Kind: quota.Search},
	}
	before := append([]quota.Visit(nil), visits...)
	limits := quota.Limits{Hourly: 5, DailySearch: 20, DailyDetail: 20}
	reason, next, err := quotaReadyAt(visits, state, 4, quota.Search, limits)
	want := gateAt("2026-11-10T10:40:00Z").UnixMilli()
	if err != nil || reason == nil || reason.Code != "run.hourly_quota" || next == nil || *next != want {
		t.Fatalf("只放出最早一条就开跑：%+v %v %v", reason, next, err)
	}
	if !reflect.DeepEqual(before, visits) {
		t.Fatal("计算修改了输入记录")
	}
	later, _, err := quota.AdvanceClock(state, time.UnixMilli(want), "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if reason, next, err := quotaReadyAt(visits, later, 4, quota.Search, limits); err != nil || reason != nil || next != nil {
		t.Fatalf("精确到边界后仍未释放：%+v %v %v", reason, next, err)
	}
}

func TestQuotaCapacityDoesNotInventRecoveryTime(t *testing.T) {
	state := gateClock(t, gateAt("2026-11-10T10:00:00Z"), "UTC")
	for _, limits := range []quota.Limits{
		{Hourly: 2, DailySearch: 10, DailyDetail: 10},
		{Hourly: 10, DailySearch: 2, DailyDetail: 10},
	} {
		reason, next, err := quotaReadyAt(nil, state, 3, quota.Search, limits)
		if err != nil || reason == nil || reason.Code != "run.quota_capacity" || next != nil {
			t.Fatalf("容量永远不足却给了恢复时间：%+v %v %v", reason, next, err)
		}
	}
}

func TestQuotaReadyAtUsesAllProtectedDayWindows(t *testing.T) {
	for _, test := range []struct {
		name, now, original, current, visit, want string
	}{
		{"往东改", "2026-11-10T20:00:00Z", "UTC", "Asia/Shanghai", "2026-11-10T01:00:00Z", "2026-11-11T00:00:00Z"},
		{"往西改跨新午夜", "2026-11-11T00:30:00Z", "Asia/Shanghai", "UTC", "2026-11-10T18:00:00Z", "2026-11-11T16:00:00Z"},
		{"午夜跳时", "2026-09-07T02:30:00Z", "America/Santiago", "UTC", "2026-09-06T05:00:00Z", "2026-09-07T03:00:00Z"},
		{"二十五小时日", "2026-11-02T01:00:00Z", "America/New_York", "UTC", "2026-11-01T05:00:00Z", "2026-11-02T05:00:00Z"},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := gateAt(test.now)
			initial := now
			if test.original == "America/Santiago" {
				initial = gateAt("2026-09-06T12:00:00Z")
			}
			if test.original == "America/New_York" {
				initial = gateAt("2026-11-01T12:00:00Z")
			}
			state := gateClock(t, initial, test.original)
			state, _, err := quota.AdvanceClock(state, initial, test.current)
			if err == nil {
				state, _, err = quota.AdvanceClock(state, now, test.current)
			}
			if err != nil {
				t.Fatal(err)
			}
			visits := []quota.Visit{{At: gateAt(test.visit), Kind: quota.Search}}
			reason, next, err := quotaReadyAt(visits, state, 1, quota.Search, quota.Limits{Hourly: 30, DailySearch: 1, DailyDetail: 1})
			want := gateAt(test.want).UnixMilli()
			if err != nil || reason == nil || reason.Code != "run.daily_search_quota" || next == nil || *next != want {
				t.Fatalf("旧日保护提前释放：%+v %v %v，期望 %d", reason, next, err, want)
			}
		})
	}
}

func TestQuotaReadyAtKeepsDetailSeparateAndRejectsBadFacts(t *testing.T) {
	now := gateAt("2026-11-10T10:00:00Z")
	state := gateClock(t, now, "UTC")
	limits := quota.Limits{Hourly: 30, DailySearch: 1, DailyDetail: 1}
	visits := []quota.Visit{{At: now.Add(-2 * time.Hour), Kind: quota.Detail}}
	if reason, _, err := quotaReadyAt(visits, state, 1, quota.Search, limits); err != nil || reason != nil {
		t.Fatalf("详情耗尽阻止搜索：%+v %v", reason, err)
	}
	if reason, next, err := quotaReadyAt(visits, state, 1, quota.Detail, limits); err != nil || reason == nil || reason.Code != "run.daily_detail_quota" || next == nil {
		t.Fatalf("详情日限未独立处理：%+v %v %v", reason, next, err)
	}
	for _, bad := range []struct {
		visits []quota.Visit
		state  quota.ClockState
		need   int64
		kind   quota.Kind
		limits quota.Limits
	}{
		{visits, state, 0, quota.Search, limits},
		{visits, state, 1, "other", limits},
		{visits, quota.ClockState{}, 1, quota.Search, limits},
		{visits, state, 1, quota.Search, quota.Limits{}},
		{[]quota.Visit{{At: now.Add(time.Hour), Kind: quota.Search}}, state, 1, quota.Search, limits},
	} {
		if _, _, err := quotaReadyAt(bad.visits, bad.state, bad.need, bad.kind, bad.limits); err == nil {
			t.Fatal("无效额度事实被放行")
		}
	}
}

func TestQuotaReadyAtRetainsEveryUnfinishedOldDay(t *testing.T) {
	now := gateAt("2026-11-10T20:00:00Z")
	state := gateClock(t, now, "UTC")
	for _, zone := range []string{"Asia/Shanghai", "America/Los_Angeles", "Pacific/Auckland"} {
		var err error
		state, _, err = quota.AdvanceClock(state, now, zone)
		if err != nil {
			t.Fatal(err)
		}
	}
	visits := []quota.Visit{
		{At: gateAt("2026-11-10T01:00:00Z"), Kind: quota.Search},
		{At: gateAt("2026-11-10T09:00:00Z"), Kind: quota.Search},
		{At: gateAt("2026-11-10T18:00:00Z"), Kind: quota.Search},
	}
	reason, next, err := quotaReadyAt(visits, state, 2, quota.Search, quota.Limits{Hourly: 30, DailySearch: 2, DailyDetail: 10})
	want := gateAt("2026-11-11T16:00:00Z").UnixMilli()
	if err != nil || reason == nil || next == nil || *next != want || len(state.Windows) != 3 {
		t.Fatalf("多次修改覆盖旧窗口或提前恢复：%+v %v %v", reason, next, err)
	}
}
