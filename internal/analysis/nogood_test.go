package analysis

import "testing"

func recentNoGood(n int) []RecentScoredFinding {
	r := make([]RecentScoredFinding, n)
	for i := range r {
		r[i] = RecentScoredFinding{DecidedLayer: 3, AnalyzedAt: int64(i), Result: ScoreResult{Decision: DecisionScored, Breakdown: ScoreBreakdown{Base: &ScoreBase{Score: 60}}}}
	}
	return r
}
func TestNoGoodRequiresThirtyRecentFinalNonBaselineFindings(t *testing.T) {
	if got := NoGoodReasons(recentNoGood(29)); len(got) != 0 {
		t.Fatal("hint before 30")
	}
	r := recentNoGood(31)
	r[30].IsBaseline = true
	if got := NoGoodReasons(r); len(got) != 1 || got[0].Count != 30 {
		t.Fatalf("count=%+v", got)
	}
	r[29].Result.Grade = ptr(GradeGood)
	if got := NoGoodReasons(r); len(got) != 0 {
		t.Fatal("good did not clear hint")
	}
	r = recentNoGood(31)
	r[0].Result.Grade = ptr(GradeSteal)
	if got := NoGoodReasons(r); len(got) == 0 {
		t.Fatal("old out-of-window steal cleared hint")
	}
	r[30].DecidedLayer = 2
	if got := NoGoodReasons(r); len(got) != 0 {
		t.Fatal("layer 2 entered recent 30 and hid steal")
	}
}
func TestNoGoodCountsReasonsOnceAndSortsTopThree(t *testing.T) {
	r := recentNoGood(30)
	reason := message("analysis.risk_unknown", map[string]any{"id": "id_lock", "name": "ID锁", "attitude": "confirm"})
	for i := range r {
		r[i].Result.Breakdown.Caps = []ScoreCap{{Limit: 89, Reason: reason}, {Limit: 89, Reason: reason}}
		if i < 20 {
			r[i].Result.Breakdown.Items = []ScoreItem{{Value: -5, Reason: message("analysis.risk_penalty", map[string]any{"id": "repaired", "name": "维修", "attitude": "mind"})}}
		}
	}
	got := NoGoodReasons(r)
	if len(got) != 3 || got[0].Count != 30 || got[1].Count != 30 || got[2].Count != 20 {
		t.Fatalf("counts=%+v", got)
	}
	got[0].Reason.Args["id"] = "changed"
	if reason.Args["id"] != "id_lock" {
		t.Fatal("reason aliases input")
	}
}

func TestNoGoodDoesNotInventAnAIAdjustmentReason(t *testing.T) {
	recent := recentNoGood(30)
	for i := range recent {
		recent[i].Result.Grade = ptr(GradeFair)
		recent[i].Result.Breakdown.Base.Score = 85
		recent[i].Result.Breakdown.AI = Adjustment{Value: -10, Reason: "综合判断"}
	}
	if got := NoGoodReasons(recent); len(got) != 0 {
		t.Fatalf("不应新增规格没有规定的AI扣分原因：%+v", got)
	}
}
