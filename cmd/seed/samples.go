package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
)

type samples struct {
	tasks    []service.Task
	listings []service.Listing
	findings []service.Finding
	specs    []service.TaskItemSpecs
	prices   []service.PricePoint
	runs     []service.RunRecord
}

func samplePtr[T any](value T) *T { return &value }

func makeSamples(now time.Time, timezone string) (samples, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return samples{}, err
	}
	observed := now.Add(-2 * time.Minute).UnixMilli()
	started, ended := now.Add(-5*time.Minute).UnixMilli(), now.Add(-time.Minute).UnixMilli()
	created := now.Add(-72 * time.Hour).UnixMilli()
	profile := analysis.DefaultProfile()
	profile.Summary = "示例：日常办公轻薄本，维修情况必须明确"
	profile.Risks = append(profile.Risks, analysis.ProfileRisk{ID: "repaired", Attitude: analysis.Confirm, Source: analysis.SourceUser, Rev: 1})
	profile.Musts = []analysis.Requirement{{ID: "m1", Text: "可用于日常办公", Source: analysis.SourceUser, Rev: 1}}
	profile.NextNo = 2
	profile, warnings, err := analysis.NormalizeProfile(profile)
	if err != nil {
		return samples{}, err
	}
	if len(warnings) != 0 {
		return samples{}, errors.New("样例清单未通过完整校验")
	}
	out := samples{}
	for index, mode := range []analysis.Mode{analysis.ModeKeyword, analysis.ModeAI} {
		task := service.Task{ID: int64(index + 1), TaskFields: service.TaskFields{
			Name: "示例 · 黑色相机", Keyword: "相机", Enabled: false,
			PersonalOnly: true, PublishWindow: "any", MaxPages: 1, IntervalMinutes: 60,
			CrawlOverrides: settings.Values{}, JudgeMode: mode, MatchKeywords: []string{"黑色"}, ExcludeKeywords: []string{"求购"},
			AnalyzeOnFirstRun: true, NotifyPolicy: "none", NotifyPriceDrop: false, ChannelIDs: []int64{},
		}, SearchGeneration: 1, SortIndex: int64(index), CreatedAt: &created, UpdatedAt: &created,
			LastRunAt: &started, LastSuccessAt: &ended, LastRunOutcome: samplePtr("success")}
		if mode == analysis.ModeAI {
			task.Name, task.Keyword, task.Profile = "示例 · 轻薄本", "轻薄本", profile
			task.Requirement, task.MatchKeywords, task.ExcludeKeywords = profile.Summary, []string{}, []string{}
			task.BudgetMax = samplePtr(int64(500000))
		}
		out.tasks = append(out.tasks, task)
	}
	ids := []string{"sample-camera-match", "sample-camera-miss", "sample-camera-excluded", "sample-laptop-deal", "sample-laptop-fair", "sample-laptop-ordinary", "sample-laptop-check", "sample-laptop-risk", "sample-laptop-unknown"}
	titles := []string{"【示例】黑色定焦相机，箱说齐全", "【示例】银色定焦相机", "【示例】求购黑色相机", "【示例】轻薄本，毕业转让", "【示例】轻薄本，正常使用痕迹", "【示例】轻薄本，带原装配件", "【示例】轻薄本，维修情况待确认", "【示例】轻薄本，更换过主板", "【示例】轻薄本，价格面议"}
	prices := []*int64{samplePtr(int64(580000)), samplePtr(int64(560000)), samplePtr(int64(550000)), samplePtr(int64(290000)), samplePtr(int64(360000)), samplePtr(int64(400000)), samplePtr(int64(410000)), samplePtr(int64(480000)), nil}
	for index, id := range ids {
		listing := service.Listing{ItemID: id, Title: titles[index], Price: prices[index], PriceText: samplePtr("面议"),
			DesktopURL: "https://example.invalid/kakatta-sample/" + id, FirstSeenAt: observed, LastSeenAt: observed,
			ImageURLs: []string{}, Tags: []string{}, Area: samplePtr("上海市"), Description: samplePtr("离线虚构样例，不对应任何真实商品。"), DetailFetchedAt: &observed}
		if listing.Price != nil {
			listing.PriceText = samplePtr(fmt.Sprintf("¥%d.%02d", *listing.Price/100, *listing.Price%100))
			out.prices = append(out.prices, service.PricePoint{ItemID: id, Price: *listing.Price, ObservedAt: observed})
		}
		listing.IsDismissed = index == 2
		if index == 1 || index == 2 {
			listing.Description, listing.DetailFetchedAt = nil, nil
		}
		if index == 3 {
			listing.IsFavorite, listing.FavoritedAt = true, &observed
			old := now.Add(-48 * time.Hour).UnixMilli()
			listing.FirstSeenAt, listing.LastDropAt, listing.LastDropAmount = old, &observed, samplePtr(int64(20000))
			out.prices = append(out.prices, service.PricePoint{ItemID: id, Price: 310000, ObservedAt: old})
		}
		out.listings = append(out.listings, listing)
	}
	observations := []analysis.MarketObservation{}
	for _, listing := range out.listings[3:] {
		observations = append(observations, analysis.MarketObservation{ItemID: listing.ItemID, Title: listing.Title,
			Day: time.UnixMilli(observed).In(location).Format(time.DateOnly), Price: listing.Price, LastSeenAt: observed,
			Kind: analysis.KindSale, Specs: []analysis.SpecValue{}, BrandNew: samplePtr(false)})
	}
	market := analysis.CalculateMarket(analysis.MarketInput{Mode: analysis.ModeAI, Profile: profile, Observations: observations, NowMS: now.UnixMilli(), TimeZone: timezone})
	if len(market.Groups) != 1 || market.Groups[0].Fair.Cents == nil || market.Groups[0].Fair.Sample != 5 {
		return samples{}, errors.New("样例行情未得到五个有效价格")
	}
	fair := market.Groups[0].Fair
	seller := analysis.JudgeSeller(analysis.SellerInput{}, nil, "轻薄本")
	for index := range out.listings {
		listing := &out.listings[index]
		task := out.tasks[0]
		if index >= 3 {
			task = out.tasks[1]
		}
		finding := service.Finding{ID: int64(index + 1), TaskID: task.ID, ItemID: listing.ItemID, SearchGeneration: 1,
			ProcessingRevision: 1, FoundAt: listing.FirstSeenAt, Stage: service.StageDone, Decision: analysis.DecisionMatched,
			DecidedLayer: samplePtr(int64(1)), MatchedKeywords: []string{}, Attempts: 1, IsRead: index == 1 || index == 4,
			UserOverrides:    analysis.UserOverrides{Risks: map[string]analysis.RiskOverride{}},
			NotifySkipReason: &locales.Message{Code: "notify.policy_none", Args: map[string]any{}}}
		if task.JudgeMode == analysis.ModeKeyword {
			description := ""
			if listing.Description != nil {
				description = *listing.Description
			}
			layer := analysis.JudgeKeyword(listing.Title, description, task.MatchKeywords, task.ExcludeKeywords)
			finding.Reason, finding.MatchedKeywords = layer.Reason, layer.MatchedKeywords
			if finding.MatchedKeywords == nil {
				finding.MatchedKeywords = []string{}
			}
			if layer.Rejected {
				finding.Decision, finding.RejectKind, finding.Attempts = analysis.DecisionRejected, &layer.RejectKind, 0
				finding.NotifySkipReason = nil
			}
		} else {
			facts := analysis.Facts{Specs: []analysis.SpecValue{}, BrandNew: samplePtr(false),
				Musts: []analysis.RequirementFact{{ID: "m1", Status: analysis.ReqMet, Source: samplePtr("text"), Evidence: samplePtr("日常办公用轻薄本"), Rev: 1}}, Bonuses: []analysis.RequirementFact{},
				Checks: []analysis.CheckFact{}, SellerSignals: []analysis.SellerSignal{}, ExtraFindings: []analysis.ExtraFinding{}, Summary: "示例：已保存的离线分析事实。"}
			status, evidence := analysis.CheckOK, "未拆未修"
			if index == 6 {
				status, evidence, facts.Summary = analysis.CheckUnknown, "", "示例：维修情况未提及，联系前需要确认。"
			}
			if index == 7 {
				status, evidence, facts.Summary = analysis.CheckRisk, "更换过主板", "示例：卖家明确提到更换主板，触发必须确认的风险。"
			}
			description := "离线虚构样例：日常办公用轻薄本，维修情况未说明。"
			if evidence != "" {
				description = "离线虚构样例：日常办公用轻薄本，" + evidence + "。"
			}
			listing.Description = &description
			check := analysis.CheckFact{Key: "repaired", Status: status, Rev: 1}
			if evidence != "" {
				check.Source, check.Evidence = samplePtr("text"), &evidence
			}
			facts.Checks = append(facts.Checks, check)
			if listing.Price == nil {
				facts.Summary = "示例：商品价格未知，已保存事实但无法给出分数。"
			}
			body, err := json.Marshal(facts)
			if err != nil {
				return samples{}, err
			}
			facts, warnings, err = analysis.ParseFacts(body, profile, analysis.EvidenceOriginals{Title: listing.Title, Description: *listing.Description})
			if err != nil {
				return samples{}, err
			}
			if len(warnings) != 0 {
				return samples{}, errors.New("样例事实未通过原文证据核对")
			}
			specs := analysis.ResolveSpecs(profile, facts.Specs, facts.BrandNew, analysis.SpecDetail)
			specs.Kind = analysis.KindSale
			out.specs = append(out.specs, service.TaskItemSpecs{TaskID: task.ID, ItemID: listing.ItemID, UpdatedAt: ended, ItemSpecs: specs})
			result := analysis.Score(analysis.ScoreInput{Profile: profile, Facts: facts, Item: specs, Price: listing.Price, Fair: &fair, Seller: seller, Overrides: finding.UserOverrides})
			finding.Decision, finding.Analysis, finding.DecidedLayer = result.Decision, &facts, samplePtr(int64(3))
			finding.Reason, finding.Grade, finding.ScoreBreakdown = &result.Reason, result.Grade, &result.Breakdown
			if result.Score != nil {
				finding.Score = samplePtr(int64(*result.Score))
			}
			finding.FairPrice, finding.FairSample, finding.FairBasis = fair.Cents, samplePtr(int64(fair.Sample)), &fair.Basis
			finding.ScoredPrice, finding.ScoredAt, finding.AnalyzedAt = listing.Price, &ended, &ended
			finding.Summary, finding.SellerType, finding.SellerAssessment = &facts.Summary, &seller.Type, &seller
		}
		out.findings = append(out.findings, finding)
	}
	for _, task := range out.tasks {
		scanned, newCount, rejected, analyzed, recommended := int64(0), int64(0), int64(0), int64(0), int64(0)
		for _, finding := range out.findings {
			if finding.TaskID != task.ID {
				continue
			}
			scanned++
			if finding.FoundAt >= started {
				newCount++
			}
			if finding.Decision == analysis.DecisionRejected {
				rejected++
			} else {
				analyzed++
			}
			if sampleRecommended(task, finding, out.listings[finding.ID-1]) {
				recommended++
			}
		}
		layer2 := int64(0)
		if task.JudgeMode == analysis.ModeAI {
			layer2 = scanned
		}
		message := locales.Message{Code: "run.completed", Args: map[string]any{"count": int64(1), "page_count": int64(1), "analyzed_count": analyzed}}
		out.runs = append(out.runs, service.RunRecord{ID: task.ID, TaskID: samplePtr(task.ID), TaskName: task.Name, Trigger: service.TriggerManual,
			StartedAt: &started, EndedAt: &ended, Outcome: samplePtr(service.RunSuccess), Message: &message, RepeatCount: 1,
			PageCount: samplePtr(int64(1)), ScannedCount: &scanned, NewCount: &newCount, Layer1Rejected: &rejected,
			Layer2Count: &layer2, Layer2Rejected: samplePtr(int64(0)), DetailCount: &analyzed,
			SellerPageCount: samplePtr(int64(0)), AnalyzedCount: &analyzed, RecommendedCount: &recommended, NotifiedCount: samplePtr(int64(0)), Warnings: []locales.Message{}})
	}
	return out, nil
}

// 统计推荐固定按 80 分判断，不随任务的推送档位变化。
func sampleRecommended(task service.Task, finding service.Finding, listing service.Listing) bool {
	if listing.IsDismissed || task.BudgetMax != nil && (listing.Price == nil || *listing.Price > *task.BudgetMax) {
		return false
	}
	if task.JudgeMode == analysis.ModeKeyword {
		return finding.Decision == analysis.DecisionMatched
	}
	if finding.Decision != analysis.DecisionScored || finding.Grade != nil && *finding.Grade == analysis.GradeAvoid {
		return false
	}
	return finding.Score != nil && *finding.Score >= 80 || finding.Score == nil && finding.Grade == nil
}
