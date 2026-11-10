package notification

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/service"
)

func decisionPtr[T any](value T) *T { return &value }

func firstFixture(score *int64) (service.Task, candidate) {
	task := service.Task{ID: 1, SearchGeneration: 1, TaskFields: service.TaskFields{JudgeMode: analysis.ModeAI, NotifyPolicy: "good"}}
	value := candidate{Finding: service.Finding{ID: 1, TaskID: 1, SearchGeneration: 1, ProcessingRevision: 1,
		Decision: analysis.DecisionScored, Stage: service.StageDone, Score: score}, Listing: service.Listing{Price: decisionPtr(int64(10000))}}
	if score != nil {
		value.Finding.Grade = decisionPtr(gradeForScore(*score))
	}
	value.Finding.Reason = notificationReason("analysis.no_fair_price", nil)
	return task, value
}

func TestDecideFirstPolicyTruthTable(t *testing.T) {
	for _, test := range []struct {
		name, policy, kind, skip string
		score                    *int64
		baseline                 bool
	}{
		{"捡漏差一分", "steal", "", "notify.below_steal", decisionPtr(int64(89)), false},
		{"捡漏门槛", "steal", "recommended", "", decisionPtr(int64(90)), true},
		{"划算差一分", "good", "", "notify.below_good", decisionPtr(int64(79)), false},
		{"划算门槛", "good", "recommended", "", decisionPtr(int64(80)), true},
		{"合理差一分", "fair", "", "notify.below_fair", decisionPtr(int64(64)), false},
		{"合理门槛", "fair", "recommended", "", decisionPtr(int64(65)), true},
		{"合理七十二", "fair", "recommended", "", decisionPtr(int64(72)), true},
		{"所有新商品低分", "all_new", "new", "", decisionPtr(int64(79)), false},
		{"所有新商品推荐", "all_new", "recommended", "", decisionPtr(int64(80)), true},
		{"捡漏不接受无法比价", "steal", "", "notify.no_comparison_steal", nil, false},
		{"划算接受无法比价", "good", "recommended", "", nil, true},
		{"合理接受无法比价", "fair", "recommended", "", nil, true},
		{"所有新商品接受无法比价", "all_new", "recommended", "", nil, true},
		{"不推送仍留基准", "none", "", "notify.policy_none", decisionPtr(int64(85)), true},
		{"不推送不降低门槛", "none", "", "notify.policy_none", decisionPtr(int64(72)), false},
		{"不推送无法比价不记基准", "none", "", "notify.policy_none", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			task, value := firstFixture(test.score)
			task.NotifyPolicy = test.policy
			got, err := decideFirst(task, value, true)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Apply || got.SetRecommendedPrice != test.baseline || (got.Kind == nil) == (got.SkipReason == nil) {
				t.Fatalf("判定或写基准错误：%+v", got)
			}
			if test.kind != "" && (got.Kind == nil || *got.Kind != test.kind) ||
				test.skip != "" && (got.SkipReason == nil || got.SkipReason.Code != test.skip) {
				t.Fatalf("判定错误：%+v，期望种类 %q，原因 %q", got, test.kind, test.skip)
			}
		})
	}
}

func TestDecideFirstKeywordTruthTable(t *testing.T) {
	for _, policy := range []string{"recommended", "all_new", "none"} {
		for _, matched := range []bool{true, false} {
			task, value := firstFixture(nil)
			task.JudgeMode, task.NotifyPolicy = analysis.ModeKeyword, policy
			value.Finding.Decision = analysis.DecisionMatched
			if !matched {
				value.Finding.Decision, value.Finding.RejectKind = analysis.DecisionRejected, decisionPtr(analysis.RejectKeywordMiss)
				value.Finding.Reason = notificationReason("analysis.keyword_miss", nil)
			}
			got, err := decideFirst(task, value, true)
			if err != nil || !got.Apply || got.SetRecommendedPrice != matched {
				t.Fatalf("%s 命中=%v：%+v %v", policy, matched, got, err)
			}
			if policy == "none" {
				if got.SkipReason == nil || got.SkipReason.Code != "notify.policy_none" {
					t.Fatal("不推送的优先级错误")
				}
			} else if matched {
				if got.Kind == nil || *got.Kind != "recommended" {
					t.Fatal("关键词命中没有推荐")
				}
			} else if policy == "all_new" {
				if got.Kind == nil || *got.Kind != "new" {
					t.Fatal("所有新商品漏掉关键词未命中")
				}
			} else if got.SkipReason == nil || got.SkipReason.Code != "notify.keyword_miss" {
				t.Fatal("关键词未命中原因错误")
			}
		}
	}
}

