package notification

import (
	"errors"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
)

func decideFirst(task service.Task, value candidate, hasEnabledChannel bool) (firstDecision, error) {
	finding := value.Finding
	if finding.NotifyKind != nil || finding.NotifySkipReason != nil {
		return firstDecision{}, nil
	}
	if !concluded(finding.Decision) {
		if finding.Decision == service.DecisionPending || finding.Decision == service.DecisionFailed || finding.Decision == service.DecisionSkipped {
			return firstDecision{}, nil
		}
		return firstDecision{}, errors.New("发现的判断状态无效")
	}
	if err := validateNotificationFacts(task, finding); err != nil {
		return firstDecision{}, err
	}
	reached, err := reachesRecommendation(task, value)
	if err != nil {
		return firstDecision{}, err
	}
	out := firstDecision{Apply: true}
	if reached && finding.RecommendedPrice == nil {
		out.SetRecommendedPrice = true
		out.RecommendedPrice = copyPrice(value.Listing.Price)
	}
	var reason *locales.Message
	switch {
	case finding.IsBaseline:
		reason = notificationReason("notify.baseline", nil)
	case task.NotifyPolicy == "none":
		reason = notificationReason("notify.policy_none", nil)
	case value.Listing.IsDismissed:
		reason = notificationReason("notify.dismissed", nil)
	case value.SellerBlocked:
		reason = notificationReason("notify.blocked", nil)
	case rejectedAs(finding, analysis.RejectFilter, analysis.RejectExcluded, analysis.RejectJunk):
		reason = copyNotificationReason(finding.Reason)
	case isAvoid(finding):
		reason = copyNotificationReason(finding.Reason)
	case exceedsBudget(task, value.Listing):
		reason = notificationReason("notify.over_budget", map[string]any{
			"price": formatNotificationMoney(*value.Listing.Price), "budget": formatNotificationMoney(*task.BudgetMax),
		})
	case value.Listing.Price == nil && task.BudgetMax != nil:
		reason = notificationReason("notify.price_unknown_budget", nil)
	case rejectedAs(finding, analysis.RejectUnmatched) && task.NotifyPolicy != "all_new":
		reason = copyNotificationReason(finding.Reason)
	case task.JudgeMode == analysis.ModeAI && !reached && task.NotifyPolicy != "all_new":
		if finding.Score == nil {
			reason = notificationReason("notify.no_comparison_steal", nil)
		} else {
			reason = notificationReason("notify.below_"+task.NotifyPolicy, map[string]any{"score": *finding.Score})
		}
	case rejectedAs(finding, analysis.RejectKeywordMiss) && task.NotifyPolicy == "recommended":
		reason = notificationReason("notify.keyword_miss", nil)
	case !hasEnabledChannel:
		reason = notificationReason("notify.no_channel", nil)
	case value.ReanalysisOrigin:
		reason = notificationReason("notify.reanalysis", nil)
	}
	if reason != nil {
		out.SkipReason = reason
		return out, nil
	}
	kind := string(intentRecommended)
	if !reached {
		if task.NotifyPolicy != "all_new" {
			return firstDecision{}, errors.New("发现没有明确的通知判定")
		}
		kind = string(intentNew)
	}
	out.Kind = &kind
	return out, nil
}

func concluded(decision analysis.Decision) bool {
	return decision == analysis.DecisionScored || decision == analysis.DecisionMatched || decision == analysis.DecisionRejected
}

func validateNotificationFacts(task service.Task, finding service.Finding) error {
	if finding.Stage != service.StageDone {
		return errors.New("已有结论的发现还停留在处理阶段")
	}
	switch task.JudgeMode {
	case analysis.ModeAI:
		switch task.NotifyPolicy {
		case "steal", "good", "fair", "all_new", "none":
		default:
			return errors.New("分析任务的推送档位无效")
		}
	case analysis.ModeKeyword:
		switch task.NotifyPolicy {
		case "recommended", "all_new", "none":
		default:
			return errors.New("关键词任务的推送档位无效")
		}
	default:
		return errors.New("任务的判断方式无效")
	}
	switch finding.Decision {
	case analysis.DecisionScored:
		if task.JudgeMode != analysis.ModeAI || finding.RejectKind != nil {
			return errors.New("算分结论与当前判断方式不一致")
		}
		if finding.Score == nil {
			if finding.Grade != nil && *finding.Grade != analysis.GradeAvoid {
				return errors.New("没有分数却保存了比价档位")
			}
		} else {
			if *finding.Score < 0 || *finding.Score > 100 || finding.Grade == nil ||
				*finding.Grade != analysis.GradeAvoid && *finding.Grade != gradeForScore(*finding.Score) {
				return errors.New("分数与档位不一致")
			}
		}
		if isAvoid(finding) && (finding.Reason == nil || finding.Reason.Code == "") {
			return errors.New("不建议结论缺少原因")
		}
	case analysis.DecisionMatched:
		if task.JudgeMode != analysis.ModeKeyword || finding.RejectKind != nil || finding.Score != nil || finding.Grade != nil {
			return errors.New("关键词命中结论与当前判断方式不一致")
		}
	case analysis.DecisionRejected:
		if finding.RejectKind == nil || finding.Reason == nil || finding.Reason.Code == "" {
			return errors.New("拒绝结论缺少类型或原因")
		}
		switch *finding.RejectKind {
		case analysis.RejectFilter, analysis.RejectExcluded, analysis.RejectJunk:
		case analysis.RejectUnmatched:
			if task.JudgeMode != analysis.ModeAI {
				return errors.New("不符合需求结论与当前判断方式不一致")
			}
		case analysis.RejectKeywordMiss:
			if task.JudgeMode != analysis.ModeKeyword {
				return errors.New("未命中结论与当前判断方式不一致")
			}
		default:
			return errors.New("拒绝结论的类型无效")
		}
	default:
		return errors.New("发现尚未得出结论")
	}
	return nil
}

