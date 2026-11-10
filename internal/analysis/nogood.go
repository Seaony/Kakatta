package analysis

import (
	"encoding/json"
	"sort"

	"github.com/Seaony/Kakatta/internal/locales"
)

func NoGoodReasons(recent []RecentScoredFinding) []ReasonCount {
	findings := make([]RecentScoredFinding, 0, len(recent))
	for _, finding := range recent {
		if !finding.IsBaseline && finding.DecidedLayer == 3 && (finding.Result.Decision == DecisionScored || finding.Result.Decision == DecisionRejected) {
			findings = append(findings, finding)
		}
	}
	sort.SliceStable(findings, func(i, j int) bool { return findings[i].AnalyzedAt > findings[j].AnalyzedAt })
	if len(findings) < 30 {
		return []ReasonCount{}
	}
	findings = findings[:30]
	for _, finding := range findings {
		if finding.Result.Grade != nil && (*finding.Result.Grade == GradeGood || *finding.Result.Grade == GradeSteal) {
			return []ReasonCount{}
		}
	}
	counts := []ReasonCount{}
	indices := map[string]int{}
	for _, finding := range findings {
		reasons := []locales.Message{}
		r := finding.Result
		if r.Decision == DecisionRejected {
			reasons = append(reasons, r.Reason)
		}
		reasons = append(reasons, r.Breakdown.Gates...)
		for _, cap := range r.Breakdown.Caps {
			// 不建议的原因已经计入，最高39只是同一门槛的结果。
			if cap.Reason.Code == "analysis.avoid_cap" && len(r.Breakdown.Gates) > 0 {
				continue
			}
			reasons = append(reasons, cap.Reason)
		}
		for _, item := range r.Breakdown.Items {
			if item.Value == -5 {
				reasons = append(reasons, item.Reason)
			}
		}
		if r.Breakdown.Base != nil && r.Breakdown.Base.Score < 70 {
			reasons = append(reasons, message("analysis.price_not_lower", nil))
		}
		if r.Decision == DecisionScored && r.Breakdown.Base == nil {
			reasons = append(reasons, message("analysis.no_fair_price", nil))
		}
		seen := map[string]bool{}
		for _, reason := range reasons {
			if reason.Code == "" {
				continue
			}
			key, err := json.Marshal(reason)
			if err != nil {
				continue
			}
			if seen[string(key)] {
				continue
			}
			seen[string(key)] = true
			if index, ok := indices[string(key)]; ok {
				counts[index].Count++
			} else {
				indices[string(key)] = len(counts)
				counts = append(counts, ReasonCount{Reason: valuationCopyMessage(reason), Count: 1})
			}
		}
	}
	sort.SliceStable(counts, func(i, j int) bool { return counts[i].Count > counts[j].Count })
	return counts[:min(3, len(counts))]
}
