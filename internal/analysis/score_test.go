package analysis

import (
	"fmt"
	"math"
	"testing"
)

func valuationInput() ScoreInput {
	return ScoreInput{Profile: Profile{}, Facts: Facts{}, Item: ItemSpecs{ConfigKey: ptr("*")}, Price: ptr(int64(65000)), Fair: &FairPrice{Cents: ptr(int64(100000)), Sample: 8}}
}
func valuationScore(t *testing.T, in ScoreInput) ScoreResult {
	t.Helper()
	got := Score(in)
	if got.Score == nil {
		t.Fatalf("missing score: %+v", got)
	}
	return got
}

func TestScoreAttitudeEvidenceMatrix(t *testing.T) {
	for _, attitude := range []Attitude{Confirm, Reject, Mind, Ignore} {
		for _, status := range []CheckStatus{CheckOK, CheckRisk, CheckSuspect, CheckUnknown} {
			for _, source := range []string{"text", "image"} {
				t.Run(string(attitude)+"/"+string(status)+"/"+source, func(t *testing.T) {
					in := valuationInput()
					in.Profile.Risks = []ProfileRisk{{ID: "id_lock", Attitude: attitude}}
					in.Facts.Checks = []CheckFact{{Key: "id_lock", Rev: 1, Status: status, Source: ptr(source), Evidence: ptr("明确说明")}}
					want := 94
					gate := false
					if attitude != Ignore {
						if status == CheckRisk && source == "text" {
							if attitude == Mind {
								want = 89
							} else {
								want = 39
								gate = true
							}
						}
						if status == CheckSuspect || status == CheckRisk && source == "image" {
							if attitude == Mind {
								want = 92
							} else {
								want = 89
							}
						}
						if status == CheckUnknown && attitude == Confirm {
							want = 89
						}
					}
					got := valuationScore(t, in)
					if *got.Score != want || (len(got.Breakdown.Gates) > 0) != gate {
						t.Fatalf("score=%d gates=%+v want %d gate=%t", *got.Score, got.Breakdown.Gates, want, gate)
					}
				})
			}
		}
	}
}

func TestScoreBaseEndpoints(t *testing.T) {
	for _, tc := range []struct {
		price   int64
		base    float64
		rounded int
	}{{140000, 30, 30}, {130000, 30, 30}, {120000, 39.5, 40}, {110000, 50, 50}, {100000, 59.5, 60}, {90000, 69, 69}, {85000, 77, 77}, {80000, 84, 84}, {75000, 88, 88}, {65000, 94, 94}, {64000, 84, 84}} {
		t.Run(fmt.Sprint(tc.price), func(t *testing.T) {
			in := valuationInput()
			in.Price = ptr(tc.price)
			got := valuationScore(t, in)
			if math.Abs(got.Breakdown.Base.Score-tc.base) > 1e-9 || *got.Score != tc.rounded {
				t.Fatalf("price %d base %g score %d", tc.price, got.Breakdown.Base.Score, *got.Score)
			}
		})
	}
}

