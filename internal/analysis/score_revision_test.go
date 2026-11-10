package analysis

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestScoreMustAndBonusRequireCurrentRevision(t *testing.T) {
	for _, saved := range []int64{-1, 0, 1, 2, 3} {
		t.Run(fmt.Sprint(saved), func(t *testing.T) {
			in := valuationInput()
			in.Profile.Musts = []Requirement{{ID: "m1", Text: "日版", Rev: 2}}
			in.Facts.Musts = []RequirementFact{{ID: "m1", Status: ReqUnmet, Source: ptr("text"), Evidence: ptr("非国行"), Rev: saved}}
			got := Score(in)
			if saved == 2 {
				if got.Decision != DecisionRejected || !got.Excluded || got.Score != nil {
					t.Fatalf("current unmet ignored: %+v", got)
				}
			} else if got.Decision != DecisionScored || got.Excluded || got.Score == nil || *got.Score != 94 {
				t.Fatalf("stale unmet reused: %+v", got)
			}
			in = valuationInput()
			in.Profile.Bonuses = []Requirement{{ID: "b1", Text: "两年保修", Rev: 2}}
			in.Facts.Bonuses = []RequirementFact{{ID: "b1", Status: ReqMet, Rev: saved}}
			got = valuationScore(t, in)
			want := 94
			if saved == 2 {
				want = 96
			}
			if *got.Score != want {
				t.Fatalf("bonus revision %d score=%d want %d", saved, *got.Score, want)
			}
		})
	}
}

func TestScoreStaleRiskBecomesUnknownUnderCurrentAttitude(t *testing.T) {
	for _, attitude := range []Attitude{Confirm, Reject, Mind, Ignore} {
		for _, saved := range []int64{-1, 0, 1, 2, 3} {
			t.Run(string(attitude)+"/"+fmt.Sprint(saved), func(t *testing.T) {
				in := valuationInput()
				in.Profile.Risks = []ProfileRisk{{ID: "c1", Name: "新问题", Attitude: attitude, Rev: 2}}
				in.Facts.Checks = []CheckFact{{Key: "c1", Status: CheckRisk, Source: ptr("text"), Evidence: ptr("旧问题有风险"), Rev: saved}}
				got := valuationScore(t, in)
				if saved == 2 {
					if got.Checks[0].Status != CheckRisk {
						t.Fatalf("current risk dropped: %+v", got)
					}
					return
				}
				want := 94
				if attitude == Confirm {
					want = 89
				}
				if *got.Score != want || got.Checks[0].Status != CheckUnknown || got.Checks[0].Evidence != nil || len(got.Breakdown.Gates) != 0 {
					t.Fatalf("stale risk reused: %+v", got)
				}
				questionCount := 1
				if attitude == Ignore {
					questionCount = 0
				}
				if len(got.Questions) != questionCount {
					t.Fatalf("unknown questions=%+v", got.Questions)
				}
			})
		}
	}
}

func TestScoreRiskOverrideMustMatchCurrentRevision(t *testing.T) {
	for _, saved := range []int64{-1, 0, 1, 2, 3} {
		t.Run(fmt.Sprint(saved), func(t *testing.T) {
			in := valuationInput()
			in.Profile.Risks = []ProfileRisk{{ID: "id_lock", Attitude: Reject, Rev: 2}}
			in.Facts.Checks = []CheckFact{{Key: "id_lock", Status: CheckRisk, Source: ptr("text"), Evidence: ptr("账号未退"), Rev: 2}}
			in.Overrides.Risks = map[string]RiskOverride{"id_lock": {Status: CheckOK, Rev: saved}}
			got := valuationScore(t, in)
			want := 39
			if saved == 2 {
				want = 94
			}
			if *got.Score != want {
				t.Fatalf("override revision %d score=%d want %d", saved, *got.Score, want)
			}
			if saved == 2 && got.Checks[0].Source != EvidenceUser {
				t.Fatalf("current override not adopted: %+v", got.Checks)
			}
		})
	}
	in := valuationInput()
	in.Profile.Risks = []ProfileRisk{{ID: "c1", Name: "新增问题", Attitude: Reject, Rev: 2}}
	in.Facts.Checks = []CheckFact{{Key: "c1", Status: CheckOK, Source: ptr("text"), Evidence: ptr("旧问题没有"), Rev: 1}}
	in.Overrides.Risks = map[string]RiskOverride{"c1": {Status: CheckRisk, Rev: 2}}
	if got := valuationScore(t, in); *got.Score != 39 || got.Checks[0].Source != EvidenceUser {
		t.Fatalf("current override could not replace old fact: %+v", got)
	}
}