func reachesRecommendation(task service.Task, value candidate) (bool, error) {
	finding := value.Finding
	if value.Listing.Price != nil && *value.Listing.Price < 0 || task.BudgetMax != nil && *task.BudgetMax < 0 {
		return false, errors.New("通知价格或预算不能为负数")
	}
	if value.Listing.IsDismissed || value.SellerBlocked || finding.Decision == analysis.DecisionRejected || isAvoid(finding) || exceedsBudget(task, value.Listing) {
		return false, nil
	}
	if value.Listing.Price == nil && task.BudgetMax != nil {
		return false, nil
	}
	if task.JudgeMode == analysis.ModeKeyword {
		return finding.Decision == analysis.DecisionMatched, nil
	}
	if finding.Decision != analysis.DecisionScored {
		return false, nil
	}
	if finding.Score == nil {
		return task.NotifyPolicy == "good" || task.NotifyPolicy == "fair" || task.NotifyPolicy == "all_new", nil
	}
	threshold := int64(80)
	if task.NotifyPolicy == "steal" {
		threshold = 90
	} else if task.NotifyPolicy == "fair" {
		threshold = 65
	}
	return *finding.Score >= threshold, nil
}

func revalidateReanalysisIntent(task service.Task, value candidate, pending intent, hasEnabledChannel bool) (bool, *locales.Message, error) {
	if !concluded(value.Finding.Decision) {
		return false, nil, nil
	}
	value.Finding.NotifyKind, value.Finding.NotifySkipReason = nil, nil
	value.Finding.IsBaseline, value.ReanalysisOrigin = false, false
	decision, err := decideFirst(task, value, hasEnabledChannel)
	if err != nil {
		return false, nil, err
	}
	if decision.SkipReason != nil {
		return false, decision.SkipReason, nil
	}
	if pending.Kind == intentPriceUpgrade && !task.NotifyPriceDrop {
		return false, notificationReason("notify.cancelled_policy", nil), nil
	}
	if pending.Kind == intentNew {
		if task.NotifyPolicy == "all_new" {
			return true, nil, nil
		}
		return false, notificationReason("notify.cancelled_policy", nil), nil
	}
	if pending.Kind != intentRecommended && pending.Kind != intentPriceUpgrade {
		return false, nil, errors.New("重新分析的通知意图种类无效")
	}
	if decision.Kind != nil && *decision.Kind == string(intentRecommended) {
		return true, nil, nil
	}
	if value.Finding.Decision == analysis.DecisionRejected {
		return false, copyNotificationReason(value.Finding.Reason), nil
	}
	if value.Finding.Score != nil {
		return false, notificationReason("notify.reanalysis_below_recommendation", map[string]any{"score": *value.Finding.Score}), nil
	}
	return false, nil, errors.New("重新分析的结论没有明确的通知决定")
}

func gradeForScore(score int64) analysis.Grade {
	switch {
	case score >= 90:
		return analysis.GradeSteal
	case score >= 80:
		return analysis.GradeGood
	case score >= 65:
		return analysis.GradeFair
	case score >= 50:
		return analysis.GradePricey
	default:
		return analysis.GradeAvoid
	}
}

func isAvoid(finding service.Finding) bool {
	return finding.Grade != nil && *finding.Grade == analysis.GradeAvoid
}

func rejectedAs(finding service.Finding, kinds ...analysis.RejectKind) bool {
	if finding.Decision != analysis.DecisionRejected || finding.RejectKind == nil {
		return false
	}
	for _, kind := range kinds {
		if *finding.RejectKind == kind {
			return true
		}
	}
	return false
}

func exceedsBudget(task service.Task, listing service.Listing) bool {
	return task.BudgetMax != nil && listing.Price != nil && *listing.Price > *task.BudgetMax
}

func copyPrice(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func notificationReason(code string, args map[string]any) *locales.Message {
	if args == nil {
		args = map[string]any{}
	}
	return &locales.Message{Code: code, Args: args}
}

func copyNotificationReason(reason *locales.Message) *locales.Message {
	if reason == nil {
		return nil
	}
	args := make(map[string]any, len(reason.Args))
	for key, value := range reason.Args {
		args[key] = value
	}
	return notificationReason(reason.Code, args)
}
