package experimentstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/Seaony/Kakatta/internal/quota"
)

const Version = 1

var ErrInvalid = errors.New("实验访问记录格式不兼容或损坏")
var ErrVersion = errors.New("实验访问记录版本不受支持")

type DayWindow struct {
	Timezone string    `json:"timezone"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
}

type State struct {
	Version         int           `json:"version"`
	Visits          []quota.Visit `json:"visits"`
	HighestAt       time.Time     `json:"highest_at"`
	Timezone        string        `json:"timezone"`
	Windows         []DayWindow   `json:"windows"`
	InProgress      bool          `json:"in_progress"`
	EndedAt         time.Time     `json:"ended_at"`
	GapSeconds      int           `json:"gap_seconds"`
	VerifyPending   bool          `json:"verify_pending"`
	VerifyURL       string        `json:"verify_url"`
	VerifyLevel     int           `json:"verify_level"`
	LastCooldownEnd time.Time     `json:"last_cooldown_end"`
	CooldownUntil   time.Time     `json:"cooldown_until"`
	ManualUsed      bool          `json:"manual_used"`
	DeniedUntil     time.Time     `json:"denied_until"`
}

// Decode 保持第一版的完整校验；后续格式变更在这里迁移，不能清掉旧保护。
func Decode(raw []byte) (State, error) {
	var state State
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return state, ErrInvalid
	}
	var version int
	if json.Unmarshal(fields["version"], &version) != nil {
		return state, ErrInvalid
	}
	if version != Version {
		return state, ErrVersion
	}
	for _, key := range []string{"version", "visits", "highest_at", "timezone", "windows", "in_progress", "ended_at", "gap_seconds", "verify_pending", "verify_url", "verify_level", "last_cooldown_end", "cooldown_until", "manual_used", "denied_until"} {
		value, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) && key != "visits" && key != "windows" {
			return state, ErrInvalid
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&state) != nil || state.Timezone == "" || state.HighestAt.IsZero() || state.VerifyLevel < 0 || state.GapSeconds < 0 || int64(state.GapSeconds) > math.MaxInt64/int64(time.Second) {
		return State{}, ErrInvalid
	}
	if _, err := time.LoadLocation(state.Timezone); err != nil {
		return State{}, ErrInvalid
	}
	if state.EndedAt.IsZero() != (state.GapSeconds == 0) || state.EndedAt.After(state.HighestAt) || state.LastCooldownEnd.After(state.HighestAt) {
		return State{}, ErrInvalid
	}
	if (state.VerifyPending || !state.CooldownUntil.IsZero() || !state.LastCooldownEnd.IsZero()) && state.VerifyLevel < 1 {
		return State{}, ErrInvalid
	}
	for _, visit := range state.Visits {
		if visit.At.IsZero() || visit.At.After(state.HighestAt) || visit.Kind != quota.Search && visit.Kind != quota.Detail {
			return State{}, ErrInvalid
		}
	}
	for _, window := range state.Windows {
		_, err := time.LoadLocation(window.Timezone)
		if err != nil || window.Timezone == "" || window.Start.IsZero() || window.End.IsZero() || !window.Start.Before(window.End) || window.Start.After(state.HighestAt) {
			return State{}, ErrInvalid
		}
	}
	return state, nil
}
