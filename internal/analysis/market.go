package analysis

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
)

type marketRow struct {
	sample     MarketSample
	item       ItemSpecs
	key        string
	brandNew   bool
	priceKnown bool
}

func CalculateMarket(in MarketInput) MarketResult {
	out := MarketResult{Groups: []MarketGroup{}, TaskTrend: []DailyPrice{}, Warnings: []locales.Message{}}
	location, err := time.LoadLocation(in.TimeZone)
	if err != nil || in.TimeZone == "" || in.TimeZone == "Local" {
		out.Warnings = append(out.Warnings, message("analysis.market_timezone", map[string]any{"timezone": in.TimeZone}))
		return out
	}
	now := time.UnixMilli(in.NowMS).In(location)
	today := now.Format(time.DateOnly)
	start14 := now.AddDate(0, 0, -13).Format(time.DateOnly)
	start30 := now.AddDate(0, 0, -29).Format(time.DateOnly)
	rows := []marketRow{}
	for _, observation := range in.Observations {
		day, parseErr := time.ParseInLocation(time.DateOnly, observation.Day, location)
		if parseErr != nil || day.Format(time.DateOnly) != observation.Day || observation.ItemID == "" {
			out.Warnings = append(out.Warnings, message("analysis.market_observation", map[string]any{"id": observation.ItemID, "day": observation.Day}))
			continue
		}
		if observation.Day < start30 || observation.Day > today {
			continue
		}
		if out.SnapshotAt == nil || observation.LastSeenAt > *out.SnapshotAt {
			out.SnapshotAt = ptr(observation.LastSeenAt)
		}
		if observation.Dismissed || observation.Excluded || len(MatchingKeywords(observation.Title, in.ExcludeKeywords)) > 0 {
			continue
		}
		if in.Mode == ModeKeyword {
			kind := KeywordKind(observation.Title)
			if kind == KindWanted || kind == KindRecycle || kind == KindRental || observation.Kind == KindWanted || observation.Kind == KindRecycle || observation.Kind == KindRental {
				continue
			}
		} else {
			allowed := observation.Kind == KindSale
			for _, kind := range in.Profile.AllowedKinds {
				allowed = allowed || observation.Kind == kind
			}
			if !allowed {
				continue
			}
		}
		item := ResolveSpecs(in.Profile, observation.Specs, observation.BrandNew, SpecDetail)
		brandNew := observation.BrandNew != nil && *observation.BrandNew
		if in.Mode == ModeKeyword {
			item = ItemSpecs{Specs: []SpecValue{}, ConfigKey: ptr("*")}
			brandNew = false
		}
		if item.ConfigKey == nil {
			continue
		}
		priceKnown := observation.Price != nil && *observation.Price >= 0
		price := int64(0)
		if priceKnown {
			price = *observation.Price
		}
		rows = append(rows, marketRow{sample: MarketSample{ItemID: observation.ItemID, SellerID: observation.SellerID, Price: price, Day: observation.Day, LastSeenAt: observation.LastSeenAt}, item: item, key: *item.ConfigKey, brandNew: brandNew, priceKnown: priceKnown})
	}
	type identity struct {
		key      string
		brandNew bool
	}
	identities := []identity{}
	templates := map[identity]ItemSpecs{}
	for _, row := range rows {
		id := identity{row.key, row.brandNew}
		if _, ok := templates[id]; !ok {
			identities = append(identities, id)
			templates[id] = row.item
		}
	}
	sort.Slice(identities, func(i, j int) bool {
		if identities[i].key != identities[j].key {
			return identities[i].key < identities[j].key
		}
		return !identities[i].brandNew && identities[j].brandNew
	})
	latest := map[string]bool{}
	for _, id := range in.LatestRunItemIDs {
		latest[id] = true
	}
	bestCount, bestIndex := -1, -1
	for _, id := range identities {
		item := templates[id]
		exact := []marketRow{}
		for _, row := range rows {
			if row.key == id.key && row.brandNew == id.brandNew {
				exact = append(exact, row)
			}
		}
		if len(marketLatestItems(exact)) == 0 {
			continue
		}
		original14 := marketSamples(exact, start14, "")
		samples := original14
		days := 14
		var dropped *string
		if in.Mode != ModeKeyword && len(samples) < 5 {
			days = 30
			samples = marketSamples(exact, start30, "")
			if len(samples) < 5 && len(in.Profile.Specs) > 1 {
				pool := []marketRow{}
				for _, row := range rows {
					if row.brandNew == id.brandNew && sameLeadingSpecs(item.Specs, row.item.Specs, len(in.Profile.Specs)-1) {
						pool = append(pool, row)
					}
				}
				samples = marketSamples(pool, start30, "")
				dropped = ptr(in.Profile.Specs[len(in.Profile.Specs)-1].Name)
			}
		}
		fair := FairPrice{Sample: len(samples), Days: days, DroppedSpec: dropped, ItemIDs: []string{}}
		for _, sample := range samples {
			fair.ItemIDs = append(fair.ItemIDs, sample.ItemID)
		}
		fair.Basis = message("analysis.fair_same_config", map[string]any{"days": days, "count": days})
		if dropped != nil {
			fair.Basis = message("analysis.fair_relaxed", map[string]any{"days": days, "count": days, "name": *dropped})
		}
		if len(samples) >= 5 || in.Mode == ModeKeyword && len(samples) > 0 {
			// 整数分只在落合理价时舍入，避免抹掉每日走势和分位数的半分。
			fair.Cents = ptr(marketMedianCents(samples))
		} else {
			fair.Basis = message("analysis.fair_unavailable", map[string]any{"count": len(samples)})
		}
		group := MarketGroup{ConfigKey: id.key, Specs: marketCopySpecs(item.Specs), BrandNew: id.brandNew, Selected: valuationMismatch(in.Profile, item.Specs) == nil, Fair: fair, Samples: samples, Daily: []DailyPrice{}}
		if len(samples) > 0 {
			group.PriceBand = &PriceBand{P25: marketQuantile(samples, .25), P75: marketQuantile(samples, .75)}
		}
		for offset := 13; offset >= 0; offset-- {
			day := now.AddDate(0, 0, -offset).Format(time.DateOnly)
			daySamples := marketSamples(exact, day, day)
			point := DailyPrice{Day: day, Sample: len(daySamples)}
			if len(daySamples) > 0 {
				point.Median = ptr(marketQuantile(daySamples, .5))
			}
			group.Daily = append(group.Daily, point)
		}
		var first, last *DailyPrice
		for i := range group.Daily {
			if group.Daily[i].Median != nil {
				if first == nil {
					first = &group.Daily[i]
				}
				last = &group.Daily[i]
			}
		}
		if first != nil && last != nil && first.Day != last.Day && first.Sample >= 3 && last.Sample >= 3 {
			group.Trend = &PriceTrend{FirstDay: first.Day, LastDay: last.Day, FirstMedian: *first.Median, LastMedian: *last.Median, Change: *last.Median - *first.Median}
			if *first.Median != 0 {
				group.Trend.Percent = ptr(group.Trend.Change / *first.Median)
			}
		}
		for _, sample := range marketLatestItems(exact) {
			if latest[sample.ItemID] && (group.Cheapest == nil || sample.Price < group.Cheapest.Price || sample.Price == group.Cheapest.Price && sample.ItemID < group.Cheapest.ItemID) {
				group.Cheapest = ptr(sample)
			}
		}
		out.Groups = append(out.Groups, group)
		if group.Selected && len(original14) > bestCount {
			bestCount, bestIndex = len(original14), len(out.Groups)-1
		}
	}
	if bestIndex >= 0 {
		for _, point := range out.Groups[bestIndex].Daily[7:] {
			out.TaskTrend = append(out.TaskTrend, DailyPrice{Day: point.Day, Median: clonePtr(point.Median), Sample: point.Sample})
		}
	} else {
		for offset := 6; offset >= 0; offset-- {
			out.TaskTrend = append(out.TaskTrend, DailyPrice{Day: now.AddDate(0, 0, -offset).Format(time.DateOnly)})
		}
	}
	return out
}

