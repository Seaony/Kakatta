package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
)

func repairImportedTaskFieldsTx(ctx context.Context, tx *sql.Tx, fields TaskFields, options *taskImportOptions, corrections *[]settings.Issue) (TaskFields, error) {
	name := strings.TrimSpace(fields.Name)
	if name == "" {
		name = strings.TrimSpace(fields.Keyword)
		importCorrection(corrections, "name", "name_defaulted")
	}
	if utf8.RuneCountInString(name) > 30 {
		name = string([]rune(name)[:30])
		importCorrection(corrections, "name", "name_truncated")
	}
	fields.Name = name
	if fields.MaxPages < 1 {
		fields.MaxPages = 1
		importCorrection(corrections, "max_pages", "pages_defaulted")
	}
	if fields.BudgetMin != nil && fields.BudgetMax != nil && *fields.BudgetMin > *fields.BudgetMax {
		fields.BudgetMin, fields.BudgetMax = nil, nil
		importCorrection(corrections, "budget_min", "budget_removed")
		importCorrection(corrections, "budget_max", "budget_removed")
	}
	if err := repairImportedOverrides(&fields, corrections); err != nil {
		return TaskFields{}, err
	}
	repairImportedProfile(&fields.Profile, corrections)
	if fields.JudgeMode == analysis.ModeAI && len(fields.Profile.Specs) == 0 && len(fields.Profile.Musts) == 0 {
		fields.JudgeMode = analysis.ModeKeyword
		importCorrection(corrections, "judge_mode", "ai_to_keyword")
	}
	if (fields.JudgeMode == analysis.ModeAI || fields.JudgeMode == analysis.ModeKeyword) && !taskNotifyPolicyValid(fields.JudgeMode, fields.NotifyPolicy) {
		fields.NotifyPolicy = "recommended"
		if fields.JudgeMode == analysis.ModeAI {
			fields.NotifyPolicy = "good"
		}
		importCorrection(corrections, "notify_policy", "policy_defaulted")
	}
	if options.Channels != nil {
		fields.ChannelIDs = []int64{}
		seen := map[int64]bool{}
		for index, ref := range *options.Channels {
			var id int64
			err := tx.QueryRowContext(ctx, `SELECT id FROM channels WHERE type=? AND name=? ORDER BY id LIMIT 1`, ref.Type, ref.Name).Scan(&id)
			key := "channels[" + strconv.Itoa(index) + "]"
			if errors.Is(err, sql.ErrNoRows) {
				importCorrection(corrections, key, "channel_unmatched")
				continue
			}
			if err != nil {
				return TaskFields{}, storageFailure(err)
			}
			if seen[id] {
				importCorrection(corrections, key, "duplicate_channel")
				continue
			}
			seen[id] = true
			fields.ChannelIDs = append(fields.ChannelIDs, id)
		}
	}
	return fields, nil
}

