package notification

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/notify"
)

func TestRecommendationShowsCriticalConclusionsBesideTextEvidence(t *testing.T) {
	task := formatTask(analysis.ModeAI)
	task.Profile.Musts = []analysis.Requirement{{ID: "m1", Text: "国行", Rev: 2}}
	task.Profile.Risks = []analysis.ProfileRisk{{ID: "id_lock", Attitude: analysis.Confirm, Rev: 2}, {ID: "c3", Name: "自定义{count}", Attitude: analysis.Reject, Rev: 1}}
	body := []byte(`{"musts":[{"id":"m1","status":"met","source":"text","evidence":"非国行"}],"checks":[{"key":"id_lock","status":"ok","source":"text","evidence":"有ID锁"},{"key":"c3","status":"suspect","source":"text","evidence":"待确认{count}"}],"adjust":{"value":8,"reason":"综合判断"},"summary":"建议核对原文"}`)
	facts, warnings, err := analysis.ParseFacts(body, task.Profile, analysis.EvidenceOriginals{Description: "非国行，有ID锁，待确认{count}"})
	if err != nil {
		t.Fatal(err)
	}
	for _, language := range []locales.Language{locales.ZhCN, locales.En} {
		for _, kind := range []intentKind{intentRecommended, intentPriceUpgrade} {
			for _, channel := range []notify.Type{notify.Bark, notify.Telegram, notify.Webhook} {
				t.Run(fmt.Sprintf("%s/%s/%s", language, kind, channel), func(t *testing.T) {
					value := formatCandidate(1, kind, formatPtr(int64(84)))
					value.Finding.Analysis, value.Finding.AnalysisWarnings = &facts, warnings
					message, err := formatGroup(task, messageGroup{Members: []candidate{value}}, formatChannel(channel), formatOpts(language))
					if err != nil {
						t.Fatal(err)
					}
					want := []string{"国行：满足 · 原文：非国行", "ID 锁：没问题 · 原文：有ID锁", "自定义{count}：疑似 · 原文：待确认{count}"}
					if language == locales.En {
						want = []string{"国行: Met · Original text: 非国行", "ID lock: No issue found · Original text: 有ID锁", "自定义{count}: Suspected issue · Original text: 待确认{count}"}
					}
					previous := strings.Index(message.Body, "\n")
					for _, line := range want {
						position := strings.Index(message.Body, line)
						if position <= previous || strings.Count(message.Body, line) != 1 {
							t.Fatalf("关键项结论旁未显示真实引用，或顺序/重复不符：%q，期望%q", message.Body, line)
						}
						previous = position
					}
					if !strings.HasSuffix(message.Body, *value.Finding.Summary) || strings.Contains(message.Reason, "有ID锁") {
						t.Fatal("原有首行和末尾结论被改写")
					}
				})
			}
		}
	}
}

