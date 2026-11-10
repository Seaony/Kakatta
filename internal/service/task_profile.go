package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
)

func invalidTaskField(field string, reason locales.Message) error {
	return &ServiceError{Kind: ErrorInvalid, Reason: locales.Message{Code: "tasks.invalid", Args: map[string]any{}},
		Fields: []settings.Issue{{Key: field, Reason: reason}}}
}

func taskFieldReason(code string) *locales.Message {
	return &locales.Message{Code: "settings." + code, Args: map[string]any{}}
}

func taskJSONFields(t reflect.Type) map[string]int {
	fields := map[string]int{}
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			fields[name] = i
		}
	}
	return fields
}

func taskJSONObject(raw []byte) (map[string]json.RawMessage, *locales.Message) {
	if len(raw) == 0 || raw[0] != '{' {
		return nil, taskFieldReason("object_required")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.Token()
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		key, _ := decoder.Token()
		name := key.(string)
		if _, exists := fields[name]; exists {
			return nil, &locales.Message{Code: "settings.duplicate_field", Args: map[string]any{"field": name}}
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, taskFieldReason("invalid_json")
		}
		fields[name] = value
	}
	return fields, nil
}

// 解码前逐层检查，避免 null 被当成空文字、false 或数组里的零值。
func taskJSONShape(raw json.RawMessage, t reflect.Type) *locales.Message {
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return taskFieldReason("invalid_json")
	}
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		if t.Kind() == reflect.Pointer {
			return nil
		}
		return taskFieldReason("null_forbidden")
	}
	if t.Kind() == reflect.Pointer {
		return taskJSONShape(raw, t.Elem())
	}
	switch t.Kind() {
	case reflect.String:
		if raw[0] != '"' {
			return taskFieldReason("string_required")
		}
	case reflect.Bool:
		if !bytes.Equal(raw, []byte("true")) && !bytes.Equal(raw, []byte("false")) {
			return taskFieldReason("boolean_required")
		}
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(string(raw), 10, t.Bits())
		if errors.Is(err, strconv.ErrRange) {
			return taskFieldReason("overflow")
		}
		if err != nil {
			return taskFieldReason("integer_required")
		}
		if n < limits.MinSafeInteger || n > limits.MaxSafeInteger {
			return taskFieldReason("overflow")
		}
	case reflect.Slice:
		if raw[0] != '[' {
			return taskFieldReason("list_required")
		}
		var values []json.RawMessage
		json.Unmarshal(raw, &values)
		for _, value := range values {
			if reason := taskJSONShape(value, t.Elem()); reason != nil {
				return reason
			}
		}
	case reflect.Map, reflect.Struct:
		values, reason := taskJSONObject(raw)
		if reason != nil || t.Kind() == reflect.Map {
			return reason
		}
		fields := taskJSONFields(t)
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			i, exists := fields[key]
			if !exists {
				return taskFieldReason("unknown_key")
			}
			if reason := taskJSONShape(values[key], t.Field(i).Type); reason != nil {
				return reason
			}
		}
	}
	return nil
}

func applyTaskPatch(base TaskFields, patch TaskPatch) (TaskFields, error) {
	result := base
	target := reflect.ValueOf(&result).Elem()
	fields := taskJSONFields(target.Type())
	keys := make([]string, 0, len(patch))
	for key := range patch {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		i, exists := fields[key]
		if !exists {
			return TaskFields{}, invalidTaskField(key, *taskFieldReason("unknown_key"))
		}
		field := target.Field(i)
		if reason := taskJSONShape(patch[key], field.Type()); reason != nil {
			return TaskFields{}, invalidTaskField(key, *reason)
		}
		// 每个字段解到新值；后面的字段失败也不会改到原任务的切片和指针。
		value := reflect.New(field.Type())
		if json.Unmarshal(patch[key], value.Interface()) != nil {
			return TaskFields{}, invalidTaskField(key, *taskFieldReason("invalid_json"))
		}
		field.Set(value.Elem())
	}
	return result, nil
}

func normalizeTaskFields(old *Task, proposed TaskFields, patch TaskPatch, global settings.Snapshot) (TaskFields, []settings.Issue, error) {
	return normalizeTaskFieldsMode(old, proposed, patch, global, false)
}