func TestDecideFirstSkipPriorityAndBaseline(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*service.Task, *candidate)
		code string
		base bool
	}{
		{"基线先于不推送", func(task *service.Task, value *candidate) {
			value.Finding.IsBaseline = true
			task.NotifyPolicy = "none"
		}, "notify.baseline", true},
		{"不推送先于隐藏", func(task *service.Task, value *candidate) {
			task.NotifyPolicy = "none"
			value.Listing.IsDismissed = true
		}, "notify.policy_none", false},
		{"不感兴趣先于屏蔽", func(_ *service.Task, value *candidate) { value.Listing.IsDismissed = true; value.SellerBlocked = true }, "notify.dismissed", false},
		{"屏蔽", func(_ *service.Task, value *candidate) { value.SellerBlocked = true }, "notify.blocked", false},
		{"基线不把屏蔽算推荐", func(_ *service.Task, value *candidate) { value.Finding.IsBaseline = true; value.SellerBlocked = true }, "notify.baseline", false},
		{"预算先于渠道", func(task *service.Task, _ *candidate) { task.BudgetMax = decisionPtr(int64(9999)) }, "notify.over_budget", false},
		{"低分先于渠道", func(_ *service.Task, value *candidate) {
			value.Finding.Score = decisionPtr(int64(72))
			value.Finding.Grade = decisionPtr(analysis.GradeFair)
		}, "notify.below_good", false},
		{"无渠道仍记基准", func(_ *service.Task, value *candidate) { value.ReanalysisOrigin = true }, "notify.no_channel", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			task, value := firstFixture(decisionPtr(int64(95)))
			test.edit(&task, &value)
			got, err := decideFirst(task, value, false)
			if err != nil || got.SkipReason == nil || got.SkipReason.Code != test.code || got.SetRecommendedPrice != test.base {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	task, value := firstFixture(decisionPtr(int64(95)))
	value.ReanalysisOrigin = true
	got, err := decideFirst(task, value, true)
	if err != nil || got.SkipReason == nil || got.SkipReason.Code != "notify.reanalysis" || !got.SetRecommendedPrice {
		t.Fatalf("重新分析只抑制提醒：%+v %v", got, err)
	}
}

func TestDecideFirstPreservesStoredRejectionReasons(t *testing.T) {
	for _, policy := range []string{"good", "all_new"} {
		for _, kind := range []analysis.RejectKind{analysis.RejectFilter, analysis.RejectExcluded, analysis.RejectJunk, analysis.RejectUnmatched} {
			task, value := firstFixture(nil)
			task.NotifyPolicy = policy
			value.Finding.Decision, value.Finding.RejectKind = analysis.DecisionRejected, &kind
			value.Finding.Reason = notificationReason("analysis.spec_mismatch", map[string]any{"name": "型号", "value": "mini 5"})
			got, err := decideFirst(task, value, true)
			if err != nil {
				t.Fatal(err)
			}
			if policy == "all_new" && kind == analysis.RejectUnmatched {
				if got.Kind == nil || *got.Kind != "new" || got.SetRecommendedPrice {
					t.Fatal("所有新商品应该包含不符合需求")
				}
			} else if !reflect.DeepEqual(got.SkipReason, value.Finding.Reason) || got.SetRecommendedPrice {
				t.Fatalf("原因没有原样保留：%+v", got)
			} else {
				got.SkipReason.Args["name"] = "已修改"
				if value.Finding.Reason.Args["name"] != "型号" {
					t.Fatal("返回原因复用了原参数映射")
				}
			}
		}
	}
	for _, score := range []*int64{nil, decisionPtr(int64(39)), decisionPtr(int64(90))} {
		task, value := firstFixture(score)
		task.NotifyPolicy = "all_new"
		value.Finding.Grade = decisionPtr(analysis.GradeAvoid)
		value.Finding.Reason = notificationReason("analysis.risk_confirmed", map[string]any{"name": "ID锁", "id": "id_lock", "attitude": "confirm"})
		got, err := decideFirst(task, value, true)
		if err != nil || !reflect.DeepEqual(got.SkipReason, value.Finding.Reason) || got.SetRecommendedPrice {
			t.Fatalf("不建议没有保留风险原由：%+v %v", got, err)
		}
	}
}

func TestDecideFirstPriceBoundariesAndSingleDecision(t *testing.T) {
	task, value := firstFixture(decisionPtr(int64(85)))
	task.BudgetMin, task.BudgetMax = decisionPtr(int64(20000)), decisionPtr(int64(10000))
	got, err := decideFirst(task, value, true)
	if err != nil || got.Kind == nil || !got.SetRecommendedPrice {
		t.Fatalf("等于上限应通过、预算下限不参与：%+v %v", got, err)
	}
	*value.Listing.Price++
	got, err = decideFirst(task, value, true)
	if err != nil || got.SkipReason == nil || got.SkipReason.Code != "notify.over_budget" || got.SetRecommendedPrice ||
		got.SkipReason.Args["price"] != "¥100.01" || got.SkipReason.Args["budget"] != "¥100" {
		t.Fatalf("超出一分：%+v %v", got, err)
	}
	task.BudgetMax = nil
	value.Listing.Price = nil
	got, err = decideFirst(task, value, true)
	if err != nil || got.Kind == nil || !got.SetRecommendedPrice || got.RecommendedPrice != nil {
		t.Fatalf("未知价不能混同未判定：%+v %v", got, err)
	}
	value.Finding.NotifyKind = got.Kind
	got, err = decideFirst(task, value, true)
	if err != nil || got.Apply {
		t.Fatal("未知基准造成重复判定")
	}
	value.Finding.NotifyKind = nil
	value.Finding.NotifySkipReason = notificationReason("notify.no_channel", nil)
	got, err = decideFirst(task, value, true)
	if err != nil || got.Apply {
		t.Fatal("已有不推原因造成重复判定")
	}
	value.Finding.NotifySkipReason = nil
	value.Finding.RecommendedPrice = decisionPtr(int64(12000))
	value.Listing.Price = decisionPtr(int64(0))
	got, err = decideFirst(task, value, true)
	if err != nil || got.Kind == nil || got.SetRecommendedPrice {
		t.Fatal("零价应可用，旧基准不能覆盖")
	}
}

func TestDecideFirstRejectsInvalidFactsAndUnknownBudgetPrice(t *testing.T) {
	for _, edit := range []func(*service.Task, *candidate){
		func(task *service.Task, _ *candidate) { task.JudgeMode = "invalid" },
		func(task *service.Task, _ *candidate) { task.NotifyPolicy = "recommended" },
		func(task *service.Task, _ *candidate) { task.JudgeMode = analysis.ModeKeyword },
		func(_ *service.Task, value *candidate) { value.Finding.Grade = nil },
		func(_ *service.Task, value *candidate) { value.Finding.Score = decisionPtr(int64(101)) },
		func(_ *service.Task, value *candidate) { value.Finding.Grade = decisionPtr(analysis.GradeFair) },
		func(_ *service.Task, value *candidate) { value.Finding.Score = nil },
		func(_ *service.Task, value *candidate) { value.Finding.Decision = "invalid" },
		func(_ *service.Task, value *candidate) { value.Finding.Decision = analysis.DecisionRejected },
	} {
		task, value := firstFixture(decisionPtr(int64(85)))
		edit(&task, &value)
		if got, err := decideFirst(task, value, true); err == nil || got.Apply {
			t.Fatalf("无效数据被判定：%+v %v", got, err)
		}
	}
	for _, state := range []analysis.Decision{service.DecisionPending, service.DecisionFailed, service.DecisionSkipped} {
		task, value := firstFixture(decisionPtr(int64(85)))
		value.Finding.Decision = state
		if got, err := decideFirst(task, value, true); err != nil || got.Apply {
			t.Fatalf("非终态被判定：%+v %v", got, err)
		}
	}
	task, value := firstFixture(nil)
	value.Listing.Price, task.BudgetMax = nil, decisionPtr(int64(10000))
	if got, err := decideFirst(task, value, true); err != nil || got.SkipReason == nil || got.SkipReason.Code != "notify.price_unknown_budget" || got.SetRecommendedPrice {
		t.Fatalf("未知预算价应保存具体原因：%+v %v", got, err)
	}
	for _, mode := range []analysis.Mode{analysis.ModeAI, analysis.ModeKeyword} {
		task.JudgeMode, task.NotifyPolicy = mode, "all_new"
		value.Finding.Decision = analysis.DecisionRejected
		value.Finding.RejectKind = decisionPtr(analysis.RejectUnmatched)
		if mode == analysis.ModeKeyword {
			value.Finding.RejectKind = decisionPtr(analysis.RejectKeywordMiss)
		}
		if got, err := decideFirst(task, value, true); err != nil || got.SkipReason == nil || got.SkipReason.Code != "notify.price_unknown_budget" || got.SetRecommendedPrice {
			t.Fatalf("所有新商品绕过未知预算价：%+v %v", got, err)
		}
	}
	task, value = firstFixture(nil)
	task.BudgetMax, task.NotifyPolicy = nil, "none"
	if got, err := decideFirst(task, value, true); err != nil || got.SkipReason == nil || got.SkipReason.Code != "notify.policy_none" || got.SetRecommendedPrice {
		t.Fatalf("不推送无法比价不能写基准：%+v %v", got, err)
	}
}

func TestDecideFirstDoesNotChangeInputAndCopiesReturnedPrice(t *testing.T) {
	task, value := firstFixture(decisionPtr(int64(85)))
	task.Profile.Specs = []analysis.ProfileSpec{{Name: "型号", Options: []string{"mini 6"}, Values: []string{"mini 6"}}}
	value.Finding.MatchedKeywords = []string{"mini"}
	before, err := json.Marshal([]any{task, value})
	if err != nil {
		t.Fatal(err)
	}
	got, err := decideFirst(task, value, true)
	after, encodeErr := json.Marshal([]any{task, value})
	if err != nil || encodeErr != nil || !bytes.Equal(before, after) {
		t.Fatalf("判定修改了输入：%v", err)
	}
	if got.RecommendedPrice == nil {
		t.Fatal("没有返回价格")
	}
	*got.RecommendedPrice = 1
	if *value.Listing.Price != 10000 {
		t.Fatal("返回价格复用了输入指针")
	}
}

func TestRevalidateReanalysisAllNewKeepsOriginalKindAndAccurateReason(t *testing.T) {
	for _, kind := range []intentKind{intentRecommended, intentPriceUpgrade, intentNew} {
		t.Run(string(kind), func(t *testing.T) {
			task, value := firstFixture(decisionPtr(int64(72)))
			task.NotifyPolicy, task.NotifyPriceDrop = "all_new", true
			eligible, reason, err := revalidateReanalysisIntent(task, value, intent{Kind: kind}, true)
			if err != nil {
				t.Fatal(err)
			}
			if kind == intentNew {
				if !eligible || reason != nil {
					t.Fatal("原新商品在所有新商品设置下被缩掉")
				}
			} else if eligible || reason == nil || reason.Code != "notify.reanalysis_below_recommendation" ||
				!reflect.DeepEqual(reason.Args, map[string]any{"score": int64(72)}) {
				t.Fatalf("所有新商品设置被写成只推划算：%v %+v", eligible, reason)
			}
		})
	}
}
