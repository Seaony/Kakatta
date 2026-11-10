package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/big"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Seaony/Kakatta/internal/limits"
)

func validate(d Definition, raw json.RawMessage) *Issue {
	bad := func(code string) *Issue { i := issue(d.Key, code, nil); return &i }
	if !json.Valid(raw) {
		return bad("invalid_json")
	}
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		if d.Kind == KindNullableID {
			return nil
		}
		return bad("null_forbidden")
	}
	switch d.Kind {
	case KindInteger, KindNullableID:
		n, err := strconv.ParseInt(string(raw), 10, 64)
		if errors.Is(err, strconv.ErrRange) {
			return bad("overflow")
		}
		if err != nil {
			return bad("integer_required")
		}
		if n < limits.MinSafeInteger || n > limits.MaxSafeInteger {
			return bad("overflow")
		}
		zero := d.Key == "crawl.interval_jitter_percent" || d.Key == "crawl.account_min_gap_jitter_seconds" || d.Key == "ai.daily_token_limit" || d.Key == "retention.findings_days"
		if n < 0 || n == 0 && !zero {
			if zero {
				return bad("nonnegative_required")
			}
			return bad("positive_required")
		}
		if d.Key == "crawl.account_min_gap_seconds" && n <= 1 {
			i := issue(d.Key, "minimum", map[string]any{"min": 2})
			return &i
		}
		if n > maxDuration(d.Unit) {
			return bad("overflow")
		}
	case KindBoolean:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return bad("boolean_required")
		}
	case KindString:
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return bad("string_required")
		}
		if d.Key == "general.timezone" {
			if value == "" || value == "Local" {
				return bad("invalid_timezone")
			}
			if _, err := time.LoadLocation(value); err != nil {
				return bad("invalid_timezone")
			}
		}
		if d.Key == "general.public_url" && value != "" && !publicURL(value) {
			return bad("invalid_url")
		}
	case KindRange:
		fields, err := fields(d.Key, raw, "min", "max")
		if err != nil {
			return err
		}
		var r Range
		if json.Unmarshal(fields["min"], &r.Min) != nil || json.Unmarshal(fields["max"], &r.Max) != nil {
			return bad("range_required")
		}
		if r.Min <= 0 || r.Max < r.Min || math.IsNaN(r.Min) || math.IsNaN(r.Max) || math.IsInf(r.Max, 0) {
			return bad("invalid_range")
		}
		nanos := float64(multiplier(d.Unit))
		if r.Min*nanos < 1 || r.Max*nanos >= float64(math.MaxInt64) {
			return bad("overflow")
		}
	case KindQuietHours:
		fields, err := fields(d.Key, raw, "enabled", "start", "end")
		if err != nil {
			return err
		}
		var quiet QuietHours
		if bytes.Equal(bytes.TrimSpace(fields["enabled"]), []byte("null")) || json.Unmarshal(fields["enabled"], &quiet.Enabled) != nil {
			return bad("boolean_required")
		}
		if json.Unmarshal(fields["start"], &quiet.Start) != nil || json.Unmarshal(fields["end"], &quiet.End) != nil {
			return bad("invalid_clock")
		}
		for _, value := range []string{quiet.Start, quiet.End} {
			parsed, err := time.Parse("15:04", value)
			if err != nil || parsed.Format("15:04") != value {
				return bad("invalid_clock")
			}
		}
	case KindIDs:
		var ids []json.RawMessage
		if err := json.Unmarshal(raw, &ids); err != nil {
			return bad("list_required")
		}
		seen := map[int64]bool{}
		for _, id := range ids {
			var n int64
			if json.Unmarshal(id, &n) != nil {
				return bad("positive_id_required")
			}
			if n < limits.MinSafeInteger || n > limits.MaxSafeInteger {
				return bad("overflow")
			}
			if n <= 0 {
				return bad("positive_id_required")
			}
			if seen[n] {
				return bad("duplicate_id")
			}
			seen[n] = true
		}
	case KindStrings:
		var addresses []json.RawMessage
		if err := json.Unmarshal(raw, &addresses); err != nil {
			return bad("list_required")
		}
		seen := map[string]bool{}
		for _, raw := range addresses {
			var address string
			if json.Unmarshal(raw, &address) != nil {
				return bad("string_required")
			}
			identity := address
			if prefix, err := netip.ParsePrefix(address); err == nil {
				identity = prefix.Masked().String()
			} else if addr, err := netip.ParseAddr(address); err == nil && addr.Zone() == "" {
				identity = netip.PrefixFrom(addr, addr.BitLen()).String()
			} else {
				return bad("invalid_proxy_address")
			}
			if seen[identity] {
				return bad("duplicate_address")
			}
			seen[identity] = true
		}
	}
	if len(d.Options) > 0 {
		var valid bool
		for _, option := range d.Options {
			if d.Kind == KindInteger && integer(raw) == integer(option) {
				valid = true
			} else if d.Kind == KindString {
				var value, choice string
				json.Unmarshal(raw, &value)
				json.Unmarshal(option, &choice)
				valid = valid || value == choice
			}
		}
		if !valid {
			return bad("invalid_option")
		}
	}
	return nil
}

