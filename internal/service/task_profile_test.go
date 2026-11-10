package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/settings"
)

func taskProfileTestPtr[T any](value T) *T { return &value }

func taskProfileTestFields() TaskFields {
	return TaskFields{Name: "原任务", Keyword: "相机", Enabled: true, MaxPages: 1, IntervalMinutes: 30,
		PublishWindow: "any", JudgeMode: analysis.ModeKeyword, NotifyPolicy: "recommended",
		CrawlOverrides: settings.Values{}, ChannelIDs: []int64{1}}
}

func taskProfileTestGlobal(t *testing.T) settings.Snapshot {
	t.Helper()
	global, err := settings.Resolve(nil, settings.Environment{})
	if err != nil {
		t.Fatal(err)
	}
	return global
}

func taskProfileTestError(t *testing.T, err error, field, code string) {
	t.Helper()
	var failure *ServiceError
	if !errors.As(err, &failure) || failure.Kind != ErrorInvalid || failure.Reason.Code != "tasks.invalid" ||
		len(failure.Fields) == 0 || failure.Fields[0].Key != field || failure.Fields[0].Reason.Code != code {
		t.Fatalf("错误不匹配：%#v，期望 %s / %s", err, field, code)
	}
}

func TestApplyTaskPatchRetainsMissingAndExplicitValues(t *testing.T) {
	base := taskProfileTestFields()
	base.MinPrice = taskProfileTestPtr[int64](100)
	base.MatchKeywords = []string{"旧词"}
	got, err := applyTaskPatch(base, TaskPatch{"enabled": json.RawMessage(`false`), "min_price": json.RawMessage(`null`),
		"max_price": json.RawMessage(`0`), "match_keywords": json.RawMessage(`[]`), "channel_ids": json.RawMessage(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != base.Name || got.Keyword != base.Keyword || got.Enabled || got.MinPrice != nil || got.MaxPrice == nil ||
		*got.MaxPrice != 0 || got.MatchKeywords == nil || len(got.MatchKeywords) != 0 || got.ChannelIDs == nil || len(got.ChannelIDs) != 0 {
		t.Fatalf("省略、清空或零值被覆盖：%#v", got)
	}
	if *base.MinPrice != 100 || !reflect.DeepEqual(base.MatchKeywords, []string{"旧词"}) || !reflect.DeepEqual(base.ChannelIDs, []int64{1}) {
		t.Fatal("原任务被改动")
	}
}

func TestApplyTaskPatchRejectsInvalidJSONShapes(t *testing.T) {
	cases := []struct{ field, raw, code string }{
		{"id", `1`, "unknown_key"}, {"search_generation", `2`, "unknown_key"}, {"Name", `"越权"`, "unknown_key"},
		{"keyword", `null`, "null_forbidden"}, {"enabled", `null`, "null_forbidden"},
		{"profile", `null`, "null_forbidden"}, {"crawl_overrides", `null`, "null_forbidden"},
		{"channel_ids", `null`, "null_forbidden"}, {"match_keywords", `[null]`, "null_forbidden"},
		{"channel_ids", `[null]`, "null_forbidden"}, {"exclude_keywords", `[3]`, "string_required"},
		{"channel_ids", `["1"]`, "integer_required"}, {"max_pages", `1.5`, "integer_required"},
		{"max_pages", `9223372036854775808`, "overflow"}, {"enabled", `0`, "boolean_required"},
		{"account_id", `"1"`, "integer_required"}, {"region_city", `3`, "string_required"},
		{"profile", `{"musts":[null]}`, "null_forbidden"},
		{"profile", `{"specs":[{"name":"型号","options":[null]}]}`, "null_forbidden"},
		{"profile", `{"musts":[{"edited":null}]}`, "null_forbidden"},
		{"profile", `{"musts":[{"text":null}]}`, "null_forbidden"},
		{"profile", `{"musts":{},"next_no":1}`, "list_required"},
		{"profile", `{"allowed_kinds":[false]}`, "string_required"},
		{"profile", `{"unknown":1}`, "unknown_key"},
		{"profile", `{"next_no":1,"next_no":2}`, "duplicate_field"},
		{"crawl_overrides", `{"page_wait_seconds":{},"page_wait_seconds":{}}`, "duplicate_field"},
		{"keyword", `"相机" true`, "invalid_json"},
		{"keyword", string([]byte{'"', 0xff, '"'}), "invalid_json"},
	}
	for _, tc := range cases {
		t.Run(tc.field+"/"+tc.raw, func(t *testing.T) {
			_, err := applyTaskPatch(taskProfileTestFields(), TaskPatch{tc.field: json.RawMessage(tc.raw)})
			taskProfileTestError(t, err, tc.field, "settings."+tc.code)
		})
	}
}

func TestApplyTaskPatchFailureDoesNotMutateBase(t *testing.T) {
	base := taskProfileTestFields()
	base.AccountID = taskProfileTestPtr[int64](8)
	base.Profile = analysis.Profile{Specs: []analysis.ProfileSpec{{Name: "旧规格", Options: []string{"旧值"}, Values: []string{"旧值"}}}}
	before, _ := json.Marshal(base)
	_, err := applyTaskPatch(base, TaskPatch{"account_id": json.RawMessage(`9`), "channel_ids": json.RawMessage(`[2,3]`),
		"profile": json.RawMessage(`{"specs":[{"name":"新规格","options":["新值"],"values":[false]}]}`)})
	if err == nil {
		t.Fatal("错误的规格值被接受")
	}
	after, _ := json.Marshal(base)
	if string(before) != string(after) {
		t.Fatalf("失败污染原任务：%s → %s", before, after)
	}
}

func TestApplyTaskPatchEnforcesSafeIntegersAtEveryDepth(t *testing.T) {
	cases := []struct {
		field string
		wrap  string
	}{
		{"min_price", "%s"}, {"max_price", "%s"}, {"budget_min", "%s"}, {"budget_max", "%s"},
		{"account_id", "%s"}, {"max_pages", "%s"}, {"interval_minutes", "%s"}, {"channel_ids", "[%s]"},
		{"profile", `{"next_no":%s}`}, {"profile", `{"musts":[{"rev":%s}]}`},
		{"profile", `{"bonuses":[{"rev":%s}]}`}, {"profile", `{"risks":[{"rev":%s}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.field+tc.wrap, func(t *testing.T) {
			for _, value := range []int64{limits.MinSafeInteger, limits.MaxSafeInteger, limits.MinSafeInteger - 1, limits.MaxSafeInteger + 1} {
				raw := json.RawMessage(fmt.Sprintf(tc.wrap, fmt.Sprint(value)))
				_, err := applyTaskPatch(taskProfileTestFields(), TaskPatch{tc.field: raw})
				if value < limits.MinSafeInteger || value > limits.MaxSafeInteger {
					taskProfileTestError(t, err, tc.field, "settings.overflow")
				} else if err != nil {
					t.Fatalf("安全整数在解码前被拒绝：%d %v", value, err)
				}
			}
		})
	}
}

func TestNormalizeTaskFieldsEnforcesSafeIntegersWithoutJSONDecode(t *testing.T) {
	global := taskProfileTestGlobal(t)
	cases := []struct {
		field string
		set   func(*TaskFields, int64)
	}{
		{"min_price", func(f *TaskFields, n int64) { f.MinPrice = &n }},
		{"max_price", func(f *TaskFields, n int64) { f.MaxPrice = &n }},
		{"budget_min", func(f *TaskFields, n int64) { f.BudgetMin = &n }},
		{"budget_max", func(f *TaskFields, n int64) { f.BudgetMax = &n }},
		{"account_id", func(f *TaskFields, n int64) { f.AccountID = &n }},
		{"channel_ids", func(f *TaskFields, n int64) { f.ChannelIDs = []int64{n} }},
		{"max_pages", func(f *TaskFields, n int64) { f.MaxPages = n }},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			fields := taskProfileTestFields()
			tc.set(&fields, limits.MaxSafeInteger)
			if _, _, err := normalizeTaskFields(nil, fields, TaskPatch{}, global); err != nil {
				t.Fatalf("安全上限被拒绝：%v", err)
			}
			for _, value := range []int64{limits.MaxSafeInteger + 1, limits.MinSafeInteger - 1} {
				tc.set(&fields, value)
				_, _, err := normalizeTaskFields(nil, fields, TaskPatch{}, global)
				taskProfileTestError(t, err, tc.field, "settings.overflow")
			}
		})
	}
}

func TestNormalizeTaskFieldsNameAndKeywords(t *testing.T) {
	global := taskProfileTestGlobal(t)
	fields := taskProfileTestFields()
	fields.Keyword = "　" + strings.Repeat("机", 31) + "ＡＢＣ　"
	fields.MatchKeywords = []string{"  ＡＢＣ  ", "abc, 相  机", "相 机"}
	fields.ExcludeKeywords = []string{"坏机， ＡＢＣ", "abc"}
	got, _, err := normalizeTaskFields(nil, fields, TaskPatch{}, global)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != strings.Repeat("机", 30) || got.Keyword != strings.Repeat("机", 31)+"ＡＢＣ" ||
		!reflect.DeepEqual(got.MatchKeywords, []string{"abc", "相 机"}) || !reflect.DeepEqual(got.ExcludeKeywords, []string{"坏机", "abc"}) {
		t.Fatalf("名称或关键词整理错误：%#v", got)
	}
	old := Task{TaskFields: taskProfileTestFields()}
	fields.Name = old.Name
	got, _, err = normalizeTaskFields(&old, fields, TaskPatch{"keyword": json.RawMessage(`"新词"`)}, global)
	if err != nil || got.Name != old.Name {
		t.Fatalf("更新省略名称没有保留：%#v %v", got, err)
	}
	fields.Name = "　" + strings.Repeat("名", 31)
	_, _, err = normalizeTaskFields(nil, fields, TaskPatch{"name": json.RawMessage(`"显式名称"`)}, global)
	taskProfileTestError(t, err, "name", "tasks.text_length")
}

func TestNormalizeTaskChannelSelectionHasStableOrder(t *testing.T) {
	fields := taskProfileTestFields()
	fields.ChannelIDs = []int64{3, 1, 2}
	got, _, err := normalizeTaskFields(nil, fields, TaskPatch{}, taskProfileTestGlobal(t))
	if err != nil || !reflect.DeepEqual(got.ChannelIDs, []int64{1, 2, 3}) || !reflect.DeepEqual(fields.ChannelIDs, []int64{3, 1, 2}) {
		t.Fatalf("渠道集合顺序不稳定，或改到了调用方：%v %v", got.ChannelIDs, err)
	}
}

func TestNormalizeTaskIntervalOutsideRecommendationIsSavedWithWarning(t *testing.T) {
	for _, minutes := range []int64{1, 121} {
		fields := taskProfileTestFields()
		fields.IntervalMinutes = minutes
		got, warnings, err := normalizeTaskFields(nil, fields, TaskPatch{}, taskProfileTestGlobal(t))
		if err != nil || got.IntervalMinutes != minutes || len(warnings) != 1 || warnings[0].Key != "interval_minutes" || warnings[0].Reason.Code != "settings.outside_recommendation" {
			t.Fatalf("间隔超过推荐值被拒绝或没有警告：%+v %v %v", got, warnings, err)
		}
	}
}

func TestNormalizeTaskFieldsRejectsInvalidLimits(t *testing.T) {
	global := taskProfileTestGlobal(t)
	cases := []struct {
		name, field, code string
		change            func(*TaskFields)
	}{
		{"空搜索词", "keyword", "tasks.text_length", func(f *TaskFields) { f.Keyword = "　 " }},
		{"搜索词超长", "keyword", "tasks.text_length", func(f *TaskFields) { f.Keyword = strings.Repeat("机", 51) }},
		{"负价格", "min_price", "settings.nonnegative_required", func(f *TaskFields) { f.MinPrice = taskProfileTestPtr[int64](-1) }},
		{"价格倒置", "max_price", "settings.invalid_range", func(f *TaskFields) {
			f.MinPrice = taskProfileTestPtr[int64](1)
			f.MaxPrice = taskProfileTestPtr[int64](0)
		}},
		{"负预算", "budget_max", "settings.nonnegative_required", func(f *TaskFields) { f.BudgetMax = taskProfileTestPtr[int64](-1) }},
		{"预算倒置", "budget_max", "settings.invalid_range", func(f *TaskFields) {
			f.BudgetMin = taskProfileTestPtr[int64](1)
			f.BudgetMax = taskProfileTestPtr[int64](0)
		}},
		{"零页数", "max_pages", "settings.positive_required", func(f *TaskFields) { f.MaxPages = 0 }},
		{"零间隔", "interval_minutes", "settings.positive_required", func(f *TaskFields) { f.IntervalMinutes = 0 }},
		{"间隔溢出", "interval_minutes", "settings.overflow", func(f *TaskFields) { f.IntervalMinutes = math.MaxInt64 }},
		{"组合溢出", "crawl_overrides.interval_jitter_percent", "settings.overflow", func(f *TaskFields) {
			f.CrawlOverrides["interval_jitter_percent"] = json.RawMessage(`9223372036854775807`)
		}},
		{"禁止覆盖", "crawl_overrides.max_pages", "settings.task_override_forbidden", func(f *TaskFields) { f.CrawlOverrides["max_pages"] = json.RawMessage(`1`) }},
		{"非法发布时间", "publish_window", "settings.invalid_option", func(f *TaskFields) { f.PublishWindow = "day2" }},
		{"非法模式", "judge_mode", "settings.invalid_option", func(f *TaskFields) { f.JudgeMode = "other" }},
		{"指定空账号", "account_id", "settings.positive_id_required", func(f *TaskFields) { f.AccountPinned = true }},
		{"零账号", "account_id", "settings.positive_id_required", func(f *TaskFields) { f.AccountID = taskProfileTestPtr[int64](0) }},
		{"无效渠道", "channel_ids", "settings.positive_id_required", func(f *TaskFields) { f.ChannelIDs = []int64{1, -1} }},
		{"重复渠道", "channel_ids", "settings.duplicate_id", func(f *TaskFields) { f.ChannelIDs = []int64{1, 1} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := taskProfileTestFields()
			tc.change(&fields)
			_, _, err := normalizeTaskFields(nil, fields, TaskPatch{}, global)
			taskProfileTestError(t, err, tc.field, tc.code)
		})
	}
	fields := taskProfileTestFields()
	fields.MaxPages = 4
	fields.MinPrice, fields.MaxPrice = taskProfileTestPtr[int64](0), taskProfileTestPtr[int64](0)
	got, warnings, err := normalizeTaskFields(nil, fields, TaskPatch{}, global)
	if err != nil || got.MaxPages != 4 || len(warnings) != 1 || warnings[0].Key != "max_pages" {
		t.Fatalf("超推荐页数应只警告，零价格应合法：%#v %#v %v", got, warnings, err)
	}
}

func TestNormalizeTaskFieldsModeSwitchAndPolicies(t *testing.T) {
	global := taskProfileTestGlobal(t)
	old := Task{TaskFields: taskProfileTestFields()}
	old.JudgeMode, old.NotifyPolicy = analysis.ModeAI, "good"
	old.Profile = analysis.Profile{Musts: []analysis.Requirement{{ID: "m1", Text: "国行", Source: analysis.SourceUser, Rev: 1}}, NextNo: 2}
	fields := old.TaskFields
	fields.JudgeMode = analysis.ModeKeyword
	got, _, err := normalizeTaskFields(&old, fields, TaskPatch{"judge_mode": json.RawMessage(`"keyword"`)}, global)
	if err != nil || got.NotifyPolicy != "recommended" || !reflect.DeepEqual(got.Profile, analysis.Profile{}) {
		t.Fatalf("切关键词模式未清空或重设默认：%#v %v", got, err)
	}
	_, _, err = normalizeTaskFields(&old, fields, TaskPatch{"notify_policy": json.RawMessage(`"good"`)}, global)
	taskProfileTestError(t, err, "notify_policy", "settings.invalid_option")
	fields.NotifyPolicy = "all_new"
	got, _, err = normalizeTaskFields(&old, fields, TaskPatch{}, global)
	if err != nil || got.NotifyPolicy != "all_new" {
		t.Fatalf("切模式丢失合法通知设置：%#v %v", got, err)
	}
	fields.JudgeMode = analysis.ModeAI
	fields.Profile = analysis.Profile{Bonuses: []analysis.Requirement{{Text: "有配件", Source: analysis.SourceUser}}}
	_, _, err = normalizeTaskFields(nil, fields, TaskPatch{}, global)
	taskProfileTestError(t, err, "profile", "analysis.profile_invalid")
}

func TestNormalizeTaskProfileUsesServerIDsAndRevisions(t *testing.T) {
	global := taskProfileTestGlobal(t)
	old := Task{TaskFields: taskProfileTestFields()}
	old.JudgeMode, old.NotifyPolicy = analysis.ModeAI, "good"
	old.Profile = analysis.Profile{Musts: []analysis.Requirement{{ID: "m8", Text: "国行", Source: analysis.SourceUser, Rev: 3}},
		Risks: []analysis.ProfileRisk{{ID: "c9", Name: "屏幕", Hint: "没有亮斑", Attitude: analysis.Mind, Source: analysis.SourceUser, Rev: 4}}, NextNo: 5}
	fields := old.TaskFields
	fields.Profile = analysis.Profile{Musts: []analysis.Requirement{{ID: "m8", Text: "日版", Source: analysis.SourceUser, Rev: 999},
		{ID: "m700", Text: "带票", Source: analysis.SourceUser, Rev: -8}},
		Bonuses: []analysis.Requirement{{ID: "b900", Text: "原盒", Source: analysis.SourceUser, Rev: 999}},
		Risks: []analysis.ProfileRisk{{ID: "c9", Name: "屏幕", Hint: "没有亮斑", Question: "新问句", Attitude: analysis.Reject, Source: analysis.SourceUser, Rev: 999},
			{ID: "c1000", Name: "压伤", Attitude: analysis.Mind, Source: analysis.SourceUser}}, NextNo: 9000}
	before, _ := json.Marshal(fields)
	got, _, err := normalizeTaskFields(&old, fields, TaskPatch{}, global)
	if err != nil {
		t.Fatal(err)
	}
	p := got.Profile
	if p.Musts[0].Rev != 4 || p.Musts[1].ID != "m10" || p.Musts[1].Rev != 1 || p.Bonuses[0].ID != "b11" ||
		p.Risks[0].Rev != 4 || p.Risks[1].ID != "c12" || p.NextNo != 13 || len(p.Risks) != 2 {
		t.Fatalf("编号、版本或通用项被客户端影响：%#v", p)
	}
	after, _ := json.Marshal(fields)
	if string(before) != string(after) || old.Profile.Musts[0].Rev != 3 {
		t.Fatal("整理画像改到了调用方的数据")
	}
	fields.Profile.NextNo = -100
	got, _, err = normalizeTaskFields(&old, fields, TaskPatch{}, global)
	if err != nil || got.Profile.NextNo != 13 {
		t.Fatalf("客户端回拨改变了服务端水位：%#v %v", got.Profile, err)
	}
}

func TestNormalizeTaskProfileRejectsDeletedAndMalformedIDs(t *testing.T) {
	global := taskProfileTestGlobal(t)
	old := Task{TaskFields: taskProfileTestFields()}
	old.JudgeMode, old.NotifyPolicy = analysis.ModeAI, "good"
	old.Profile = analysis.Profile{Musts: []analysis.Requirement{{ID: "m8", Text: "国行", Source: analysis.SourceUser, Rev: 1}}, NextNo: 10}
	for _, id := range []string{"m3", "m01", "m-1", "m0", "b20", "m92233720368547758080", "m9007199254740991", "m9007199254740992"} {
		t.Run(id, func(t *testing.T) {
			fields := old.TaskFields
			fields.Profile = analysis.Profile{Musts: []analysis.Requirement{{ID: id, Text: "新项", Source: analysis.SourceUser}}, NextNo: 1}
			_, _, err := normalizeTaskFields(&old, fields, TaskPatch{}, global)
			taskProfileTestError(t, err, "profile", "analysis.profile_invalid")
		})
	}
	fields := old.TaskFields
	fields.Profile = analysis.Profile{Musts: []analysis.Requirement{{ID: "m20", Text: "一", Source: analysis.SourceUser}, {ID: "m20", Text: "二", Source: analysis.SourceUser}}}
	_, _, err := normalizeTaskFields(&old, fields, TaskPatch{}, global)
	taskProfileTestError(t, err, "profile", "analysis.profile_invalid")
	fields.Profile.Musts = fields.Profile.Musts[:1]
	got, _, err := normalizeTaskFields(nil, fields, TaskPatch{}, global)
	if err != nil || got.Profile.Musts[0].ID != "m1" || got.Profile.Musts[0].Rev != 1 || got.Profile.NextNo != 2 {
		t.Fatalf("创建任务信任了客户端编号：%#v %v", got.Profile, err)
	}
}

func TestNormalizeTaskProfileWarningsDoNotRestoreCommonItems(t *testing.T) {
	fields := taskProfileTestFields()
	fields.JudgeMode, fields.NotifyPolicy = analysis.ModeAI, "good"
	fields.Profile = analysis.Profile{Specs: []analysis.ProfileSpec{{Name: "型号", Options: []string{"A"}, Values: []string{"A"}, Source: analysis.SourceUser}},
		Musts: []analysis.Requirement{{ID: "m88", Text: "原盒", Source: analysis.SourceAI}},
		Risks: []analysis.ProfileRisk{{ID: "c99", Name: "亮斑", Attitude: analysis.Reject, Source: analysis.SourceAI}}, NextNo: 999}
	got, warnings, err := normalizeTaskFields(nil, fields, TaskPatch{}, taskProfileTestGlobal(t))
	if err != nil || len(got.Profile.Musts) != 0 || len(got.Profile.Bonuses) != 1 || got.Profile.Bonuses[0].ID != "b1" ||
		len(got.Profile.Risks) != 1 || got.Profile.Risks[0].ID != "c2" || got.Profile.Risks[0].Attitude != analysis.Mind ||
		len(warnings) != 2 || warnings[0].Key != "profile" || warnings[0].Reason.Code != "analysis.must_downgraded" ||
		warnings[1].Key != "profile" || warnings[1].Reason.Code != "analysis.risk_softened" {
		t.Fatalf("整理规则、编号或警告不正确：%#v %#v %v", got.Profile, warnings, err)
	}
}

func TestNormalizeTaskProfileRiskMeaningBumpsRevision(t *testing.T) {
	old := Task{TaskFields: taskProfileTestFields()}
	old.JudgeMode, old.NotifyPolicy = analysis.ModeAI, "good"
	old.Profile = analysis.Profile{Musts: []analysis.Requirement{{ID: "m1", Text: "国行", Source: analysis.SourceUser, Rev: 2}},
		Risks: []analysis.ProfileRisk{{ID: "c2", Name: "屏幕", Hint: "无亮斑", Source: analysis.SourceUser, Attitude: analysis.Mind, Rev: 3}}, NextNo: 3}
	fields := old.TaskFields
	fields.Profile.Risks = append([]analysis.ProfileRisk{}, old.Profile.Risks...)
	fields.Profile.Risks[0].Hint = "无亮斑、无压伤"
	got, _, err := normalizeTaskFields(&old, fields, TaskPatch{}, taskProfileTestGlobal(t))
	if err != nil || got.Profile.Musts[0].Rev != 2 || got.Profile.Risks[0].Rev != 4 {
		t.Fatalf("含义变更未更新版本：%#v %v", got.Profile, err)
	}
	old.Profile.Risks[0].Rev = limits.MaxSafeInteger
	_, _, err = normalizeTaskFields(&old, fields, TaskPatch{}, taskProfileTestGlobal(t))
	taskProfileTestError(t, err, "profile", "analysis.profile_invalid")
}

func TestNormalizeTaskFieldsAcceptsOnlyModePolicies(t *testing.T) {
	global := taskProfileTestGlobal(t)
	for _, mode := range []analysis.Mode{analysis.ModeAI, analysis.ModeKeyword} {
		for _, policy := range []string{"steal", "good", "fair", "recommended", "all_new", "none", "unknown"} {
			t.Run(string(mode)+"/"+policy, func(t *testing.T) {
				fields := taskProfileTestFields()
				fields.JudgeMode, fields.NotifyPolicy = mode, policy
				fields.Profile = analysis.Profile{Musts: []analysis.Requirement{{Text: "国行", Source: analysis.SourceUser}}}
				got, _, err := normalizeTaskFields(nil, fields, TaskPatch{"notify_policy": json.RawMessage(`"明确填写"`)}, global)
				valid := policy == "all_new" || policy == "none" || mode == analysis.ModeAI && (policy == "steal" || policy == "good" || policy == "fair") || mode == analysis.ModeKeyword && policy == "recommended"
				if valid {
					if err != nil || got.NotifyPolicy != policy {
						t.Fatalf("合法通知设置被改动：%#v %v", got, err)
					}
				} else {
					taskProfileTestError(t, err, "notify_policy", "settings.invalid_option")
				}
			})
		}
	}
}

func TestTaskSearchChangedChecksOnlySearchConditions(t *testing.T) {
	before := Task{TaskFields: taskProfileTestFields()}
	before.MinPrice, before.RegionCity = taskProfileTestPtr[int64](0), taskProfileTestPtr("杭州")
	after := before
	after.Name, after.MaxPages, after.IntervalMinutes, after.NotifyPolicy = "新名", 3, 60, "none"
	after.MatchKeywords, after.ExcludeKeywords = []string{"新词"}, []string{"新排除"}
	after.MinPrice, after.RegionCity = taskProfileTestPtr[int64](0), taskProfileTestPtr("杭州")
	if taskSearchChanged(before, after) {
		t.Fatal("非搜索条件或相同的指针值重置了基线")
	}
	for _, change := range []func(*Task){
		func(v *Task) { v.Keyword = "新词" }, func(v *Task) { v.MinPrice = nil }, func(v *Task) { v.MaxPrice = taskProfileTestPtr[int64](0) },
		func(v *Task) { v.PersonalOnly = !v.PersonalOnly }, func(v *Task) { v.FreeShippingOnly = !v.FreeShippingOnly },
		func(v *Task) { v.PublishWindow = "day1" }, func(v *Task) { v.RegionProvince = taskProfileTestPtr("浙江") },
		func(v *Task) { v.RegionCity = taskProfileTestPtr("宁波") }, func(v *Task) { v.RegionDistrict = taskProfileTestPtr("西湖") },
	} {
		after = before
		change(&after)
		if !taskSearchChanged(before, after) {
			t.Fatalf("遗漏搜索条件：%#v", after)
		}
	}
}

func TestAddedExcludeWordsComparesNormalizedWords(t *testing.T) {
	got := addedExcludeWords([]string{" ＡＢＣ ", "坏  机"}, []string{"abc", "坏 机，配件", "配件\n拆修"})
	if !reflect.DeepEqual(got, []string{"配件", "拆修"}) {
		t.Fatalf("新增排除词没有按匹配规则去重：%#v", got)
	}
}