func TestScoreGatesAndNoFairPrice(t *testing.T) {
	in := valuationInput()
	in.Profile.Risks = []ProfileRisk{{ID: "id_lock", Attitude: Reject}}
	in.Facts.Checks = []CheckFact{{Key: "id_lock", Rev: 1, Status: CheckRisk, Source: ptr("text"), Evidence: ptr("有ID锁")}}
	for _, unset := range []string{"fair", "price"} {
		t.Run(unset, func(t *testing.T) {
			copy := in
			if unset == "fair" {
				copy.Fair = nil
			} else {
				copy.Price = nil
			}
			got := Score(copy)
			if got.Score != nil || got.Grade == nil || *got.Grade != GradeAvoid {
				t.Fatalf("hard risk lost without %s: %+v", unset, got)
			}
		})
	}
	in.Profile.Risks = nil
	in.Fair = nil
	if got := Score(in); got.Score != nil || got.Grade != nil {
		t.Fatalf("no fair invented score: %+v", got)
	}
	in = valuationInput()
	in.Profile.Specs = []ProfileSpec{{Name: "存储", Options: []string{"128G", "256G"}, Values: []string{"256G"}}}
	in.Item.Specs = []SpecValue{{Name: "存储", Value: ptr("128G")}}
	got := Score(in)
	if got.Decision != DecisionRejected || got.RejectKind != RejectUnmatched || got.Excluded || got.Score != nil {
		t.Fatalf("spec gate=%+v", got)
	}
	in.Item.Specs = []SpecValue{{Name: "存储", Value: ptr("512G")}}
	in.Profile.Musts = []Requirement{{ID: "m1", Text: "国行"}}
	in.Facts.Musts = []RequirementFact{{ID: "m1", Rev: 1, Status: ReqUnmet, Source: ptr("image"), Evidence: ptr("第1张图")}}
	if got := Score(in); got.Decision == DecisionRejected {
		t.Fatalf("unknown option/image unmet rejected: %+v", got)
	}
	in.Facts.Musts[0].Source = ptr("text")
	in.Facts.Musts[0].Evidence = ptr("非国行")
	got = Score(in)
	if !got.Excluded || got.Score != nil || got.Decision != DecisionRejected {
		t.Fatalf("must gate=%+v", got)
	}
}

func TestScoreOverridesSellerUnknownCapsAndScam(t *testing.T) {
	in := valuationInput()
	in.Profile.Risks = []ProfileRisk{{ID: "seller_new", Attitude: Confirm}, {ID: "id_lock", Attitude: Reject}}
	in.Facts.Checks = []CheckFact{{Key: "seller_new", Rev: 1, Status: CheckRisk, Source: ptr("text"), Evidence: ptr("假的卖家事实")}, {Key: "id_lock", Rev: 1, Status: CheckRisk, Source: ptr("text"), Evidence: ptr("有锁")}}
	in.Overrides.Risks = map[string]RiskOverride{"id_lock": {Status: CheckOK, Rev: 1}}
	got := valuationScore(t, in)
	if *got.Score != 94 || len(got.Breakdown.Caps) != 0 {
		t.Fatalf("seller unknown or override mishandled: %+v", got)
	}
	in.Overrides.Risks["id_lock"] = RiskOverride{Status: CheckRisk, Rev: 1}
	got = valuationScore(t, in)
	if *got.Score != 39 || got.Checks[1].Source != EvidenceUser {
		t.Fatalf("user risk=%+v", got)
	}
	in = valuationInput()
	in.Price = ptr(int64(50000))
	in.Profile.Risks = []ProfileRisk{{ID: "off_platform", Attitude: Ignore}}
	in.Facts.Checks = []CheckFact{{Key: "off_platform", Rev: 1, Status: CheckSuspect, Source: ptr("text"), Evidence: ptr("加微信聊")}}
	in.Seller = JudgeSeller(SellerInput{RegistrationDays: ptr(5)}, nil, "ipad")
	if got := valuationScore(t, in); *got.Score != 39 {
		t.Fatalf("scam=%+v", got)
	}
	in.Seller = JudgeSeller(SellerInput{RegistrationDays: ptr(0)}, nil, "ipad")
	if got := valuationScore(t, in); *got.Score != 84 {
		t.Fatalf("unknown age counted new=%+v", got)
	}
	in.Facts.Adjust = Adjustment{Value: 8, Reason: "急出"}
	in.Facts.PriceNote = ptr("卖家急出")
	if got := valuationScore(t, in); *got.Score != 92 {
		t.Fatalf("verified low-price note ignored=%+v", got)
	}
}