func repairImportedProfile(profile *analysis.Profile, corrections *[]settings.Issue) {
	dictionary := map[string]analysis.RiskDefinition{}
	for _, risk := range analysis.RiskDictionary() {
		dictionary[risk.ID] = risk
	}
	source := func(value *analysis.ProfileSource, risk bool, path string) bool {
		if *value == analysis.SourceUser || *value == analysis.SourceAI || risk && *value == analysis.SourceSystem {
			return false
		}
		*value = analysis.SourceAI
		importCorrection(corrections, path+".source", "profile_source")
		return true
	}
	specs := []analysis.ProfileSpec{}
	for index, spec := range profile.Specs {
		path := "profile.specs[" + strconv.Itoa(index) + "]"
		if strings.TrimSpace(spec.Name) == "" {
			importCorrection(corrections, path, "profile_empty_spec_removed")
			continue
		}
		source(&spec.Source, false, path)
		specs = append(specs, spec)
	}
	profile.Specs = specs
	seen := map[string]bool{}
	fixedID := func(id *string, prefix byte, path string) {
		_, valid := taskProfileID(*id, prefix)
		if !valid || seen[*id] {
			*id = ""
			importCorrection(corrections, path+".id", "profile_id")
		} else {
			seen[*id] = true
		}
	}
	revision := func(rev *int64, path string) {
		if *rev == 0 {
			*rev = 1
			importCorrection(corrections, path+".rev", "profile_revision")
		}
	}
	musts := []analysis.Requirement{}
	moved := []analysis.Requirement{}
	for index, item := range profile.Musts {
		path := "profile.musts[" + strconv.Itoa(index) + "]"
		source(&item.Source, false, path)
		fixedID(&item.ID, 'm', path)
		revision(&item.Rev, path)
		if item.Source == analysis.SourceAI {
			item.ID = ""
			moved = append(moved, item)
			importCorrection(corrections, path, "profile_must_moved")
		} else {
			musts = append(musts, item)
		}
	}
	bonuses := []analysis.Requirement{}
	for index, item := range profile.Bonuses {
		path := "profile.bonuses[" + strconv.Itoa(index) + "]"
		source(&item.Source, false, path)
		fixedID(&item.ID, 'b', path)
		revision(&item.Rev, path)
		bonuses = append(bonuses, item)
	}
	profile.Musts = musts
	profile.Bonuses = append(moved, bonuses...)
	risks := []analysis.ProfileRisk{}
	for index, item := range profile.Risks {
		path := "profile.risks[" + strconv.Itoa(index) + "]"
		definition, known := dictionary[item.ID]
		if !known && item.ID != "" && !strings.HasPrefix(item.ID, "c") {
			importCorrection(corrections, path, "profile_unknown_risk_removed")
			continue
		}
		if known {
			if seen[item.ID] {
				importCorrection(corrections, path, "profile_duplicate_risk_removed")
				continue
			}
			seen[item.ID] = true
		} else {
			if strings.TrimSpace(item.Name) == "" {
				importCorrection(corrections, path, "profile_empty_risk_removed")
				continue
			}
			fixedID(&item.ID, 'c', path)
		}
		fallback := source(&item.Source, true, path)
		revision(&item.Rev, path)
		if item.Attitude != analysis.Confirm && item.Attitude != analysis.Reject && item.Attitude != analysis.Mind && item.Attitude != analysis.Ignore {
			item.Attitude = analysis.Mind
			if known {
				item.Attitude = definition.DefaultAttitude
			}
			importCorrection(corrections, path+".attitude", "profile_attitude")
		}
		if !known && (fallback || item.Source == analysis.SourceAI && !item.Edited) && (item.Attitude == analysis.Confirm || item.Attitude == analysis.Reject) {
			item.Attitude = analysis.Mind
			importCorrection(corrections, path+".attitude", "profile_attitude")
		}
		risks = append(risks, item)
	}
	profile.Risks = risks
	kinds := []analysis.Kind{}
	for index, kind := range profile.AllowedKinds {
		switch kind {
		case analysis.KindSale, analysis.KindWanted, analysis.KindAccessory, analysis.KindRecycle, analysis.KindRental, analysis.KindRepair, analysis.KindBox, analysis.KindMulti, analysis.KindBroken, analysis.KindOther:
			kinds = append(kinds, kind)
		default:
			importCorrection(corrections, "profile.allowed_kinds["+strconv.Itoa(index)+"]", "profile_kind_removed")
		}
	}
	profile.AllowedKinds = kinds
	next := max(1, profile.NextNo)
	for id := range seen {
		for _, prefix := range []byte{'m', 'b', 'c'} {
			if n, ok := taskProfileID(id, prefix); ok {
				next = max(next, n+1)
			}
		}
	}
	if next != profile.NextNo {
		profile.NextNo = next
		importCorrection(corrections, "profile.next_no", "profile_next_no")
	}
}

// 修复时已把必须满足移到目标组；Normalize 只在各组原位置分号，不替换内容。
func normalizeImportedProfile(profile analysis.Profile) (analysis.Profile, []locales.Message, error) {
	result, warnings, err := analysis.NormalizeProfile(profile)
	if err != nil {
		return result, warnings, err
	}
	for index, item := range profile.Musts {
		if item.Rev > 0 {
			result.Musts[index].Rev = item.Rev
		}
	}
	for index, item := range profile.Bonuses {
		if item.Rev > 0 {
			result.Bonuses[index].Rev = item.Rev
		}
	}
	for index, item := range profile.Risks {
		if item.Rev > 0 {
			result.Risks[index].Rev = item.Rev
		}
	}
	return result, warnings, nil
}

func repairImportedOverrides(fields *TaskFields, corrections *[]settings.Issue) error {
	defs := map[string]settings.Definition{}
	for _, definition := range settings.Definitions(settings.Environment{}) {
		if definition.TaskOverride {
			defs[strings.TrimPrefix(definition.Key, "crawl.")] = definition
		}
	}
	keys := make([]string, 0, len(fields.CrawlOverrides))
	for key := range fields.CrawlOverrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := fields.CrawlOverrides[key]
		definition, ok := defs[key]
		path := "crawl_overrides." + key
		if !ok {
			delete(fields.CrawlOverrides, key)
			importCorrection(corrections, path, "unknown_field")
			continue
		}
		invalid := false
		switch definition.Kind {
		case settings.KindInteger:
			if reason := taskJSONShape(value, reflect.TypeFor[int64]()); reason != nil {
				return invalidTaskField(path, *reason)
			}
			var n int64
			json.Unmarshal(value, &n)
			invalid = n < 0 || n == 0 && key != "interval_jitter_percent"
		case settings.KindRange:
			clean, err := cleanImportedJSON(value, reflect.TypeFor[settings.Range](), path, corrections)
			if err != nil {
				return err
			}
			value = clean
			fields.CrawlOverrides[key] = clean
			parts, reason := taskJSONObject(bytes.TrimSpace(clean))
			if reason != nil {
				return invalidTaskField(path, *reason)
			}
			for _, part := range parts {
				if bytes.Equal(bytes.TrimSpace(part), []byte("null")) || len(part) == 0 || bytes.TrimSpace(part)[0] == '"' {
					return invalidTaskField(path, *taskFieldReason("range_required"))
				}
			}
			var r settings.Range
			if json.Unmarshal(clean, &r) != nil {
				return invalidTaskField(path, *taskFieldReason("range_required"))
			}
			invalid = r.Min <= 0 || r.Max < r.Min
		}
		if invalid {
			delete(fields.CrawlOverrides, key)
			importCorrection(corrections, path, "override_removed")
		}
	}
	return nil
}
