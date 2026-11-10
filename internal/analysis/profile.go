package analysis

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/locales"
)

func ParseGeneratedProfile(body []byte) (GeneratedProfile, []locales.Message, error) {
	out := GeneratedProfile{}
	warnings := []locales.Message{}
	text := string(body)
	for {
		start := strings.Index(text, "<think>")
		if start < 0 {
			break
		}
		end := strings.Index(text[start+7:], "</think>")
		if end < 0 {
			return out, warnings, ruleError("analysis.thinking_truncated", nil)
		}
		text = text[:start] + text[start+7+end+8:]
	}
	if strings.TrimSpace(text) == "" {
		return out, warnings, ruleError("analysis.thinking_truncated", nil)
	}
	obj, err := jsonObject([]byte(text))
	if err != nil {
		return out, warnings, err
	}
	for _, key := range []string{"summary", "specs", "musts", "bonuses", "risks", "allowed_kinds", "budget_min", "budget_max", "keyword", "exclude_words"} {
		if _, exists := obj[key]; !exists {
			return out, warnings, invalidField(key)
		}
	}
	discardedBudgets := []string{}
	for _, key := range []string{"budget_min", "budget_max"} {
		if bytes.Equal(bytes.TrimSpace(obj[key]), []byte("null")) {
			continue
		}
		var n float64
		if json.Unmarshal(obj[key], &n) != nil || math.IsInf(n, 0) || math.IsNaN(n) {
			obj[key] = json.RawMessage("null")
			discardedBudgets = append(discardedBudgets, key)
		}
	}
	normalized, _ := json.Marshal(obj)
	if json.Unmarshal(normalized, &out) != nil {
		return GeneratedProfile{}, warnings, invalidField("profile")
	}
	out.DiscardedBudgets = discardedBudgets
	if _, ok := rawString(obj["summary"]); !ok {
		return GeneratedProfile{}, warnings, invalidField("summary")
	}
	for _, key := range []string{"specs", "musts", "bonuses", "risks", "allowed_kinds", "exclude_words"} {
		if _, ok := rawArray(obj[key]); !ok {
			return GeneratedProfile{}, warnings, invalidField(key)
		}
	}
	if _, ok := nullableString(obj["keyword"]); !ok {
		return GeneratedProfile{}, warnings, invalidField("keyword")
	}
	specRows, _ := rawArray(obj["specs"])
	for _, row := range specRows {
		var fields rawObject
		if json.Unmarshal(row, &fields) != nil || fields == nil {
			return GeneratedProfile{}, warnings, invalidField("specs")
		}
		for _, key := range []string{"options", "values"} {
			values, ok := rawArray(fields[key])
			if !ok {
				return GeneratedProfile{}, warnings, invalidField("specs." + key)
			}
			for _, value := range values {
				if _, ok := rawString(value); !ok {
					return GeneratedProfile{}, warnings, invalidField("specs." + key)
				}
			}
		}
	}
	words, _ := rawArray(obj["exclude_words"])
	for _, word := range words {
		if _, ok := rawString(word); !ok {
			return GeneratedProfile{}, warnings, invalidField("exclude_words")
		}
	}
	for _, spec := range out.Specs {
		if spec.Name == "" || spec.Options == nil || spec.Values == nil || !generatedSource(spec.Source) {
			return GeneratedProfile{}, warnings, invalidField("specs")
		}
	}
	for _, set := range [][]GeneratedRequirement{out.Musts, out.Bonuses} {
		for _, r := range set {
			if r.Text == "" || !generatedSource(r.Source) {
				return GeneratedProfile{}, warnings, invalidField("requirements")
			}
		}
	}
	riskRows, _ := rawArray(obj["risks"])
	for _, row := range riskRows {
		var fields rawObject
		if json.Unmarshal(row, &fields) != nil || fields == nil {
			return GeneratedProfile{}, warnings, invalidField("risks")
		}
		for _, key := range []string{"id", "name", "hint", "question", "attitude", "source"} {
			if _, exists := fields[key]; !exists {
				return GeneratedProfile{}, warnings, invalidField("risks." + key)
			}
		}
	}
	for _, r := range out.Risks {
		if !generatedSource(r.Source) || r.Attitude != nil && !validAttitude(*r.Attitude) {
			return GeneratedProfile{}, warnings, invalidField("risks")
		}
	}
	for _, k := range out.AllowedKinds {
		if !validKind(k) {
			return GeneratedProfile{}, warnings, invalidField("allowed_kinds")
		}
	}
	return out, warnings, nil
}
func generatedSource(s ProfileSource) bool { return s == SourceUser || s == SourceAI }
func cloneSpec(s ProfileSpec) ProfileSpec {
	s.Options = slices.Clone(s.Options)
	s.Values = slices.Clone(s.Values)
	return s
}
func requirementKey(s string) string { return evidenceText(s) }
func riskKey(r ProfileRisk) string {
	if _, ok := dictionaryRisk(r.ID); ok {
		return "id:" + r.ID
	}
	return "name:" + evidenceText(r.Name)
}

