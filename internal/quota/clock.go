package quota

import (
	"errors"
	"time"
)

// ClockState 在设置和额度的同一事务里保存，重启不能抹掉保护窗口。
type ClockState struct {
	Version    int         `json:"version"`
	HighestUTC int64       `json:"highest_utc"`
	Timezone   string      `json:"timezone"`
	Windows    []DayWindow `json:"windows"`
}

type DayWindow struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// AdvanceClock 只防时间倒退；向前调整时间仍按新时间计算。
func AdvanceClock(previous ClockState, observed time.Time, timezone string) (ClockState, time.Time, error) {
	if _, err := time.LoadLocation(timezone); timezone == "" || timezone == "Local" || err != nil || observed.IsZero() {
		return ClockState{}, time.Time{}, errors.New("额度时钟参数无效")
	}
	now := observed.UTC()
	if previous.Version == 0 && previous.Timezone == "" && previous.HighestUTC == 0 && len(previous.Windows) == 0 {
		if now.UnixMilli() <= 0 {
			return ClockState{}, time.Time{}, errors.New("首次启动的额度时钟无效")
		}
		return ClockState{Version: 1, HighestUTC: now.UnixMilli(), Timezone: timezone, Windows: []DayWindow{}}, now, nil
	}
	if err := previous.Validate(); err != nil {
		return ClockState{}, time.Time{}, err
	}
	if previous.HighestUTC > now.UnixMilli() {
		now = time.UnixMilli(previous.HighestUTC).UTC()
	}
	next := ClockState{Version: 1, HighestUTC: now.UnixMilli(), Timezone: timezone, Windows: []DayWindow{}}
	for _, window := range previous.Windows {
		if now.UnixMilli() < window.End {
			next.Windows = append(next.Windows, window)
		}
	}
	if previous.Timezone != timezone {
		oldLocation, _ := time.LoadLocation(previous.Timezone)
		start, end := DayBounds(now.In(oldLocation))
		window := DayWindow{Start: start.UnixMilli(), End: end.UnixMilli()}
		found := false
		for _, existing := range next.Windows {
			if existing == window {
				found = true
				break
			}
		}
		if !found {
			next.Windows = append(next.Windows, window)
		}
	}
	if err := next.Validate(); err != nil {
		return ClockState{}, time.Time{}, err
	}
	return next, now, nil
}

func (state ClockState) Validate() error {
	if state.Version != 1 || state.Timezone == "" || state.Timezone == "Local" || state.HighestUTC <= 0 || state.Windows == nil {
		return errors.New("额度时钟记录损坏")
	}
	if _, err := time.LoadLocation(state.Timezone); err != nil {
		return errors.New("额度时钟时区无效")
	}
	for _, window := range state.Windows {
		if window.End <= window.Start || window.Start > state.HighestUTC {
			return errors.New("额度时钟窗口无效")
		}
	}
	return nil
}

func (state ClockState) DayStart() (time.Time, error) {
	if err := state.Validate(); err != nil {
		return time.Time{}, err
	}
	location, _ := time.LoadLocation(state.Timezone)
	start, _ := DayBounds(time.UnixMilli(state.HighestUTC).In(location))
	start = start.UTC()
	for _, window := range state.Windows {
		if state.HighestUTC < window.End && window.Start < start.UnixMilli() {
			start = time.UnixMilli(window.Start).UTC()
		}
	}
	return start, nil
}

// DayBounds 按真实时区区间找日界，午夜不存在或重复时也不靠归一化猜测。
func DayBounds(now time.Time) (time.Time, time.Time) {
	year, month, day := now.Date()
	return civilDayStart(year, month, day, now.Location()), civilDayStart(year, month, day+1, now.Location())
}

func civilDayStart(year int, month time.Month, day int, location *time.Location) time.Time {
	wall := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	year, month, day = wall.Date()
	approximate := time.Date(year, month, day, 0, 0, 0, 0, location)
	cursor, limit := approximate.Add(-48*time.Hour), approximate.Add(48*time.Hour)
	var first time.Time
	for cursor.Before(limit) {
		local := cursor.In(location)
		_, offset := local.Zone()
		_, end := local.ZoneBounds()
		// Go 的未来时区规则在闰年末可能给出已过的年界，跨到下一年再读。
		if !end.IsZero() && !end.After(cursor) {
			end = time.Date(cursor.UTC().Year()+1, time.January, 1, 0, 0, 0, 0, time.UTC)
		}
		if end.IsZero() || end.After(limit) {
			end = limit
		}
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		if candidate.Before(cursor) {
			candidate = cursor
		}
		if candidate.Before(end) {
			y, m, d := candidate.In(location).Date()
			date := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
			if !date.Before(wall) && (first.IsZero() || candidate.Before(first)) {
				first = candidate
			}
		}
		cursor = end
	}
	return first.In(location)
}