func marketSamples(rows []marketRow, start, end string) []MarketSample {
	pool := make([]marketRow, 0, len(rows))
	for _, row := range rows {
		if row.sample.Day >= start && (end == "" || row.sample.Day <= end) {
			pool = append(pool, row)
		}
	}
	items := marketLatestItems(pool)
	bySeller := map[string]MarketSample{}
	for _, sample := range items {
		key := "seller:" + sample.SellerID
		if sample.SellerID == "" {
			key = "item:" + sample.ItemID
		}
		old, ok := bySeller[key]
		if !ok || sample.Price < old.Price || sample.Price == old.Price && sample.ItemID < old.ItemID {
			bySeller[key] = sample
		}
	}
	out := make([]MarketSample, 0, len(bySeller))
	for _, sample := range bySeller {
		out = append(out, sample)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Price == out[j].Price {
			return out[i].ItemID < out[j].ItemID
		}
		return out[i].Price < out[j].Price
	})
	if len(out) >= 4 {
		q1, q3 := marketQuantile(out, .25), marketQuantile(out, .75)
		low, high := q1-1.5*(q3-q1), q3+1.5*(q3-q1)
		kept := make([]MarketSample, 0, len(out))
		for _, sample := range out {
			if float64(sample.Price) >= low && float64(sample.Price) <= high {
				kept = append(kept, sample)
			}
		}
		out = kept
	}
	return out
}
func marketLatestItems(rows []marketRow) []MarketSample {
	byItem := map[string]marketRow{}
	for _, row := range rows {
		sample := row.sample
		old, ok := byItem[sample.ItemID]
		previous := old.sample
		if !ok || sample.Day > previous.Day || sample.Day == previous.Day && (sample.LastSeenAt > previous.LastSeenAt || sample.LastSeenAt == previous.LastSeenAt && (!row.priceKnown && old.priceKnown || row.priceKnown == old.priceKnown && (sample.Price < previous.Price || sample.Price == previous.Price && sample.SellerID < previous.SellerID))) {
			byItem[sample.ItemID] = row
		}
	}
	out := make([]MarketSample, 0, len(byItem))
	// 最新一天价格未知时不能退回旧价。
	for _, row := range byItem {
		if row.priceKnown {
			out = append(out, row.sample)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ItemID < out[j].ItemID })
	return out
}
func marketMedianCents(sorted []MarketSample) int64 {
	middle := len(sorted) / 2
	if len(sorted)%2 != 0 {
		return sorted[middle].Price
	}
	a, b := sorted[middle-1].Price, sorted[middle].Price
	difference := b - a
	return a + difference/2 + difference%2
}
func marketQuantile(sorted []MarketSample, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := float64(len(sorted)-1) * q
	lower := int(math.Floor(index))
	upper := int(math.Ceil(index))
	return float64(sorted[lower].Price) + (float64(sorted[upper].Price)-float64(sorted[lower].Price))*(index-float64(lower))
}
func sameLeadingSpecs(a, b []SpecValue, n int) bool {
	if len(a) < n || len(b) < n {
		return false
	}
	for i := 0; i < n; i++ {
		if a[i].Name != b[i].Name || a[i].Value == nil || b[i].Value == nil || configText(*a[i].Value) != configText(*b[i].Value) {
			return false
		}
	}
	return true
}
func marketCopySpecs(values []SpecValue) []SpecValue {
	out := make([]SpecValue, 0, len(values))
	for _, v := range values {
		out = append(out, SpecValue{Name: v.Name, Value: clonePtr(v.Value)})
	}
	return out
}