func TestScoreAdjustBonusesAndIndependentResults(t *testing.T) {
	in := valuationInput()
	in.Profile.Bonuses = []Requirement{{ID: "b1", Text: "在保"}, {ID: "b2", Text: "配件齐全"}, {ID: "b3", Text: "有票"}}
	in.Facts.Bonuses = []RequirementFact{{ID: "b1", Rev: 1, Status: ReqMet}, {ID: "b2", Rev: 1, Status: ReqMet}, {ID: "b3", Rev: 1, Status: ReqMet}}
	in.Facts.ExtraFindings = []ExtraFinding{{Name: "鼓包", Evidence: "电池鼓包", Source: "text"}}
	in.Facts.Adjust = Adjustment{Value: 99, Reason: "配件齐全"}
	in.Seller = SellerResult{Type: SellerPersonal, BonusEligible: true}
	got := valuationScore(t, in)
	if *got.Score != 100 || len(got.Breakdown.Items) != 3 || got.Breakdown.AI.Value != 8 {
		t.Fatalf("bonus cap/adjust=%+v", got)
	}
	in.Facts.Adjust = Adjustment{Value: -99, Reason: "问题多"}
	if got := valuationScore(t, in); *got.Score != 85 {
		t.Fatalf("negative clamp=%+v", got)
	}
	in.Facts.Adjust.Reason = " "
	if got := valuationScore(t, in); *got.Score != 100 || got.Breakdown.AI.Value != 0 {
		t.Fatalf("empty reason=%+v", got)
	}
	in = valuationInput()
	in.Profile.Risks = []ProfileRisk{{ID: "id_lock", Attitude: Confirm}}
	in.Facts.Adjust = Adjustment{Value: 8, Reason: "配件齐"}
	got = valuationScore(t, in)
	if *got.Score != 89 {
		t.Fatal("adjust bypassed cap")
	}
	in.Fair.Basis = message("basis", map[string]any{"days": 14})
	got = Score(in)
	got.Breakdown.Base.Basis.Args["days"] = 30
	if in.Fair.Basis.Args["days"] != 14 {
		t.Fatal("mutated input basis")
	}
}

func TestContactQuestionsOrderCountAndUnknownSeller(t *testing.T) {
	p := Profile{Risks: []ProfileRisk{{ID: "c1", Name: "外观", Attitude: Mind}, {ID: "off_platform", Attitude: Reject}, {ID: "seller_dealer", Attitude: Confirm}, {ID: "c2", Name: "屏幕", Attitude: Confirm}, {ID: "water_damage", Attitude: Reject}, {ID: "id_lock", Attitude: Confirm}, {ID: "mdm", Attitude: Confirm}, {ID: "c3", Name: "划痕", Question: "有划痕吗？", Attitude: Mind}, {ID: "c4", Name: "不在意", Attitude: Ignore}}}
	checks := []EffectiveCheck{{Key: "c2", Status: CheckSuspect}, {Key: "seller_dealer", Status: CheckUnknown}}
	got := ContactQuestions(p, checks)
	want := []string{"c2", "id_lock", "mdm", "water_damage", "c1", "c3"}
	if len(got) != len(want) {
		t.Fatalf("count=%d %+v", len(got), got)
	}
	for i, key := range want {
		if got[i].Key != key {
			t.Fatalf("order %d=%s want %s", i, got[i].Key, key)
		}
	}
	for i := range checks {
		if checks[i].Key == "c2" {
			checks[i].Status = CheckRisk
			checks[i].Source = EvidenceImage
		}
	}
	got = ContactQuestions(p, checks)
	if len(got) != 5 {
		t.Fatal("image risk became a question")
	}
}