// 旧编号也计入水位，删除条目不能让编号重新发放。
func profileWatermark(p Profile) (int, error) {
	if int64(p.NextNo) < limits.MinSafeInteger || int64(p.NextNo) > limits.MaxSafeInteger {
		return 0, ruleError("analysis.profile_invalid", map[string]any{"field": "next_no"})
	}
	watermark := max(1, p.NextNo)
	reserve := func(id string) error {
		if len(id) < 2 || id[0] != 'm' && id[0] != 'b' && id[0] != 'c' {
			return nil
		}
		n, err := strconv.Atoi(id[1:])
		if errors.Is(err, strconv.ErrRange) || int64(n) < limits.MinSafeInteger || int64(n) > limits.MaxSafeInteger {
			return ruleError("analysis.profile_invalid", map[string]any{"field": "id"})
		}
		if err == nil && n >= watermark {
			if int64(n) == limits.MaxSafeInteger {
				return ruleError("analysis.profile_id_exhausted", nil)
			}
			watermark = n + 1
		}
		return nil
	}
	for _, items := range [][]Requirement{p.Musts, p.Bonuses} {
		for _, item := range items {
			if err := reserve(item.ID); err != nil {
				return 0, err
			}
		}
	}
	for _, item := range p.Risks {
		if err := reserve(item.ID); err != nil {
			return 0, err
		}
	}
	return watermark, nil
}

