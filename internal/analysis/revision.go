package analysis

import (
	"strconv"

	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/locales"
)

func validateProfileRevisions(p Profile) error {
	for _, requirements := range [][]Requirement{p.Musts, p.Bonuses} {
		for _, r := range requirements {
			if r.Rev < 0 || r.Rev > limits.MaxSafeInteger {
				return ruleError("analysis.profile_invalid", map[string]any{"field": "rev"})
			}
		}
	}
	for _, r := range p.Risks {
		if r.Rev < 0 || r.Rev > limits.MaxSafeInteger {
			return ruleError("analysis.profile_invalid", map[string]any{"field": "rev"})
		}
	}
	return nil
}

// ReviseProfile 按已存清单比较含义，版本不由请求里的数字决定。
func ReviseProfile(old, proposed Profile) (Profile, []locales.Message, error) {
	warnings := []locales.Message{}
	if err := validateProfileRevisions(old); err != nil {
		return Profile{}, warnings, err
	}
	watermark, err := profileWatermark(old)
	if err != nil {
		return Profile{}, warnings, err
	}
	if int64(proposed.NextNo) < limits.MinSafeInteger || int64(proposed.NextNo) > limits.MaxSafeInteger {
		return Profile{}, warnings, ruleError("analysis.profile_invalid", map[string]any{"field": "next_no"})
	}
	oldIDs := map[string]bool{}
	for _, requirements := range [][]Requirement{old.Musts, old.Bonuses} {
		for _, r := range requirements {
			oldIDs[r.ID] = true
		}
	}
	for _, r := range old.Risks {
		oldIDs[r.ID] = true
	}
	checkID := func(id string) error {
		if id == "" || oldIDs[id] {
			return nil
		}
		if len(id) > 1 && (id[0] == 'm' || id[0] == 'b' || id[0] == 'c') {
			n, err := strconv.Atoi(id[1:])
			if err == nil && n > 0 && n < watermark {
				return ruleError("analysis.profile_invalid", map[string]any{"field": "id"})
			}
		}
		return nil
	}
	for _, requirements := range [][]Requirement{proposed.Musts, proposed.Bonuses} {
		for _, r := range requirements {
			if err := checkID(r.ID); err != nil {
				return Profile{}, warnings, err
			}
		}
	}
	for _, r := range proposed.Risks {
		if err := checkID(r.ID); err != nil {
			return Profile{}, warnings, err
		}
	}
	if proposed.NextNo < watermark {
		proposed.NextNo = watermark
	}
	// 请求的计数即使为负也不参与计算，清零时不能改到调用方的切片。
	proposed.Musts = append([]Requirement{}, proposed.Musts...)
	proposed.Bonuses = append([]Requirement{}, proposed.Bonuses...)
	proposed.Risks = append([]ProfileRisk{}, proposed.Risks...)
	for i := range proposed.Musts {
		proposed.Musts[i].Rev = 0
	}
	for i := range proposed.Bonuses {
		proposed.Bonuses[i].Rev = 0
	}
	for i := range proposed.Risks {
		proposed.Risks[i].Rev = 0
	}
	result, warnings, err := NormalizeProfile(proposed)
	if err != nil {
		return Profile{}, warnings, err
	}
	revise := func(previous int64, changed bool) (int64, error) {
		rev := profileRevision(previous)
		if changed {
			if rev == limits.MaxSafeInteger {
				return 0, ruleError("analysis.profile_invalid", map[string]any{"field": "rev"})
			}
			rev++
		}
		return rev, nil
	}
	requirements := map[string]Requirement{}
	for _, set := range [][]Requirement{old.Musts, old.Bonuses} {
		for _, r := range set {
			requirements[r.ID] = r
		}
	}
	for _, set := range [][]Requirement{result.Musts, result.Bonuses} {
		for i := range set {
			previous, exists := requirements[set[i].ID]
			set[i].Rev = 1
			if exists {
				rev, err := revise(previous.Rev, previous.Text != set[i].Text)
				if err != nil {
					return Profile{}, warnings, err
				}
				set[i].Rev = rev
			}
		}
	}
	risks := map[string]ProfileRisk{}
	for _, r := range old.Risks {
		risks[r.ID] = r
	}
	for i := range result.Risks {
		current := &result.Risks[i]
		previous, exists := risks[current.ID]
		current.Rev = 1
		if exists {
			_, dictionary := dictionaryRisk(current.ID)
			changed := !dictionary && (previous.Name != current.Name || previous.Hint != current.Hint)
			rev, err := revise(previous.Rev, changed)
			if err != nil {
				return Profile{}, warnings, err
			}
			current.Rev = rev
		}
	}
	return result, warnings, nil
}