func normalizeTaskFieldsMode(old *Task, proposed TaskFields, patch TaskPatch, global settings.Snapshot, imported bool) (TaskFields, []settings.Issue, error) {
	warnings := []settings.Issue{}
	proposed.Keyword = strings.TrimSpace(proposed.Keyword)
	if n := utf8.RuneCountInString(proposed.Keyword); n < 1 || n > 50 {
		return TaskFields{}, warnings, invalidTaskField("keyword", locales.Message{Code: "tasks.text_length", Args: map[string]any{"min": 1, "max": 50}})
	}
	proposed.Name = strings.TrimSpace(proposed.Name)
	if _, supplied := patch["name"]; old == nil && !supplied {
		proposed.Name = string([]rune(proposed.Keyword)[:min(30, utf8.RuneCountInString(proposed.Keyword))])
	}
	if n := utf8.RuneCountInString(proposed.Name); n < 1 || n > 30 {
		return TaskFields{}, warnings, invalidTaskField("name", locales.Message{Code: "tasks.text_length", Args: map[string]any{"min": 1, "max": 30}})
	}
	for _, price := range []struct {
		key   string
		value *int64
	}{{"min_price", proposed.MinPrice}, {"max_price", proposed.MaxPrice}, {"budget_min", proposed.BudgetMin}, {"budget_max", proposed.BudgetMax}} {
		if price.value != nil && (*price.value < limits.MinSafeInteger || *price.value > limits.MaxSafeInteger) {
			return TaskFields{}, warnings, invalidTaskField(price.key, *taskFieldReason("overflow"))
		}
		if price.value != nil && *price.value < 0 {
			return TaskFields{}, warnings, invalidTaskField(price.key, *taskFieldReason("nonnegative_required"))
		}
	}
	if proposed.MinPrice != nil && proposed.MaxPrice != nil && *proposed.MinPrice > *proposed.MaxPrice {
		return TaskFields{}, warnings, invalidTaskField("max_price", *taskFieldReason("invalid_range"))
	}
	if proposed.BudgetMin != nil && proposed.BudgetMax != nil && *proposed.BudgetMin > *proposed.BudgetMax {
		return TaskFields{}, warnings, invalidTaskField("budget_max", *taskFieldReason("invalid_range"))
	}
	if proposed.AccountID != nil && (*proposed.AccountID < limits.MinSafeInteger || *proposed.AccountID > limits.MaxSafeInteger) {
		return TaskFields{}, warnings, invalidTaskField("account_id", *taskFieldReason("overflow"))
	}
	if proposed.AccountID != nil && *proposed.AccountID < 1 || proposed.AccountPinned && proposed.AccountID == nil {
		return TaskFields{}, warnings, invalidTaskField("account_id", *taskFieldReason("positive_id_required"))
	}
	channels := map[int64]bool{}
	for _, id := range proposed.ChannelIDs {
		if id < limits.MinSafeInteger || id > limits.MaxSafeInteger {
			return TaskFields{}, warnings, invalidTaskField("channel_ids", *taskFieldReason("overflow"))
		}
		if id < 1 {
			return TaskFields{}, warnings, invalidTaskField("channel_ids", *taskFieldReason("positive_id_required"))
		}
		if channels[id] {
			return TaskFields{}, warnings, invalidTaskField("channel_ids", *taskFieldReason("duplicate_id"))
		}
		channels[id] = true
	}
	proposed.ChannelIDs = append([]int64{}, proposed.ChannelIDs...)
	sort.Slice(proposed.ChannelIDs, func(i, j int) bool { return proposed.ChannelIDs[i] < proposed.ChannelIDs[j] })
	if proposed.MaxPages < limits.MinSafeInteger || proposed.MaxPages > limits.MaxSafeInteger {
		return TaskFields{}, warnings, invalidTaskField("max_pages", *taskFieldReason("overflow"))
	}
	if proposed.MaxPages < 1 {
		return TaskFields{}, warnings, invalidTaskField("max_pages", *taskFieldReason("positive_required"))
	}
	if warning := taskNumberWarning("max_pages", "crawl.max_pages", proposed.MaxPages); warning != nil {
		warnings = append(warnings, *warning)
	}
	switch proposed.PublishWindow {
	case "any", "day1", "day3", "day7", "day14":
	default:
		return TaskFields{}, warnings, invalidTaskField("publish_window", *taskFieldReason("invalid_option"))
	}
	if proposed.CrawlOverrides == nil {
		proposed.CrawlOverrides = settings.Values{}
	}
	overrides, err := json.Marshal(proposed.CrawlOverrides)
	if err != nil {
		return TaskFields{}, warnings, invalidTaskField("crawl_overrides", *taskFieldReason("invalid_json"))
	}
	_, advice, err := settings.ResolveTask(global, proposed.IntervalMinutes, overrides)
	if err != nil {
		var validation *settings.ValidationError
		if errors.As(err, &validation) {
			fields := make([]settings.Issue, len(validation.Issues))
			for i, issue := range validation.Issues {
				fields[i] = taskCrawlIssue(issue)
			}
			return TaskFields{}, warnings, &ServiceError{Kind: ErrorInvalid, Reason: locales.Message{Code: "tasks.invalid", Args: map[string]any{}}, Fields: fields}
		}
		return TaskFields{}, warnings, invalidTaskField("crawl_overrides", *taskFieldReason("invalid_json"))
	}
	for _, issue := range advice {
		warnings = append(warnings, taskCrawlIssue(issue))
	}
	if warning := taskNumberWarning("interval_minutes", "crawl.task_interval_minutes", proposed.IntervalMinutes); warning != nil {
		warnings = append(warnings, *warning)
	}
	if proposed.JudgeMode != analysis.ModeAI && proposed.JudgeMode != analysis.ModeKeyword {
		return TaskFields{}, warnings, invalidTaskField("judge_mode", *taskFieldReason("invalid_option"))
	}
	if !taskNotifyPolicyValid(proposed.JudgeMode, proposed.NotifyPolicy) {
		_, supplied := patch["notify_policy"]
		if !supplied && (old == nil || old.JudgeMode != proposed.JudgeMode) {
			proposed.NotifyPolicy = "recommended"
			if proposed.JudgeMode == analysis.ModeAI {
				proposed.NotifyPolicy = "good"
			}
		} else {
			return TaskFields{}, warnings, invalidTaskField("notify_policy", *taskFieldReason("invalid_option"))
		}
	}
	proposed.MatchKeywords = analysis.ParseKeywords(strings.Join(proposed.MatchKeywords, "\n"))
	proposed.ExcludeKeywords = analysis.ParseKeywords(strings.Join(proposed.ExcludeKeywords, "\n"))
	if proposed.JudgeMode == analysis.ModeKeyword {
		proposed.Profile = analysis.Profile{}
		return proposed, warnings, nil
	}
	previous := analysis.Profile{}
	if old != nil {
		previous = old.Profile
	}
	var profile analysis.Profile
	var profileWarnings []locales.Message
	if imported {
		profile, profileWarnings, err = normalizeImportedProfile(proposed.Profile)
	} else {
		profile, err = taskProfileForRevision(previous, proposed.Profile)
		if err == nil {
			profile, profileWarnings, err = analysis.ReviseProfile(previous, profile)
		}
	}
	if err != nil {
		return TaskFields{}, warnings, invalidTaskField("profile", locales.Message{Code: "analysis.profile_invalid", Args: map[string]any{"field": "profile"}})
	}
	if len(profile.Specs) == 0 && len(profile.Musts) == 0 {
		return TaskFields{}, warnings, invalidTaskField("profile", locales.Message{Code: "analysis.profile_invalid", Args: map[string]any{"field": "profile"}})
	}
	for _, reason := range profileWarnings {
		warnings = append(warnings, settings.Issue{Key: "profile", Reason: reason})
	}
	proposed.Profile = profile
	return proposed, warnings, nil
}