func MergeGeneratedProfile(old Profile, generated GeneratedProfile, keyword, requirement string) (Profile, ProfileSuggestions, []locales.Message, error) {
	result := Profile{Summary: generated.Summary, Specs: []ProfileSpec{}, Musts: []Requirement{}, Bonuses: []Requirement{}, Risks: []ProfileRisk{}, AllowedKinds: append([]Kind{}, generated.AllowedKinds...), NextNo: old.NextNo}
	suggestions := ProfileSuggestions{BudgetMin: clonePtr(generated.BudgetMin), BudgetMax: clonePtr(generated.BudgetMax), Keyword: clonePtr(generated.Keyword), ExcludeWords: append([]string{}, generated.ExcludeWords...), DiscardedBudgets: append([]string{}, generated.DiscardedBudgets...)}
	warnings := []locales.Message{}
	watermark, err := profileWatermark(old)
	if err != nil {
		return Profile{}, suggestions, warnings, err
	}
	result.NextNo = watermark
	oldSpecs := map[string]ProfileSpec{}
	oldMusts := map[string]Requirement{}
	oldBonuses := map[string]Requirement{}
	oldRisks := map[string]ProfileRisk{}
	editedRequirements := map[string]string{}
	for _, s := range old.Specs {
		if _, exists := oldSpecs[s.Name]; !exists {
			oldSpecs[s.Name] = s
		}
	}
	for _, r := range old.Musts {
		key := requirementKey(r.Text)
		if _, exists := oldMusts[key]; !exists {
			oldMusts[key] = r
		}
		if r.Edited {
			editedRequirements[key] = "must"
		}
	}
	for _, r := range old.Bonuses {
		key := requirementKey(r.Text)
		if _, exists := oldBonuses[key]; !exists {
			oldBonuses[key] = r
		}
		if r.Edited {
			if _, exists := editedRequirements[key]; !exists {
				editedRequirements[key] = "bonus"
			}
		}
	}
	for _, r := range old.Risks {
		key := riskKey(r)
		if _, exists := oldRisks[key]; !exists {
			oldRisks[key] = r
		}
	}
	allocate := func(prefix string) (string, error) {
		if int64(result.NextNo) >= limits.MaxSafeInteger {
			return "", ruleError("analysis.profile_id_exhausted", nil)
		}
		id := fmt.Sprintf("%s%d", prefix, result.NextNo)
		result.NextNo++
		return id, nil
	}
	specSeen := map[string]bool{}
	for _, s := range generated.Specs {
		if strings.TrimSpace(s.Name) == "" || specSeen[s.Name] {
			warnings = append(warnings, ignored("specs"))
			continue
		}
		if !generatedSource(s.Source) {
			return Profile{}, suggestions, warnings, invalidField("specs.source")
		}
		specSeen[s.Name] = true
		if current, exists := oldSpecs[s.Name]; exists && current.Edited {
			result.Specs = append(result.Specs, cloneSpec(current))
		} else {
			result.Specs = append(result.Specs, ProfileSpec{Name: s.Name, Options: append([]string{}, s.Options...), Values: append([]string{}, s.Values...), Source: s.Source})
		}
	}
	for _, s := range old.Specs {
		if s.Edited && !specSeen[s.Name] {
			result.Specs = append(result.Specs, cloneSpec(s))
			specSeen[s.Name] = true
		}
	}
	mustSeen, bonusSeen := map[string]bool{}, map[string]bool{}
	addRequirement := func(r GeneratedRequirement, target string, forceNew bool) error {
		key := requirementKey(r.Text)
		if key == "" {
			warnings = append(warnings, ignored("requirements"))
			return nil
		}
		if !generatedSource(r.Source) {
			return invalidField("requirements.source")
		}
		if editedTarget, exists := editedRequirements[key]; exists && editedTarget != target {
			return nil
		}
		seen := mustSeen
		prior := oldMusts
		prefix := "m"
		if target == "bonus" {
			seen = bonusSeen
			prior = oldBonuses
			prefix = "b"
		}
		if seen[key] {
			return nil
		}
		seen[key] = true
		current, matched := prior[key]
		var item Requirement
		if matched && current.Edited {
			item = current
		} else {
			item = Requirement{Text: r.Text, Source: r.Source}
			if matched && !forceNew {
				item.ID = current.ID
			} else {
				id, err := allocate(prefix)
				if err != nil {
					return err
				}
				item.ID = id
			}
		}
		if target == "bonus" {
			result.Bonuses = append(result.Bonuses, item)
		} else {
			result.Musts = append(result.Musts, item)
		}
		return nil
	}
	for _, r := range generated.Musts {
		if !generatedSource(r.Source) {
			return Profile{}, suggestions, warnings, invalidField("musts.source")
		}
		key := requirementKey(r.Text)
		if current, exists := oldMusts[key]; exists && current.Edited {
			if err := addRequirement(r, "must", false); err != nil {
				return Profile{}, suggestions, warnings, err
			}
			continue
		}
		if editedRequirements[key] == "bonus" {
			continue
		}
		if r.Source == SourceUser && (evidencePresent(requirement, r.Text) || evidencePresent(keyword, r.Text)) {
			if err := addRequirement(r, "must", false); err != nil {
				return Profile{}, suggestions, warnings, err
			}
		} else {
			warnings = append(warnings, message("analysis.must_downgraded", map[string]any{"text": r.Text}))
			r.Source = SourceAI
			_, wasMust := oldMusts[key]
			if err := addRequirement(r, "bonus", wasMust); err != nil {
				return Profile{}, suggestions, warnings, err
			}
		}
	}
	for _, r := range generated.Bonuses {
		if err := addRequirement(r, "bonus", false); err != nil {
			return Profile{}, suggestions, warnings, err
		}
	}
	for _, r := range old.Musts {
		key := requirementKey(r.Text)
		if r.Edited && !mustSeen[key] {
			result.Musts = append(result.Musts, r)
			mustSeen[key] = true
		}
	}
	for _, r := range old.Bonuses {
		key := requirementKey(r.Text)
		if r.Edited && !bonusSeen[key] {
			result.Bonuses = append(result.Bonuses, r)
			bonusSeen[key] = true
		}
	}
	riskSeen := map[string]bool{}
	addRisk := func(r ProfileRisk) error {
		key := riskKey(r)
		if riskSeen[key] {
			return nil
		}
		riskSeen[key] = true
		if current, exists := oldRisks[key]; exists {
			if current.Edited {
				r = current
			} else if strings.HasPrefix(key, "name:") {
				r.ID = current.ID
			}
		}
		if r.ID == "" {
			id, err := allocate("c")
			if err != nil {
				return err
			}
			r.ID = id
		}
		result.Risks = append(result.Risks, r)
		return nil
	}
	for _, r := range generated.Risks {
		if !generatedSource(r.Source) || r.Attitude != nil && !validAttitude(*r.Attitude) {
			return Profile{}, suggestions, warnings, invalidField("risks")
		}
		var d RiskDefinition
		dictionary := false
		if r.ID != nil {
			d, dictionary = dictionaryRisk(*r.ID)
		}
		name := ""
		if r.Name != nil {
			name = strings.TrimSpace(*r.Name)
		}
		if !dictionary && evidenceText(name) == "" {
			warnings = append(warnings, ignored("risks"))
			continue
		}
		if !dictionary {
			for _, candidate := range riskDefinitions {
				if evidenceText(candidate.Name) == evidenceText(name) {
					d, dictionary = candidate, true
					break
				}
			}
		}
		item := ProfileRisk{Source: r.Source}
		if dictionary {
			item.ID = d.ID
			item.Attitude = d.DefaultAttitude
			if r.Attitude != nil {
				item.Attitude = *r.Attitude
			}
		} else {
			item.Name = name
			item.Attitude = Mind
			if r.Hint != nil {
				item.Hint = *r.Hint
			}
			if r.Question != nil {
				item.Question = *r.Question
			}
			if r.Attitude != nil && *r.Attitude == Ignore {
				item.Attitude = Ignore
			}
			if r.Attitude != nil && (*r.Attitude == Confirm || *r.Attitude == Reject) {
				warnings = append(warnings, message("analysis.risk_softened", map[string]any{"name": name}))
			}
		}
		if err := addRisk(item); err != nil {
			return Profile{}, suggestions, warnings, err
		}
	}
	for _, d := range riskDefinitions {
		if generalRisk(d.ID) {
			if err := addRisk(ProfileRisk{ID: d.ID, Attitude: d.DefaultAttitude, Source: SourceSystem}); err != nil {
				return Profile{}, suggestions, warnings, err
			}
		}
	}
	for _, r := range old.Risks {
		key := riskKey(r)
		if r.Edited && !riskSeen[key] {
			result.Risks = append(result.Risks, r)
			riskSeen[key] = true
		}
	}
	revised, revisionWarnings, err := ReviseProfile(old, result)
	warnings = append(warnings, revisionWarnings...)
	if err != nil {
		return Profile{}, suggestions, warnings, err
	}
	return revised, suggestions, warnings, nil
}

