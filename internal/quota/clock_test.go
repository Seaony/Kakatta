package quota

import (
	"reflect"
	"testing"
	"time"
)

func TestClockRollbackAndMultipleTimezoneChanges(t *testing.T) {
	now := time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC)
	state, effective, err := AdvanceClock(ClockState{}, now, "UTC")
	if err != nil || !effective.Equal(now) {
		t.Fatal(err)
	}
	for _, timezone := range []string{"Asia/Shanghai", "America/Los_Angeles", "Asia/Shanghai"} {
		state, effective, err = AdvanceClock(state, now.Add(-time.Hour), timezone)
		if err != nil || !effective.Equal(now) {
			t.Fatalf("时间倒退恢复了额度：%v %v", effective, err)
		}
	}
	if len(state.Windows) != 3 {
		t.Fatalf("重复窗口未去重：%+v", state.Windows)
	}
	start, err := state.DayStart()
	if err != nil || !start.Equal(time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("未取全部有效窗口的最早起点：%v %v", start, err)
	}
	previous := state
	state, effective, err = AdvanceClock(state, now.Add(20*time.Hour), "Asia/Shanghai")
	if err != nil || !effective.Equal(now.Add(20*time.Hour)) || len(state.Windows) != 0 {
		t.Fatalf("向前调整或窗口到期错误：%+v %v", state, err)
	}
	if len(previous.Windows) != 3 {
		t.Fatal("修改了传入的旧状态")
	}
}

func TestClockDoesNotReleaseWestwardAtNewMidnight(t *testing.T) {
	now := time.Date(2026, 11, 10, 16, 30, 0, 0, time.UTC)
	state, _, _ := AdvanceClock(ClockState{}, now, "Asia/Shanghai")
	state, _, err := AdvanceClock(state, now, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	start, _ := state.DayStart()
	if !start.Equal(time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)) {
		t.Fatal(start)
	}
	state, _, _ = AdvanceClock(state, now.Add(8*time.Hour), "UTC")
	start, _ = state.DayStart()
	if !start.Equal(time.Date(2026, 11, 10, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("旧日结束前提前释放：%v", start)
	}
}

func TestClockUsesCalendarMidnightAcrossDST(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	state, _, _ := AdvanceClock(ClockState{}, now, "America/New_York")
	state, _, err := AdvanceClock(state, now, "UTC")
	if err != nil || len(state.Windows) != 1 || state.Windows[0].End-state.Windows[0].Start != int64(25*time.Hour/time.Millisecond) {
		t.Fatalf("夏令时回退日不是25小时：%+v %v", state.Windows, err)
	}
}

func TestInvalidClockCannotBeReset(t *testing.T) {
	now := time.Now()
	for _, state := range []ClockState{
		{Version: 2, Timezone: "UTC"},
		{Version: 1, Timezone: "bad/zone"},
		{Version: 1, Timezone: "UTC", Windows: []DayWindow{{Start: 10, End: 9}}},
		{Version: 1, Timezone: "UTC", HighestUTC: 1, Windows: []DayWindow{{Start: 2, End: 3}}},
	} {
		before := state
		if _, _, err := AdvanceClock(state, now, "UTC"); err == nil {
			t.Fatalf("坏记录被当成新记录：%+v", state)
		}
		if !reflect.DeepEqual(before, state) {
			t.Fatal("错误路径改了输入")
		}
	}
}

func TestRealCivilDayBoundaries(t *testing.T) {
	for _, test := range []struct{ zone, now, start, end string }{
		{"America/Santiago", "2026-09-06T12:00:00Z", "2026-09-06T04:00:00Z", "2026-09-07T03:00:00Z"},
		{"America/Santiago", "2026-09-05T12:00:00Z", "2026-09-05T04:00:00Z", "2026-09-06T04:00:00Z"},
		{"America/Havana", "2025-11-02T12:00:00Z", "2025-11-02T04:00:00Z", "2025-11-03T05:00:00Z"},
		{"Pacific/Apia", "2011-12-29T12:00:00Z", "2011-12-29T10:00:00Z", "2011-12-30T10:00:00Z"},
		{"America/New_York", "2040-12-31T17:00:00Z", "2040-12-31T05:00:00Z", "2041-01-01T05:00:00Z"},
	} {
		t.Run(test.zone+test.now, func(t *testing.T) {
			location, err := time.LoadLocation(test.zone)
			if err != nil {
				t.Fatal(err)
			}
			now, _ := time.Parse(time.RFC3339, test.now)
			start, end := DayBounds(now.In(location))
			if start.UTC().Format(time.RFC3339) != test.start || end.UTC().Format(time.RFC3339) != test.end {
				t.Fatalf("日界错误：%v — %v", start.UTC(), end.UTC())
			}
		})
	}
}

func TestSkippedMidnightKeepsOldDayUntilActualEnd(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	state, _, _ := AdvanceClock(ClockState{}, now, "America/Santiago")
	state, _, err := AdvanceClock(state, now, "UTC")
	if err != nil || len(state.Windows) != 1 {
		t.Fatal(err)
	}
	later := time.Date(2026, 9, 7, 2, 30, 0, 0, time.UTC)
	state, _, err = AdvanceClock(state, later, "UTC")
	start, _ := state.DayStart()
	if err != nil || len(state.Windows) != 1 || !start.Equal(time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC)) {
		t.Fatalf("旧日结束前提前恢复了额度：%+v %v %v", state, start, err)
	}
}