func TestScoreSellerProgramFactsRemainCurrentAndOverridesAreVersioned(t *testing.T) {
	in := valuationInput()
	in.Profile.Risks = []ProfileRisk{{ID: "seller_dealer", Attitude: Reject, Rev: 2}}
	in.Seller = SellerResult{Type: SellerDealerSuspect, Checks: []EffectiveCheck{{Key: "seller_dealer", Status: CheckRisk, Source: EvidenceProgram}}}
	in.Facts.Checks = []CheckFact{{Key: "seller_dealer", Status: CheckOK, Source: ptr("text"), Evidence: ptr("自用"), Rev: 2}}
	if got := valuationScore(t, in); *got.Score != 39 {
		t.Fatalf("program risk gated by facts revision: %+v", got)
	}
	in.Overrides.Risks = map[string]RiskOverride{"seller_dealer": {Status: CheckOK, Rev: 1}}
	if got := valuationScore(t, in); *got.Score != 39 {
		t.Fatalf("stale seller override cleared risk: %+v", got)
	}
	in.Overrides.Risks["seller_dealer"] = RiskOverride{Status: CheckOK, Rev: 2}
	if got := valuationScore(t, in); *got.Score != 94 || got.Checks[0].Source != EvidenceUser {
		t.Fatalf("current seller override ignored: %+v", got)
	}
}

func TestScoreScamCannotReadStaleOrUnlistedSellerOverride(t *testing.T) {
	for _, tc := range []struct {
		name             string
		profileHasSeller bool
		saved            int64
		want             int
	}{{"stale", true, 1, 39}, {"zero", true, 0, 39}, {"negative", true, -1, 39}, {"current", true, 2, 84}, {"unlisted", false, 2, 39}} {
		t.Run(tc.name, func(t *testing.T) {
			in := valuationInput()
			in.Price = ptr(int64(50000))
			in.Profile.Risks = []ProfileRisk{{ID: "off_platform", Attitude: Ignore, Rev: 1}}
			if tc.profileHasSeller {
				in.Profile.Risks = append(in.Profile.Risks, ProfileRisk{ID: "seller_new", Attitude: Ignore, Rev: 2})
			}
			in.Facts.Checks = []CheckFact{{Key: "off_platform", Status: CheckSuspect, Source: ptr("text"), Evidence: ptr("加微信聊"), Rev: 1}}
			in.Seller = JudgeSeller(SellerInput{RegistrationDays: ptr(5)}, nil, "ipad")
			in.Overrides.Risks = map[string]RiskOverride{"seller_new": {Status: CheckOK, Rev: tc.saved}}
			if got := valuationScore(t, in); *got.Score != tc.want {
				t.Fatalf("scam override bypassed current profile: %+v", got)
			}
		})
	}
}