func AdjacentPrices(profile Profile, item ItemSpecs, price int64, groups []MarketGroup) []AdjacentPrice {
	out := []AdjacentPrice{}
	item = ResolveSpecs(profile, item.Specs, item.BrandNew, item.Source)
	if item.ConfigKey == nil {
		return out
	}
	brandNew := item.BrandNew != nil && *item.BrandNew
	var upper *MarketGroup
	for i := range groups {
		if groups[i].ConfigKey == *item.ConfigKey && groups[i].BrandNew == brandNew && groups[i].Fair.Cents != nil {
			upper = &groups[i]
			break
		}
	}
	if upper == nil {
		return out
	}
	for i, spec := range profile.Specs {
		if i >= len(item.Specs) || item.Specs[i].Value == nil {
			continue
		}
		index := -1
		for j, option := range spec.Options {
			if configText(option) == configText(*item.Specs[i].Value) {
				index = j
				break
			}
		}
		if index <= 0 {
			continue
		}
		values := marketCopySpecs(item.Specs)
		values[i].Value = ptr(configText(spec.Options[index-1]))
		lowerItem := ResolveSpecs(profile, values, item.BrandNew, item.Source)
		if lowerItem.ConfigKey == nil {
			continue
		}
		for _, lower := range groups {
			if lower.ConfigKey == *lowerItem.ConfigKey && lower.BrandNew == brandNew && lower.Fair.Cents != nil {
				out = append(out, AdjacentPrice{SpecName: spec.Name, From: strings.TrimSpace(spec.Options[index-1]), To: *item.Specs[i].Value, LowerConfigKey: lower.ConfigKey, ItemPremium: price - *lower.Fair.Cents, TypicalPremium: *upper.Fair.Cents - *lower.Fair.Cents})
				break
			}
		}
	}
	return out
}
