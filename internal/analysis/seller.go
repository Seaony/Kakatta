package analysis

import "github.com/Seaony/Kakatta/internal/locales"

func JudgeSeller(in SellerInput, verifiedSignals []SellerSignal, keyword string) SellerResult {
	out := SellerResult{Type: SellerUnknown, Checks: []EffectiveCheck{}, Reasons: []locales.Message{}}
	statuses := map[string]CheckStatus{"seller_dealer": CheckUnknown, "seller_new": CheckUnknown, "seller_bad_reviews": CheckUnknown, "seller_partial_refund": CheckUnknown}
	evidence := map[string]*string{}
	if in.RegistrationDays != nil && *in.RegistrationDays > 0 {
		statuses["seller_new"] = CheckOK
		if *in.RegistrationDays < 30 {
			statuses["seller_new"] = CheckRisk
			out.Reasons = append(out.Reasons, message("analysis.seller_new", map[string]any{"days": *in.RegistrationDays, "count": *in.RegistrationDays}))
		}
	}
	if in.Available {
		out.Type = SellerPersonal
		statuses["seller_dealer"] = CheckOK
		if in.RatesAvailable {
			statuses["seller_bad_reviews"], statuses["seller_partial_refund"] = CheckOK, CheckOK
		}
		matching := 0
		for _, item := range in.RecentItems[:min(len(in.RecentItems), 20)] {
			if item.OnSale && keyword != "" && len(MatchingKeywords(item.Title, []string{keyword})) > 0 {
				matching++
			}
		}
		if matching >= 3 {
			out.Type, statuses["seller_dealer"] = SellerDealerSuspect, CheckRisk
			out.Reasons = append(out.Reasons, message("analysis.seller_dealer_items", map[string]any{"count": matching}))
		}
		seen := map[string]bool{}
		for _, signal := range verifiedSignals {
			if seen[signal.Key] {
				continue
			}
			seen[signal.Key] = true
			switch signal.Key {
			case "dealer_words":
				out.Type, statuses["seller_dealer"] = SellerDealerSuspect, CheckRisk
				evidence["seller_dealer"] = ptr(signal.Evidence)
				out.Reasons = append(out.Reasons, message("analysis.seller_dealer_words", map[string]any{"evidence": signal.Evidence}))
			case "desc_mismatch", "after_sale", "slow_ship":
				statuses["seller_bad_reviews"] = CheckRisk
				if evidence["seller_bad_reviews"] == nil {
					evidence["seller_bad_reviews"] = ptr(signal.Evidence)
				}
				out.Reasons = append(out.Reasons, message("analysis.seller_bad_reviews", map[string]any{"evidence": signal.Evidence}))
			case "partial_refund":
				statuses["seller_partial_refund"] = CheckRisk
				evidence["seller_partial_refund"] = ptr(signal.Evidence)
				out.Reasons = append(out.Reasons, message("analysis.seller_partial_refund", map[string]any{"evidence": signal.Evidence}))
			}
		}
		bad := false
		for _, rate := range in.RecentRates[:min(len(in.RecentRates), 10)] {
			bad = bad || rate.AsSeller && rate.Negative
		}
		out.BonusEligible = in.RatesAvailable && out.Type == SellerPersonal && in.CreditExcellent != nil && *in.CreditExcellent && !bad
	} else {
		out.Reasons = append(out.Reasons, message("analysis.seller_unavailable", nil))
	}
	for _, key := range []string{"seller_dealer", "seller_new", "seller_bad_reviews", "seller_partial_refund"} {
		source := EvidenceProgram
		if statuses[key] == CheckUnknown {
			source = EvidenceUnknown
		}
		out.Checks = append(out.Checks, EffectiveCheck{Key: key, Status: statuses[key], Source: source, Evidence: clonePtr(evidence[key])})
	}
	return out
}
