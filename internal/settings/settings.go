// Package settings 定义并校验设置，不负责保存。
package settings

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/locales"
)

type Values map[string]json.RawMessage
type Source string

const (
	SourceDefault     Source = "default"
	SourceStored      Source = "stored"
	SourceEnvironment Source = "environment"
)

type Environment struct {
	TZ             string  `json:"tz"`
	TrustedProxies *string `json:"trusted_proxies"`
}

type Range struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

type QuietHours struct {
	Enabled bool   `json:"enabled"`
	Start   string `json:"start"`
	End     string `json:"end"`
}

type Issue struct {
	Key    string          `json:"key"`
	Reason locales.Message `json:"reason"`
}

type ValidationError struct {
	Issues []Issue `json:"issues"`
}

func (*ValidationError) Error() string { return "settings.invalid" }

type Snapshot struct {
	Values   Values            `json:"values"`
	Sources  map[string]Source `json:"sources"`
	Warnings []Issue           `json:"warnings"`
}

func issue(key, code string, args map[string]any) Issue {
	return Issue{Key: key, Reason: locales.Message{Code: "settings." + code, Args: args}}
}

func Resolve(stored Values, env Environment) (Snapshot, error) {
	defs := Definitions(env)
	s := Snapshot{Values: Values{}, Sources: map[string]Source{}}
	for _, d := range defs {
		s.Values[d.Key] = bytes.Clone(d.Default)
		s.Sources[d.Key] = SourceDefault
	}
	var problems []Issue
	for _, key := range sortedKeys(stored) {
		d, ok := find(defs, key)
		if !ok {
			problems = append(problems, issue(key, "unknown_key", nil))
			continue
		}
		if err := validate(d, stored[key]); err != nil {
			problems = append(problems, *err)
			continue
		}
		s.Values[key] = bytes.Clone(stored[key])
		s.Sources[key] = SourceStored
	}
	if env.TrustedProxies != nil {
		proxies := []string{}
		if value := strings.TrimSpace(*env.TrustedProxies); value != "" {
			for _, proxy := range strings.Split(value, ",") {
				proxies = append(proxies, strings.TrimSpace(proxy))
			}
		}
		s.Values["security.trusted_proxies"], _ = json.Marshal(proxies)
		s.Sources["security.trusted_proxies"] = SourceEnvironment
	}
	if len(problems) != 0 {
		return Snapshot{}, &ValidationError{Issues: problems}
	}
	return checked(s, defs)
}

func Apply(current Snapshot, patch Values) (Snapshot, error) {
	s := Snapshot{Values: cloneValues(current.Values), Sources: map[string]Source{}}
	for key, source := range current.Sources {
		s.Sources[key] = source
	}
	defs := Definitions(Environment{})
	var problems []Issue
	for _, key := range sortedKeys(patch) {
		if _, ok := find(defs, key); !ok {
			problems = append(problems, issue(key, "unknown_key", nil))
		} else if s.Sources[key] == SourceEnvironment {
			problems = append(problems, issue(key, "read_only", nil))
		} else {
			s.Values[key] = bytes.Clone(patch[key])
			s.Sources[key] = SourceStored
		}
	}
	if len(problems) != 0 {
		return Snapshot{}, &ValidationError{Issues: problems}
	}
	return checked(s, defs)
}

func checked(s Snapshot, defs []Definition) (Snapshot, error) {
	var problems []Issue
	s.Warnings = []Issue{}
	for _, d := range defs {
		if err := validate(d, s.Values[d.Key]); err != nil {
			problems = append(problems, *err)
		} else {
			s.Warnings = append(s.Warnings, warnings(d, s.Values[d.Key])...)
		}
	}
	if len(problems) == 0 {
		problems = crossValidate(s.Values)
	}
	if len(problems) != 0 {
		return Snapshot{}, &ValidationError{Issues: problems}
	}
	return s, nil
}

func ResolveTask(global Snapshot, intervalMinutes int64, overrides json.RawMessage) (Values, []Issue, error) {
	raw, err := object("crawl_overrides", overrides)
	if err != nil {
		return nil, nil, &ValidationError{Issues: []Issue{*err}}
	}
	defs := Definitions(Environment{})
	values := Values{}
	var problems []Issue
	var advice []Issue
	for _, key := range sortedKeys(raw) {
		d, exists := find(defs, "crawl."+key)
		if !exists || !d.TaskOverride {
			problems = append(problems, issue(key, "task_override_forbidden", nil))
		}
	}
	for _, d := range defs {
		if !d.TaskOverride {
			continue
		}
		key := strings.TrimPrefix(d.Key, "crawl.")
		value := global.Values[d.Key]
		if override, exists := raw[key]; exists {
			value = override
		}
		if err := validate(d, value); err != nil {
			err.Key = key
			problems = append(problems, *err)
			continue
		}
		values[key] = bytes.Clone(value)
		for _, warning := range warnings(d, value) {
			warning.Key = key
			advice = append(advice, warning)
		}
	}
	if intervalMinutes < limits.MinSafeInteger || intervalMinutes > limits.MaxSafeInteger {
		problems = append(problems, issue("interval_minutes", "overflow", nil))
	} else if intervalMinutes < 1 {
		problems = append(problems, issue("interval_minutes", "positive_required", nil))
	} else if intervalMinutes > maxDuration("minutes") {
		problems = append(problems, issue("interval_minutes", "overflow", nil))
	} else if len(problems) == 0 && !intervalFits(intervalMinutes, integer(values["interval_jitter_percent"])) {
		problems = append(problems, issue("interval_jitter_percent", "overflow", nil))
	}
	if len(problems) != 0 {
		return nil, nil, &ValidationError{Issues: problems}
	}
	if advice == nil {
		advice = []Issue{}
	}
	return values, advice, nil
}

func find(defs []Definition, key string) (Definition, bool) {
	for _, d := range defs {
		if d.Key == key {
			return d, true
		}
	}
	return Definition{}, false
}

func sortedKeys(values Values) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneValues(values Values) Values {
	copy := make(Values, len(values))
	for key, value := range values {
		copy[key] = bytes.Clone(value)
	}
	return copy
}