func object(key string, raw json.RawMessage) (Values, *Issue) {
	bad := func(code string, args map[string]any) (Values, *Issue) { i := issue(key, code, args); return nil, &i }
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return bad("object_required", nil)
	}
	values := Values{}
	for dec.More() {
		token, err = dec.Token()
		if err != nil {
			return bad("invalid_json", nil)
		}
		name, ok := token.(string)
		if !ok {
			return bad("invalid_json", nil)
		}
		if _, duplicate := values[name]; duplicate {
			return bad("duplicate_field", map[string]any{"field": name})
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return bad("invalid_json", nil)
		}
		values[name] = value
	}
	if token, err = dec.Token(); err != nil || token != json.Delim('}') {
		return bad("invalid_json", nil)
	}
	if _, err := dec.Token(); err != io.EOF {
		return bad("invalid_json", nil)
	}
	return values, nil
}

func fields(key string, raw json.RawMessage, names ...string) (Values, *Issue) {
	values, err := object(key, raw)
	if err != nil {
		return nil, err
	}
	for _, name := range sortedKeys(values) {
		if !slices.Contains(names, name) {
			i := issue(key, "unknown_field", map[string]any{"field": name})
			return nil, &i
		}
	}
	for _, name := range names {
		if _, exists := values[name]; !exists {
			i := issue(key, "missing_field", map[string]any{"field": name})
			return nil, &i
		}
	}
	return values, nil
}

func integer(raw json.RawMessage) int64 {
	var n int64
	json.Unmarshal(raw, &n)
	return n
}

func multiplier(unit string) int64 {
	switch unit {
	case "milliseconds":
		return int64(time.Millisecond)
	case "seconds":
		return int64(time.Second)
	case "minutes":
		return int64(time.Minute)
	case "hours":
		return int64(time.Hour)
	case "days":
		return int64(24 * time.Hour)
	case "mb":
		return 1 << 20
	}
	return 1
}

func maxDuration(unit string) int64 { return math.MaxInt64 / multiplier(unit) }

func intervalFits(minutes, percent int64) bool {
	if minutes < 1 || minutes > maxDuration("minutes") || percent < 0 {
		return false
	}
	base := minutes * int64(time.Minute)
	// 先用整数计算上界，避免大百分比的浮点舍入把溢出藏起来。
	var n, remainder big.Int
	n.Mul(big.NewInt(base), big.NewInt(percent))
	n.QuoRem(&n, big.NewInt(100), &remainder)
	if remainder.Sign() != 0 {
		n.Add(&n, big.NewInt(1))
	}
	return n.Cmp(big.NewInt(math.MaxInt64-base)) <= 0
}

func crossValidate(values Values) []Issue {
	var problems []Issue
	base := integer(values["crawl.verify_cooldown_base_minutes"])
	max := integer(values["crawl.verify_cooldown_max_minutes"])
	if base > max {
		problems = append(problems, issue("crawl.verify_cooldown_base_minutes", "cooldown_order", nil))
	}
	gap := integer(values["crawl.account_min_gap_seconds"])
	jitter := integer(values["crawl.account_min_gap_jitter_seconds"])
	if gap > maxDuration("seconds")-jitter {
		problems = append(problems, issue("crawl.account_min_gap_jitter_seconds", "overflow", nil))
	}
	if !intervalFits(integer(values["crawl.task_interval_minutes"]), integer(values["crawl.interval_jitter_percent"])) {
		problems = append(problems, issue("crawl.interval_jitter_percent", "overflow", nil))
	}
	return problems
}

func warnings(d Definition, raw json.RawMessage) []Issue {
	var out []Issue
	r := d.Recommendation
	var min, max float64
	if d.Kind == KindInteger {
		min = float64(integer(raw))
		max = min
	} else if d.Kind == KindRange {
		var value Range
		json.Unmarshal(raw, &value)
		min, max = value.Min, value.Max
	}
	if r != nil && (r.Min != nil && min < *r.Min || r.Max != nil && max > *r.Max) {
		args := map[string]any{}
		if r.Min != nil {
			args["min"] = *r.Min
		}
		if r.Max != nil {
			args["max"] = *r.Max
		}
		out = append(out, issue(d.Key, "outside_recommendation", args))
	}
	if d.Key == "retention.stats_days" && integer(raw) < 31 {
		out = append(out, issue(d.Key, "stats_incomplete", nil))
	}
	if d.Kind == KindQuietHours {
		var quiet QuietHours
		json.Unmarshal(raw, &quiet)
		if !quiet.Enabled || quiet.Start == quiet.End {
			out = append(out, issue(d.Key, "quiet_disabled", nil))
		}
	}
	return out
}

func publicURL(value string) bool {
	u, err := url.Parse(value)
	if err != nil || !(strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")) || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(value, "#") {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		return err == nil && n > 0
	}
	return !strings.HasSuffix(u.Host, ":")
}
