package analysis

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/Seaony/Kakatta/internal/locales"
)

func Score(in ScoreInput) ScoreResult {
	adjust := in.Facts.Adjust
	adjust.Value = max(-15, min(8, adjust.Value))
	if strings.TrimSpace(adjust.Reason) == "" {
		adjust.Value = 0
	}
	out := ScoreResult{Decision: DecisionScored, Breakdown: ScoreBreakdown{Items: []ScoreItem{}, AI: adjust, Caps: []ScoreCap{}, Gates: []locales.Message{}}, Checks: effectiveChecks(in), Questions: []ContactQuestion{}}
	out.Questions = ContactQuestions(in.Profile, out.Checks)
	mustReason := valuationUnmet(in.Profile, in.Facts)
	out.Excluded = mustReason != nil
	if mismatch := valuationMismatch(in.Profile, in.Item.Specs); mismatch != nil {
		out.Decision, out.RejectKind, out.Reason = DecisionRejected, RejectUnmatched, *mismatch
		return out
	}
	if mustReason != nil {
		out.Decision, out.RejectKind, out.Reason = DecisionRejected, RejectUnmatched, *mustReason
		return out
	}
	if in.Price != nil && *in.Price >= 0 && in.Fair != nil && in.Fair.Cents != nil && *in.Fair.Cents > 0 {
		d := (float64(*in.Fair.Cents) - float64(*in.Price)) / float64(*in.Fair.Cents)
		out.Breakdown.Base = &ScoreBase{Score: baseScore(d), D: d, FairPrice: *in.Fair.Cents, Sample: in.Fair.Sample, Basis: valuationCopyMessage(in.Fair.Basis)}
	}
	bonus := 0
	for _, requirement := range in.Profile.Bonuses {
		for _, fact := range in.Facts.Bonuses {
			if fact.ID == requirement.ID && revisionMatches(fact.Rev, requirement.Rev) && fact.Status == ReqMet {
				if bonus < 4 {
					out.Breakdown.Items = append(out.Breakdown.Items, ScoreItem{ID: requirement.ID, Name: requirement.Text, Value: 2, Source: "rule", Reason: message("analysis.bonus_met", map[string]any{"id": requirement.ID, "name": requirement.Text})})
					bonus += 2
				}
				break
			}
		}
	}
	if in.Seller.BonusEligible && in.Seller.Type == SellerPersonal {
		out.Breakdown.Items = append(out.Breakdown.Items, ScoreItem{ID: "seller_credit", Name: "个人信用极好", Value: 2, Source: "rule", Reason: message("analysis.seller_bonus", nil)})
	}
	resolved := ResolveRisks(in.Profile)
	for i, check := range out.Checks {
		risk := resolved[i]
		args := map[string]any{"id": risk.ID, "name": risk.Name, "attitude": string(risk.Attitude)}
		if risk.Attitude == Ignore || check.Status == CheckUnknown && strings.HasPrefix(check.Key, "seller_") {
			continue
		}
		strong := check.Source == EvidenceText || check.Source == EvidenceProgram || check.Source == EvidenceUser
		uncertain := check.Status == CheckSuspect || check.Status == CheckRisk && check.Source == EvidenceImage
		if check.Status == CheckRisk && strong && (risk.Attitude == Confirm || risk.Attitude == Reject) {
			out.Breakdown.Gates = append(out.Breakdown.Gates, message("analysis.risk_confirmed", args))
		}
		value := 0
		if risk.Attitude == Mind && check.Status == CheckRisk && strong {
			value = -5
		}
		if uncertain && (risk.Attitude == Mind || risk.Attitude == Confirm || risk.Attitude == Reject) {
			value = -2
		}
		if value != 0 {
			source := "rule"
			if check.Source == EvidenceUser {
				source = "user"
			}
			code := "analysis.risk_penalty"
			if uncertain {
				code = "analysis.risk_uncertain"
			}
			out.Breakdown.Items = append(out.Breakdown.Items, ScoreItem{ID: risk.ID, Name: risk.Name, Value: value, Source: source, Reason: message(code, args)})
		}
		if (risk.Attitude == Confirm || risk.Attitude == Reject) && uncertain {
			out.Breakdown.Caps = append(out.Breakdown.Caps, ScoreCap{Limit: 89, Reason: message("analysis.risk_uncertain", args)})
		}
		if risk.Attitude == Confirm && check.Status == CheckUnknown {
			out.Breakdown.Caps = append(out.Breakdown.Caps, ScoreCap{Limit: 89, Reason: message("analysis.risk_unknown", args)})
		}
	}
	low := out.Breakdown.Base != nil && out.Breakdown.Base.D > .35
	if low {
		limit := 84
		if in.Facts.PriceNote != nil && strings.TrimSpace(*in.Facts.PriceNote) != "" {
			limit = 94
		}
		out.Breakdown.Caps = append(out.Breakdown.Caps, ScoreCap{Limit: limit, Reason: message("analysis.abnormally_low", map[string]any{"limit": limit})})
		newSeller := false
		for _, check := range in.Seller.Checks {
			if check.Key == "seller_new" && check.Status == CheckRisk {
				newSeller = true
			}
		}
		for _, check := range out.Checks {
			if check.Key == "seller_new" {
				newSeller = check.Status == CheckRisk
				break
			}
		}
		if newSeller {
			for _, check := range out.Checks {
				if (check.Key == "off_platform" || check.Key == "deposit") && (check.Status == CheckRisk || check.Status == CheckSuspect) {
					out.Breakdown.Gates = append(out.Breakdown.Gates, message("analysis.scam_combination", map[string]any{"id": check.Key}))
					break
				}
			}
		}
	}
	if len(out.Breakdown.Gates) > 0 {
		out.Breakdown.Caps = append(out.Breakdown.Caps, ScoreCap{Limit: 39, Reason: message("analysis.avoid_cap", nil)})
	}
	if out.Breakdown.Base == nil {
		out.Reason = message("analysis.no_fair_price", nil)
		if len(out.Breakdown.Gates) > 0 {
			out.Grade = ptr(GradeAvoid)
			out.Reason = valuationCopyMessage(out.Breakdown.Gates[0])
		}
		return out
	}
	total := out.Breakdown.Base.Score + float64(adjust.Value)
	for _, item := range out.Breakdown.Items {
		total += float64(item.Value)
	}
	score := max(0, min(100, int(math.Round(total))))
	for _, cap := range out.Breakdown.Caps {
		score = min(score, cap.Limit)
	}
	grade := scoreGrade(score)
	out.Score, out.Grade = ptr(score), ptr(grade)
	out.Reason = message("analysis.scored", map[string]any{"score": score, "grade": string(grade)})
	if len(out.Breakdown.Caps) > 0 {
		out.Reason = valuationCopyMessage(out.Breakdown.Caps[0].Reason)
	}
	if len(out.Breakdown.Gates) > 0 {
		out.Reason = valuationCopyMessage(out.Breakdown.Gates[0])
	}
	return out
}