func TestScoreProgramAndUserEvidenceMatrix(t *testing.T) {
	for _, source := range []EvidenceOrigin{EvidenceProgram, EvidenceUser} {
		for _, attitude := range []Attitude{Confirm, Reject, Mind, Ignore} {
			for _, status := range []CheckStatus{CheckOK, CheckRisk, CheckSuspect, CheckUnknown} {
				t.Run(string(source)+"/"+string(attitude)+"/"+string(status), func(t *testing.T) {
					in := valuationInput()
					key := "id_lock"
					if source == EvidenceProgram {
						key = "seller_dealer"
						in.Seller.Checks = []EffectiveCheck{{Key: key, Status: status, Source: EvidenceProgram}}
					} else {
						in.Overrides.Risks = map[string]RiskOverride{key: {Status: status, Rev: 1}}
					}
					in.Profile.Risks = []ProfileRisk{{ID: key, Attitude: attitude}}
					want := 94
					if attitude != Ignore {
						if status == CheckRisk {
							if attitude == Mind {
								want = 89
							} else {
								want = 39
							}
						}
						if status == CheckSuspect {
							if attitude == Mind {
								want = 92
							} else {
								want = 89
							}
						}
						if status == CheckUnknown && attitude == Confirm && source == EvidenceUser {
							want = 89
						}
					}
					got := valuationScore(t, in)
					if *got.Score != want {
						t.Fatalf("score=%d want %d", *got.Score, want)
					}
				})
			}
		}
	}
}

func TestScoreInvalidSourceCannotBecomeConfirmedOrSafe(t *testing.T) {
	for _, source := range []*string{nil, ptr(""), ptr("script")} {
		in := valuationInput()
		in.Profile.Risks = []ProfileRisk{{ID: "id_lock", Attitude: Confirm}}
		in.Facts.Checks = []CheckFact{{Key: "id_lock", Rev: 1, Status: CheckRisk, Source: source, Evidence: ptr("有锁")}}
		in.Overrides.Risks = map[string]RiskOverride{"other": {Status: CheckOK, Rev: 1}, "id_lock": {Status: "invalid", Rev: 1}}
		got := valuationScore(t, in)
		if *got.Score != 89 || len(got.Breakdown.Gates) != 0 || got.Checks[0].Status != CheckSuspect {
			t.Fatalf("invalid source became confirmed/safe: %+v", got)
		}
	}
	in := valuationInput()
	in.Profile.Specs = []ProfileSpec{{Name: "型号", Options: []string{"A", "B"}, Values: []string{"A"}}}
	in.Item.Specs = []SpecValue{{Name: "型号", Value: ptr("B")}}
	in.Profile.Musts = []Requirement{{ID: "m1", Text: "国行"}}
	in.Facts.Musts = []RequirementFact{{ID: "m1", Rev: 1, Status: ReqUnmet, Source: ptr("text"), Evidence: ptr("非国行")}}
	got := Score(in)
	if !got.Excluded || got.Score != nil || got.Reason.Code != "analysis.spec_mismatch" {
		t.Fatalf("spec mismatch masked must exclusion: %+v", got)
	}
}

func TestScoreGradeThresholdsAndNoExtraFindingPenalty(t *testing.T) {
	for _, tc := range []struct {
		price  int64
		adjust int
		score  int
		grade  Grade
	}{{110000, -1, 49, GradeAvoid}, {110000, 0, 50, GradePricey}, {90000, -5, 64, GradePricey}, {90000, -4, 65, GradeFair}, {85000, 2, 79, GradeFair}, {85000, 3, 80, GradeGood}, {75000, 1, 89, GradeGood}, {75000, 2, 90, GradeSteal}} {
		in := valuationInput()
		in.Price = ptr(tc.price)
		in.Facts.Adjust = Adjustment{Value: tc.adjust, Reason: "综合判断"}
		got := valuationScore(t, in)
		if *got.Score != tc.score || *got.Grade != tc.grade {
			t.Fatalf("price=%d adjust=%d got=%+v", tc.price, tc.adjust, got)
		}
	}
	in := valuationInput()
	in.Facts.ExtraFindings = []ExtraFinding{{Name: "漏光", Evidence: "屏幕漏光", Source: "text"}}
	if got := valuationScore(t, in); *got.Score != 94 {
		t.Fatalf("extra finding separately deducted: %+v", got)
	}
	in.Fair.Cents = ptr(int64(0))
	if got := Score(in); got.Score != nil || got.Grade != nil {
		t.Fatalf("zero fair divided: %+v", got)
	}
}