func TestRecommendationDoesNotBorrowStaleOverriddenOrMissingEvidence(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*analysis.Profile, *analysis.Facts, *analysis.UserOverrides, *[]locales.Message)
	}{
		{"旧版本", func(profile *analysis.Profile, _ *analysis.Facts, _ *analysis.UserOverrides, _ *[]locales.Message) {
			profile.Musts[0].Rev, profile.Risks[0].Rev = 3, 3
		}},
		{"无版本", func(_ *analysis.Profile, facts *analysis.Facts, _ *analysis.UserOverrides, _ *[]locales.Message) {
			facts.Musts[0].Rev, facts.Checks[0].Rev = 0, 0
		}},
		{"图片不是原文", func(_ *analysis.Profile, facts *analysis.Facts, _ *analysis.UserOverrides, _ *[]locales.Message) {
			facts.Musts[0].Source, facts.Checks[0].Source = formatPtr("image"), formatPtr("image")
		}},
		{"未核对到原文", func(_ *analysis.Profile, _ *analysis.Facts, _ *analysis.UserOverrides, warnings *[]locales.Message) {
			*warnings = []locales.Message{{Code: "analysis.evidence_missing", Args: map[string]any{"item": "m1"}}, {Code: "analysis.evidence_missing", Args: map[string]any{"item": "id_lock"}}}
		}},
		{"无证据", func(_ *analysis.Profile, facts *analysis.Facts, _ *analysis.UserOverrides, _ *[]locales.Message) {
			facts.Musts[0].Evidence, facts.Checks[0].Evidence = nil, nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			task := formatTask(analysis.ModeAI)
			task.Profile.Musts = []analysis.Requirement{{ID: "m1", Text: "国行", Rev: 2}}
			task.Profile.Risks = []analysis.ProfileRisk{{ID: "id_lock", Attitude: analysis.Confirm, Rev: 2}}
			facts := analysis.Facts{Musts: []analysis.RequirementFact{{ID: "m1", Rev: 2, Status: analysis.ReqMet, Source: formatPtr("text"), Evidence: formatPtr("旧证据")}}, Checks: []analysis.CheckFact{{Key: "id_lock", Rev: 2, Status: analysis.CheckOK, Source: formatPtr("text"), Evidence: formatPtr("旧证据")}}}
			value := formatCandidate(1, intentRecommended, formatPtr(int64(84)))
			value.Finding.Analysis = &facts
			test.edit(&task.Profile, &facts, &value.Finding.UserOverrides, &value.Finding.AnalysisWarnings)
			message, err := formatGroup(task, messageGroup{Members: []candidate{value}}, formatChannel(notify.Bark), formatOpts(locales.ZhCN))
			if err != nil || strings.Contains(message.Body, "旧证据") {
				t.Fatalf("非当前原文被拼到了推荐结论上：%q，%v", message.Body, err)
			}
		})
	}
	for _, rev := range []int64{2, 3} {
		t.Run(fmt.Sprintf("手动覆盖版本%d", rev), func(t *testing.T) {
			task := formatTask(analysis.ModeAI)
			task.Profile.Risks = []analysis.ProfileRisk{{ID: "id_lock", Attitude: analysis.Confirm, Rev: 2}}
			value := formatCandidate(1, intentRecommended, formatPtr(int64(84)))
			value.Finding.Analysis = &analysis.Facts{Checks: []analysis.CheckFact{{Key: "id_lock", Rev: 2, Status: analysis.CheckOK, Source: formatPtr("text"), Evidence: formatPtr("原始引用")}}}
			value.Finding.UserOverrides.Risks = map[string]analysis.RiskOverride{"id_lock": {Status: analysis.CheckRisk, Rev: rev}}
			message, err := formatGroup(task, messageGroup{Members: []candidate{value}}, formatChannel(notify.Bark), formatOpts(locales.ZhCN))
			if err != nil || strings.Contains(message.Body, "原始引用") != (rev == 3) {
				t.Fatalf("手动覆盖借用了旧模型原文，或失效覆盖隐藏了有效引用：%q，%v", message.Body, err)
			}
		})
	}
}

func TestNonRecommendationBodiesDoNotGainChecklistEvidence(t *testing.T) {
	task := formatTask(analysis.ModeAI)
	task.Profile.Musts = []analysis.Requirement{{ID: "m1", Text: "国行", Rev: 1}}
	task.Profile.Risks = []analysis.ProfileRisk{{ID: "id_lock", Attitude: analysis.Confirm, Rev: 1}}
	facts := &analysis.Facts{Musts: []analysis.RequirementFact{{ID: "m1", Rev: 1, Status: analysis.ReqMet, Source: formatPtr("text"), Evidence: formatPtr("仅供核对")}}, Checks: []analysis.CheckFact{{Key: "id_lock", Rev: 1, Status: analysis.CheckOK, Source: formatPtr("text"), Evidence: formatPtr("仅供核对")}}}
	for _, kind := range []intentKind{intentNew, intentPriceDrop} {
		value := formatCandidate(1, kind, formatPtr(int64(84)))
		value.Finding.Analysis = facts
		message, err := formatGroup(task, messageGroup{Members: []candidate{value}}, formatChannel(notify.Bark), formatOpts(locales.ZhCN))
		if err != nil || strings.Contains(message.Body, "仅供核对") || strings.Contains(message.Body, *value.Finding.Summary) || strings.Contains(message.Body, "\n") {
			t.Fatalf("新商品或普通降价正文被扩充：%q，%v", message.Body, err)
		}
	}
	value := formatCandidate(1, intentRecommended, formatPtr(int64(84)))
	value.Finding.Analysis = facts
	message, err := formatGroup(task, messageGroup{Members: []candidate{value}, Summary: true}, formatChannel(notify.Bark), formatOpts(locales.ZhCN))
	if err != nil || strings.Contains(message.Body, "仅供核对") {
		t.Fatalf("合并摘要格式被扩充：%q，%v", message.Body, err)
	}
	for _, attitude := range []analysis.Attitude{analysis.Mind, analysis.Ignore} {
		task.Profile.Musts = nil
		task.Profile.Risks[0].Attitude = attitude
		message, err := formatGroup(task, messageGroup{Members: []candidate{value}}, formatChannel(notify.Bark), formatOpts(locales.ZhCN))
		if err != nil || strings.Contains(message.Body, "仅供核对") {
			t.Fatalf("非关键检查项被额外加入推荐正文：%q，%v", message.Body, err)
		}
	}
}