func taskCrawlIssue(issue settings.Issue) settings.Issue {
	if issue.Key != "interval_minutes" && issue.Key != "crawl_overrides" {
		issue.Key = "crawl_overrides." + issue.Key
	}
	return issue
}

func taskNumberWarning(field, definitionKey string, value int64) *settings.Issue {
	for _, definition := range settings.Definitions(settings.Environment{}) {
		if definition.Key != definitionKey || definition.Recommendation == nil {
			continue
		}
		range_ := definition.Recommendation
		if (range_.Min == nil || float64(value) >= *range_.Min) && (range_.Max == nil || float64(value) <= *range_.Max) {
			return nil
		}
		args := map[string]any{}
		if range_.Min != nil {
			args["min"] = *range_.Min
		}
		if range_.Max != nil {
			args["max"] = *range_.Max
		}
		return &settings.Issue{Key: field, Reason: locales.Message{Code: "settings.outside_recommendation", Args: args}}
	}
	return nil
}

func taskNotifyPolicyValid(mode analysis.Mode, policy string) bool {
	if policy == "all_new" || policy == "none" {
		return true
	}
	if mode == analysis.ModeKeyword {
		return policy == "recommended"
	}
	return mode == analysis.ModeAI && (policy == "steal" || policy == "good" || policy == "fair")
}