func baseScore(d float64) float64 {
	switch {
	case d <= -.30:
		return 30
	case d < -.10:
		return 30 + (d+.30)/.20*19
	case d <= .10:
		return 50 + (d+.10)/.20*19
	case d <= .20:
		return 70 + (d-.10)/.10*14
	case d <= .35:
		return 85 + (d-.20)/.15*9
	default:
		return 84
	}
}
func scoreGrade(score int) Grade {
	switch {
	case score >= 90:
		return GradeSteal
	case score >= 80:
		return GradeGood
	case score >= 65:
		return GradeFair
	case score >= 50:
		return GradePricey
	default:
		return GradeAvoid
	}
}
func valuationMismatch(p Profile, values []SpecValue) *locales.Message {
	for _, spec := range p.Specs {
		if len(spec.Values) == 0 {
			continue
		}
		for _, value := range values {
			if value.Name != spec.Name || value.Value == nil {
				continue
			}
			v := configText(*value.Value)
			known, selected := false, false
			for _, option := range spec.Options {
				known = known || configText(option) == v
			}
			for _, accepted := range spec.Values {
				selected = selected || configText(accepted) == v
			}
			if known && !selected {
				return ptr(message("analysis.spec_mismatch", map[string]any{"name": spec.Name, "value": v}))
			}
			break
		}
	}
	return nil
}
func valuationUnmet(p Profile, facts Facts) *locales.Message {
	for _, requirement := range p.Musts {
		for _, fact := range facts.Musts {
			if fact.ID == requirement.ID && revisionMatches(fact.Rev, requirement.Rev) && fact.Status == ReqUnmet && fact.Source != nil && *fact.Source == "text" && fact.Evidence != nil && strings.TrimSpace(*fact.Evidence) != "" {
				return ptr(message("analysis.must_unmet", map[string]any{"id": requirement.ID, "name": requirement.Text}))
			}
		}
	}
	return nil
}
func effectiveChecks(in ScoreInput) []EffectiveCheck {
	out := make([]EffectiveCheck, 0, len(in.Profile.Risks))
	for _, risk := range in.Profile.Risks {
		check := EffectiveCheck{Key: risk.ID, Status: CheckUnknown, Source: EvidenceUnknown}
		if strings.HasPrefix(risk.ID, "seller_") {
			for _, known := range in.Seller.Checks {
				if known.Key == risk.ID && validCheck(known.Status) {
					check = known
					check.Evidence = clonePtr(known.Evidence)
					break
				}
			}
		} else {
			for _, fact := range in.Facts.Checks {
				if fact.Key != risk.ID || !revisionMatches(fact.Rev, risk.Rev) || !validCheck(fact.Status) {
					continue
				}
				check.Status, check.Evidence = fact.Status, clonePtr(fact.Evidence)
				if fact.Source != nil && (*fact.Source == "text" || *fact.Source == "image") && fact.Evidence != nil && strings.TrimSpace(*fact.Evidence) != "" {
					check.Source = EvidenceOrigin(*fact.Source)
				}
				if check.Source == EvidenceUnknown {
					if check.Status == CheckRisk {
						check.Status = CheckSuspect
					} else if check.Status == CheckOK {
						check.Status = CheckUnknown
					}
				}
				break
			}
		}
		if override, ok := in.Overrides.Risks[risk.ID]; ok && revisionMatches(override.Rev, risk.Rev) && validCheck(override.Status) {
			check.Status, check.Source, check.Evidence = override.Status, EvidenceUser, nil
		}
		if check.Status == CheckUnknown {
			check.Source, check.Evidence = EvidenceUnknown, nil
		}
		out = append(out, check)
	}
	return out
}

