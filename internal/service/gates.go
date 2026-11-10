package service

import (
	"errors"
	"sort"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/quota"
)

func requiredEntryVisits(task TaskFields) int64 {
	count := int64(3)
	if task.PersonalOnly {
		count++
	}
	if task.FreeShippingOnly {
		count++
	}
	if task.RegionProvince != nil && *task.RegionProvince != "" {
		count += 2
		for _, name := range []*string{task.RegionProvince, task.RegionCity, task.RegionDistrict} {
			if name != nil && *name != "" {
				count++
			}
		}
	}
	if task.MinPrice != nil || task.MaxPrice != nil {
		count++
	}
	return count
}

// quotaReadyAt 只推算现有记录何时释放，不预扣未来的访问。
func quotaReadyAt(visits []quota.Visit, state quota.ClockState, required int64, kind quota.Kind, limits quota.Limits) (*locales.Message, *int64, error) {
	if err := validateQuotaFacts(visits, state, kind, limits); err != nil {
		return nil, nil, err
	}
	if required < 1 {
		return nil, nil, errors.New("申请的访问次数必须为正数")
	}
	dailyLimit := int64(limits.DailySearch)
	dailyCode := "run.daily_search_quota"
	if kind == quota.Detail {
		dailyLimit = int64(limits.DailyDetail)
		dailyCode = "run.daily_detail_quota"
	}
	if required > int64(limits.Hourly) || required > dailyLimit {
		return &locales.Message{Code: "run.quota_capacity", Args: map[string]any{
			"required": required, "count": required, "hourly_limit": limits.Hourly, "daily_limit": dailyLimit,
		}}, nil, nil
	}
	now := time.UnixMilli(state.HighestUTC).UTC()
	dayStart, _ := state.DayStart()
	expiries := []int64{}
	dailyUsed := int64(0)
	for _, visit := range visits {
		if visit.At.After(now.Add(-time.Hour)) {
			expiries = append(expiries, visit.At.Add(time.Hour).UnixMilli())
		}
		if visit.Kind == kind && !visit.At.Before(dayStart) {
			dailyUsed++
		}
	}
	var reason *locales.Message
	var next int64
	if needed := int64(len(expiries)) + required - int64(limits.Hourly); needed > 0 {
		sort.Slice(expiries, func(i, j int) bool { return expiries[i] < expiries[j] })
		next = expiries[needed-1]
		reason = &locales.Message{Code: "run.hourly_quota", Args: map[string]any{"used": len(expiries), "limit": limits.Hourly}}
	}
	if dailyUsed > dailyLimit-required {
		candidate := state
		for remaining := len(state.Windows) + 3; remaining > 0; remaining-- {
			boundary, err := nextQuotaDayBoundary(candidate)
			if err != nil {
				return nil, nil, err
			}
			candidate, _, err = quota.AdvanceClock(candidate, time.UnixMilli(boundary), candidate.Timezone)
			if err != nil {
				return nil, nil, err
			}
			start, _ := candidate.DayStart()
			used := int64(0)
			for _, visit := range visits {
				if visit.Kind == kind && !visit.At.Before(start) {
					used++
				}
			}
			if used <= dailyLimit-required {
				if boundary > next {
					next = boundary
					reason = &locales.Message{Code: dailyCode, Args: map[string]any{"used": dailyUsed, "limit": dailyLimit}}
				}
				break
			}
			if remaining == 1 {
				return nil, nil, errors.New("无法确定额度日窗口的结束时间")
			}
		}
	}
	if reason == nil {
		return nil, nil, nil
	}
	return reason, &next, nil
}

func validateQuotaFacts(visits []quota.Visit, state quota.ClockState, kind quota.Kind, limits quota.Limits) error {
	if err := state.Validate(); err != nil {
		return err
	}
	if kind != quota.Search && kind != quota.Detail || limits.Hourly < 1 || limits.DailySearch < 1 || limits.DailyDetail < 1 {
		return errors.New("访问类别或额度上限无效")
	}
	now := time.UnixMilli(state.HighestUTC).UTC()
	for _, visit := range visits {
		if visit.At.IsZero() || visit.At.After(now) || visit.Kind != quota.Search && visit.Kind != quota.Detail {
			return errors.New("访问记录与额度时钟不一致")
		}
	}
	return nil
}

// 旧日控制起点时，先等控制它的窗口结束，不能拿新时区的午夜提前放行。
func nextQuotaDayBoundary(state quota.ClockState) (int64, error) {
	start, err := state.DayStart()
	if err != nil {
		return 0, err
	}
	location, _ := time.LoadLocation(state.Timezone)
	now := time.UnixMilli(state.HighestUTC).In(location)
	currentStart, currentEnd := quota.DayBounds(now)
	boundary := currentEnd.UnixMilli()
	if start.Before(currentStart) {
		boundary = 0
		for _, window := range state.Windows {
			if window.Start == start.UnixMilli() && window.End > state.HighestUTC && (boundary == 0 || window.End < boundary) {
				boundary = window.End
			}
		}
	}
	if boundary <= state.HighestUTC {
		return 0, errors.New("额度日窗口没有向前推进")
	}
	return boundary, nil
}