func taskProfileID(id string, prefix byte) (int, bool) {
	if len(id) < 2 || id[0] != prefix {
		return 0, false
	}
	n, err := strconv.Atoi(id[1:])
	return n, err == nil && n > 0 && int64(n) < limits.MaxSafeInteger && strconv.Itoa(n) == id[1:]
}

func taskProfileForRevision(old, proposed analysis.Profile) (analysis.Profile, error) {
	invalid := func() error {
		return invalidTaskField("profile", locales.Message{Code: "analysis.profile_invalid", Args: map[string]any{"field": "profile"}})
	}
	if int64(proposed.NextNo) < limits.MinSafeInteger || int64(proposed.NextNo) > limits.MaxSafeInteger {
		return analysis.Profile{}, invalid()
	}
	watermark := max(1, old.NextNo)
	oldIDs := map[string]bool{}
	reserve := func(id string) {
		oldIDs[id] = true
		for _, prefix := range []byte{'m', 'b', 'c'} {
			if n, ok := taskProfileID(id, prefix); ok && n >= watermark {
				watermark = n + 1
			}
		}
	}
	for _, requirements := range [][]analysis.Requirement{old.Musts, old.Bonuses} {
		for _, item := range requirements {
			reserve(item.ID)
		}
	}
	for _, item := range old.Risks {
		reserve(item.ID)
	}
	proposed.NextNo = max(watermark, proposed.NextNo)
	seen := map[string]bool{}
	prepare := func(id string, prefix byte) error {
		if id == "" {
			return nil
		}
		if seen[id] {
			return invalid()
		}
		seen[id] = true
		if oldIDs[id] {
			return nil
		}
		if n, valid := taskProfileID(id, prefix); valid {
			if n < watermark {
				return invalid()
			}
			proposed.NextNo = max(proposed.NextNo, n+1)
		}
		return nil
	}
	for _, set := range []struct {
		items  []analysis.Requirement
		prefix byte
	}{{proposed.Musts, 'm'}, {proposed.Bonuses, 'b'}} {
		for i := range set.items {
			if err := prepare(set.items[i].ID, set.prefix); err != nil {
				return analysis.Profile{}, err
			}
		}
	}
	for i := range proposed.Risks {
		if err := prepare(proposed.Risks[i].ID, 'c'); err != nil {
			return analysis.Profile{}, err
		}
	}
	return proposed, nil
}

func taskPointerEqual[T comparable](before, after *T) bool {
	return before == nil && after == nil || before != nil && after != nil && *before == *after
}

func taskSearchChanged(before, after Task) bool {
	return before.Keyword != after.Keyword || !taskPointerEqual(before.MinPrice, after.MinPrice) ||
		!taskPointerEqual(before.MaxPrice, after.MaxPrice) || before.PersonalOnly != after.PersonalOnly ||
		before.FreeShippingOnly != after.FreeShippingOnly || before.PublishWindow != after.PublishWindow ||
		!taskPointerEqual(before.RegionProvince, after.RegionProvince) || !taskPointerEqual(before.RegionCity, after.RegionCity) ||
		!taskPointerEqual(before.RegionDistrict, after.RegionDistrict)
}

func addedExcludeWords(before, after []string) []string {
	existing := map[string]bool{}
	for _, word := range analysis.ParseKeywords(strings.Join(before, "\n")) {
		existing[word] = true
	}
	added := []string{}
	for _, word := range analysis.ParseKeywords(strings.Join(after, "\n")) {
		if !existing[word] {
			added = append(added, word)
		}
	}
	return added
}