func ContactQuestions(profile Profile, checks []EffectiveCheck) []ContactQuestion {
	out := []ContactQuestion{}
	for _, attitude := range []Attitude{Confirm, Reject, Mind} {
		for _, risk := range ResolveRisks(profile) {
			if risk.Attitude != attitude {
				continue
			}
			status := CheckUnknown
			for _, check := range checks {
				if check.Key == risk.ID && validCheck(check.Status) {
					status = check.Status
					break
				}
			}
			if status != CheckUnknown && status != CheckSuspect || status == CheckUnknown && strings.HasPrefix(risk.ID, "seller_") {
				continue
			}
			var question locales.Message
			if _, ok := dictionaryRisk(risk.ID); ok {
				if risk.Question == "" {
					continue
				}
				question = message("analysis.question."+risk.ID, nil)
			} else if strings.TrimSpace(risk.Question) == "" {
				question = message("analysis.question_about", map[string]any{"name": risk.Name})
			} else {
				question = message("analysis.question_custom", map[string]any{"question": risk.Question})
			}
			out = append(out, ContactQuestion{Key: risk.ID, Question: question})
		}
	}
	return out
}

func valuationCopyMessage(m locales.Message) locales.Message {
	args := make(map[string]any, len(m.Args))
	for key, value := range m.Args {
		args[key] = valuationCopyArg(value)
	}
	return locales.Message{Code: m.Code, Args: args}
}
func valuationCopyArg(v any) any {
	switch value := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for k, item := range value {
			out[k] = valuationCopyArg(item)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = valuationCopyArg(item)
		}
		return out
	case []string:
		return append([]string(nil), value...)
	case json.RawMessage:
		return append(json.RawMessage(nil), value...)
	default:
		return v
	}
}
