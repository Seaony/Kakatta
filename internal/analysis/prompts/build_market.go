package prompts

import (
	"encoding/json"
	"math"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/locales"
)

func BuildMarket(s Snapshot, taskID *int64, stats analysis.MarketResult) (client.Request, error) {
	system, _, err := requestParts(s, client.Market)
	if err != nil {
		return client.Request{}, err
	}
	groups := make([]map[string]any, 0, len(stats.Groups))
	for _, source := range stats.Groups {
		if !source.Selected {
			continue
		}
		if source.Fair.Sample < 0 || source.Fair.Cents != nil && *source.Fair.Cents < 0 {
			return client.Request{}, inputError("market.fair_price")
		}
		basis, err := locales.Format(locales.ZhCN, source.Fair.Basis)
		if err != nil {
			return client.Request{}, inputError("market.fair_price.basis")
		}
		fair := map[string]any{"price": nil, "sample": source.Fair.Sample, "basis": basis}
		if source.Fair.Cents != nil {
			fair["price"] = centsNumber(*source.Fair.Cents)
		}
		specs := make([]map[string]any, 0, len(source.Specs))
		for _, spec := range source.Specs {
			specs = append(specs, map[string]any{"name": spec.Name, "value": spec.Value})
		}
		daily := make([]map[string]any, 0, len(source.Daily))
		for _, point := range source.Daily {
			if point.Sample < 0 || point.Median != nil && *point.Median < 0 {
				return client.Request{}, inputError("market.daily")
			}
			row := map[string]any{"day": point.Day, "median": nil, "sample": point.Sample}
			if point.Median != nil {
				median, err := marketFractionalCents(*point.Median, "market.daily.median")
				if err != nil {
					return client.Request{}, err
				}
				row["median"] = median
			}
			daily = append(daily, row)
		}
		group := map[string]any{
			"specs": specs, "brand_new": source.BrandNew, "fair_price": fair,
			"price_band": nil, "cheapest_price": nil, "trend": nil, "daily": daily,
		}
		if source.PriceBand != nil {
			if source.PriceBand.P25 < 0 || source.PriceBand.P75 < 0 {
				return client.Request{}, inputError("market.price_band")
			}
			p25, err := marketFractionalCents(source.PriceBand.P25, "market.price_band.p25")
			if err != nil {
				return client.Request{}, err
			}
			p75, err := marketFractionalCents(source.PriceBand.P75, "market.price_band.p75")
			if err != nil {
				return client.Request{}, err
			}
			group["price_band"] = map[string]any{"p25": p25, "p75": p75}
		}
		if source.Cheapest != nil {
			if source.Cheapest.Price < 0 {
				return client.Request{}, inputError("market.cheapest_price")
			}
			group["cheapest_price"] = centsNumber(source.Cheapest.Price)
		}
		if source.Trend != nil {
			trend := source.Trend
			if trend.FirstMedian < 0 || trend.LastMedian < 0 || trend.Percent != nil && (math.IsNaN(*trend.Percent) || math.IsInf(*trend.Percent, 0)) {
				return client.Request{}, inputError("market.trend")
			}
			first, err := marketFractionalCents(trend.FirstMedian, "market.trend.first_median")
			if err != nil {
				return client.Request{}, err
			}
			last, err := marketFractionalCents(trend.LastMedian, "market.trend.last_median")
			if err != nil {
				return client.Request{}, err
			}
			change, err := marketFractionalCents(trend.Change, "market.trend.change")
			if err != nil {
				return client.Request{}, err
			}
			group["trend"] = map[string]any{
				"first_day": trend.FirstDay, "last_day": trend.LastDay,
				"first_median": first, "last_median": last, "change": change, "percent": trend.Percent,
			}
		}
		groups = append(groups, group)
	}
	user, err := userJSON(map[string]any{"groups": groups, "snapshot_at": stats.SnapshotAt})
	if err != nil {
		return client.Request{}, err
	}
	return client.Request{Kind: client.Market, TaskID: copyTaskID(taskID), System: system, User: user}, nil
}

func marketFractionalCents(cents float64, field string) (json.Number, error) {
	number, err := fractionalCentsNumber(cents)
	if err != nil {
		return "", inputError(field)
	}
	return number, nil
}