// NormalizeProfile 整理普通保存的清单，不补回用户删掉的通用项。
func NormalizeProfile(p Profile) (Profile, []locales.Message, error) {
	warnings := []locales.Message{}
	if err := validateProfileRevisions(p); err != nil {
		return Profile{}, warnings, err
	}
	invalid := func(field string) error { return ruleError("analysis.profile_invalid", map[string]any{"field": field}) }
	if int64(p.NextNo) < limits.MinSafeInteger || int64(p.NextNo) > limits.MaxSafeInteger {
		return Profile{}, warnings, invalid("next_no")
	}
	out := Profile{Summary: p.Summary, Specs: []ProfileSpec{}, Musts: []Requirement{}, Bonuses: []Requirement{}, Risks: []ProfileRisk{}, AllowedKinds: append([]Kind{}, p.AllowedKinds...), NextNo: p.NextNo}
	if out.NextNo < 1 {
		out.NextNo = 1
	}
	ids := map[string]bool{}
	reserve := func(id, prefix string) error {
		if id == "" {
			return nil
		}
		if ids[id] {
			return invalid("id")
		}
		ids[id] = true
		if !strings.HasPrefix(id, prefix) || len(id) <= 1 {
			return invalid("id")
		}
		n, err := strconv.Atoi(id[1:])
		if err != nil || n <= 0 || strconv.Itoa(n) != id[1:] {
			return invalid("id")
		}
		if int64(n) > limits.MaxSafeInteger {
			return invalid("id")
		}
		if int64(n) == limits.MaxSafeInteger {
			return ruleError("analysis.profile_id_exhausted", nil)
		}
		if n >= out.NextNo {
			out.NextNo = n + 1
		}
		return nil
	}
	for _, r := range p.Musts {
		if !generatedSource(r.Source) || strings.TrimSpace(r.Text) == "" {
			return Profile{}, warnings, invalid("musts")
		}
		if err := reserve(r.ID, "m"); err != nil {
			return Profile{}, warnings, err
		}
	}
	for _, r := range p.Bonuses {
		if !generatedSource(r.Source) || strings.TrimSpace(r.Text) == "" {
			return Profile{}, warnings, invalid("bonuses")
		}
		if err := reserve(r.ID, "b"); err != nil {
			return Profile{}, warnings, err
		}
	}
	for _, r := range p.Risks {
		if !validAttitude(r.Attitude) || r.Source != SourceUser && r.Source != SourceAI && r.Source != SourceSystem {
			return Profile{}, warnings, invalid("risks")
		}
		if _, dictionary := dictionaryRisk(r.ID); dictionary {
			if ids[r.ID] {
				return Profile{}, warnings, invalid("id")
			}
			ids[r.ID] = true
		} else {
			if evidenceText(r.Name) == "" {
				return Profile{}, warnings, invalid("risks.name")
			}
			if err := reserve(r.ID, "c"); err != nil {
				return Profile{}, warnings, err
			}
		}
	}
	specNames := map[string]bool{}
	for _, s := range p.Specs {
		if strings.TrimSpace(s.Name) == "" || specNames[s.Name] || !generatedSource(s.Source) {
			return Profile{}, warnings, invalid("specs")
		}
		specNames[s.Name] = true
		options := map[string]bool{}
		for _, option := range s.Options {
			v := configText(option)
			if v == "" {
				return Profile{}, warnings, invalid("specs.options")
			}
			options[v] = true
		}
		for _, value := range s.Values {
			if !options[configText(value)] {
				return Profile{}, warnings, invalid("specs.values")
			}
		}
		copied := cloneSpec(s)
		if copied.Options == nil {
			copied.Options = []string{}
		}
		if copied.Values == nil {
			copied.Values = []string{}
		}
		out.Specs = append(out.Specs, copied)
	}
	for _, k := range p.AllowedKinds {
		if !validKind(k) {
			return Profile{}, warnings, invalid("allowed_kinds")
		}
	}
	allocate := func(prefix string) (string, error) {
		if int64(out.NextNo) >= limits.MaxSafeInteger {
			return "", ruleError("analysis.profile_id_exhausted", nil)
		}
		id := fmt.Sprintf("%s%d", prefix, out.NextNo)
		out.NextNo++
		return id, nil
	}
	for _, r := range p.Musts {
		r.Rev = profileRevision(r.Rev)
		if r.Source == SourceAI {
			id, err := allocate("b")
			if err != nil {
				return Profile{}, warnings, err
			}
			r.ID = id
			r.Rev = 1
			out.Bonuses = append(out.Bonuses, r)
			warnings = append(warnings, message("analysis.must_downgraded", map[string]any{"text": r.Text}))
			continue
		}
		if r.ID == "" {
			id, err := allocate("m")
			if err != nil {
				return Profile{}, warnings, err
			}
			r.ID = id
			r.Rev = 1
		}
		out.Musts = append(out.Musts, r)
	}
	for _, r := range p.Bonuses {
		r.Rev = profileRevision(r.Rev)
		if r.ID == "" {
			id, err := allocate("b")
			if err != nil {
				return Profile{}, warnings, err
			}
			r.ID = id
			r.Rev = 1
		}
		out.Bonuses = append(out.Bonuses, r)
	}
	for _, r := range p.Risks {
		r.Rev = profileRevision(r.Rev)
		if _, dictionary := dictionaryRisk(r.ID); dictionary {
			r.Name = ""
			r.Hint = ""
			r.Question = ""
		} else if r.ID == "" {
			id, err := allocate("c")
			if err != nil {
				return Profile{}, warnings, err
			}
			r.ID = id
			r.Rev = 1
		}
		if _, dictionary := dictionaryRisk(r.ID); !dictionary && r.Source == SourceAI && !r.Edited && (r.Attitude == Confirm || r.Attitude == Reject) {
			r.Attitude = Mind
			warnings = append(warnings, message("analysis.risk_softened", map[string]any{"name": r.Name}))
		}
		out.Risks = append(out.Risks, r)
	}
	return out, warnings, nil
}