func TestScoreRevisionCompatibilityAndInputPreservation(t *testing.T) {
	in := valuationInput()
	in.Profile.Musts = []Requirement{{ID: "m1", Text: "国行"}}
	in.Facts.Musts = []RequirementFact{{ID: "m1", Status: ReqUnmet, Source: ptr("text"), Evidence: ptr("非国行"), Rev: 1}}
	if got := Score(in); got.Decision != DecisionRejected || !got.Excluded {
		t.Fatalf("legacy profile revision not normalized: %+v", got)
	}
	in = valuationInput()
	in.Profile.Risks = []ProfileRisk{{ID: "c1", Name: "新问题", Attitude: Confirm, Rev: 2}}
	in.Profile.Musts = []Requirement{{ID: "m1", Text: "新条件", Rev: 2}}
	in.Profile.Bonuses = []Requirement{{ID: "b1", Text: "新加分", Rev: 2}}
	in.Facts.Musts = []RequirementFact{{ID: "m1", Status: ReqUnmet, Source: ptr("text"), Evidence: ptr("旧条件不符"), Rev: 1}}
	in.Facts.Bonuses = []RequirementFact{{ID: "b1", Status: ReqMet, Rev: 1}}
	in.Facts.Checks = []CheckFact{{Key: "c1", Status: CheckRisk, Source: ptr("text"), Evidence: ptr("旧问题有风险"), Rev: 1}}
	in.Overrides.Risks = map[string]RiskOverride{"c1": {Status: CheckOK, Rev: 1}}
	before, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	got := valuationScore(t, in)
	if *got.Score != 89 || got.Excluded {
		t.Fatalf("stale input adopted: %+v", got)
	}
	after, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("scoring rewrote stored facts or overrides\nbefore=%s\nafter=%s", before, after)
	}
}

func TestScoreStaleBonusDoesNotAddPoints(t *testing.T) {
	in := valuationInput()
	in.Profile.Bonuses = []Requirement{{ID: "b1", Text: "两年保修", Rev: 2}}
	in.Facts.Bonuses = []RequirementFact{{ID: "b1", Status: ReqMet, Rev: 1}}
	got := valuationScore(t, in)
	if *got.Score != 94 || len(got.Breakdown.Items) != 0 {
		t.Fatalf("old met became new bonus: %+v", got)
	}
}

func TestScoreStaleSafeFactDoesNotConfirmChangedRisk(t *testing.T) {
	in := valuationInput()
	in.Profile.Risks = []ProfileRisk{{ID: "c1", Name: "电池鼓包", Attitude: Confirm, Rev: 2}}
	in.Facts.Checks = []CheckFact{{Key: "c1", Status: CheckOK, Source: ptr("text"), Evidence: ptr("屏幕没有坏点"), Rev: 1}}
	got := valuationScore(t, in)
	if *got.Score != 89 || got.Checks[0].Status != CheckUnknown || len(got.Questions) != 1 {
		t.Fatalf("old ok confirmed different risk: %+v", got)
	}
}

func TestScoreRetainedOldAndCurrentFactsUseMatchingRevision(t *testing.T) {
	in := valuationInput()
	in.Profile.Musts = []Requirement{{ID: "m1", Text: "日版", Rev: 2}}
	in.Profile.Bonuses = []Requirement{{ID: "b1", Text: "两年保修", Rev: 2}}
	in.Profile.Risks = []ProfileRisk{{ID: "id_lock", Attitude: Reject, Rev: 2}}
	in.Facts.Musts = []RequirementFact{{ID: "m1", Status: ReqUnmet, Source: ptr("text"), Evidence: ptr("不是国行"), Rev: 1}, {ID: "m1", Status: ReqMet, Source: ptr("text"), Evidence: ptr("日版"), Rev: 2}}
	in.Facts.Bonuses = []RequirementFact{{ID: "b1", Status: ReqMet, Rev: 1}, {ID: "b1", Status: ReqUnmet, Rev: 2}}
	in.Facts.Checks = []CheckFact{{Key: "id_lock", Status: CheckOK, Source: ptr("text"), Evidence: ptr("没有旧问题"), Rev: 1}, {Key: "id_lock", Status: CheckRisk, Source: ptr("text"), Evidence: ptr("账号未退"), Rev: 2}}
	got := valuationScore(t, in)
	if got.Decision != DecisionScored || *got.Score != 39 || got.Excluded || len(got.Breakdown.Items) != 0 || got.Checks[0].Status != CheckRisk {
		t.Fatalf("old objects masked current facts: %+v", got)
	}
}
