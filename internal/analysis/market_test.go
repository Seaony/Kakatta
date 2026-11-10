package analysis

import (
	"fmt"
	"math"
	"testing"
	"time"
)

func marketInput() MarketInput {
	return MarketInput{Mode: ModeAI, NowMS: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC).UnixMilli(), TimeZone: "Asia/Shanghai"}
}
func valuationObservation(id, day string, price int64) MarketObservation {
	return MarketObservation{ItemID: id, Day: day, Price: ptr(price), Kind: KindSale, LastSeenAt: 1}
}
func onlyMarket(t *testing.T, in MarketInput) MarketGroup {
	t.Helper()
	r := CalculateMarket(in)
	if len(r.Groups) != 1 {
		t.Fatalf("groups=%+v warnings=%+v", r.Groups, r.Warnings)
	}
	return r.Groups[0]
}

func TestMarketNoSpecsPriceRoundingAndKeywordSmallSamples(t *testing.T) {
	in := marketInput()
	for i, p := range []int64{100, 100, 100, 101, 101, 101} {
		in.Observations = append(in.Observations, valuationObservation(fmt.Sprint(i), "2026-11-10", p))
	}
	g := onlyMarket(t, in)
	if g.ConfigKey != "*" || g.Fair.Cents == nil || *g.Fair.Cents != 101 || g.Fair.Days != 14 || g.Fair.DroppedSpec != nil || g.Daily[13].Median == nil || *g.Daily[13].Median != 100.5 {
		t.Fatalf("unconfigured rounding=%+v", g)
	}
	in.Mode = ModeKeyword
	in.Observations = in.Observations[:2]
	in.Observations[0].BrandNew = ptr(true)
	in.Observations[1].BrandNew = ptr(false)
	g = onlyMarket(t, in)
	if g.Fair.Cents == nil || g.Fair.Sample != 2 || g.Fair.Days != 14 {
		t.Fatalf("keyword small sample=%+v", g)
	}
	in.Observations = append(in.Observations, valuationObservation("junk", "2026-11-10", 1))
	in.Observations[2].Title = "收一台ipad"
	if g := onlyMarket(t, in); g.Fair.Sample != 2 {
		t.Fatal("keyword wanted entered market")
	}
}

func TestMarketSampleDeduplicationAndExclusions(t *testing.T) {
	in := marketInput()
	prices := []int64{100, 110, 120, 130, 140}
	for i, p := range prices {
		row := valuationObservation(fmt.Sprint(i), "2026-11-10", p)
		in.Observations = append(in.Observations, row)
	}
	old := valuationObservation("0", "2026-11-09", 1)
	in.Observations = append(in.Observations, old)
	seller := valuationObservation("seller expensive", "2026-11-10", 150)
	seller.SellerID = "s"
	cheap := valuationObservation("seller cheap", "2026-11-10", 125)
	cheap.SellerID = "s"
	in.Observations = append(in.Observations, seller, cheap)
	for _, kind := range []string{"dismissed", "excluded", "exclude word", "null price", "junk"} {
		row := valuationObservation(kind, "2026-11-10", 1)
		switch kind {
		case "dismissed":
			row.Dismissed = true
		case "excluded":
			row.Excluded = true
		case "exclude word":
			row.Title = "电池坏了"
		case "null price":
			row.Price = nil
		case "junk":
			row.Kind = KindBox
		}
		in.Observations = append(in.Observations, row)
	}
	in.ExcludeKeywords = []string{"坏了"}
	g := onlyMarket(t, in)
	if g.Fair.Sample != 6 || g.Fair.Cents == nil || *g.Fair.Cents != 123 {
		t.Fatalf("dedup=%+v", g)
	}
	for _, s := range g.Samples {
		if s.ItemID == "seller expensive" || s.Price == 1 {
			t.Fatalf("invalid sample=%+v", s)
		}
	}
	in.ExcludeKeywords = []string{"不存在"}
	if g := onlyMarket(t, in); g.Fair.Sample != 6 {
		t.Fatalf("outlier handling expected 1 removed: %+v", g)
	}
}

func TestMarketIQRThresholdAndWindowFallback(t *testing.T) {
	for _, tc := range []struct {
		prices []int64
		count  int
		known  bool
	}{{[]int64{100, 100, 10000}, 3, false}, {[]int64{100, 100, 100, 10000}, 3, false}, {[]int64{100, 100, 100, 100, 100, 10000}, 5, true}} {
		in := marketInput()
		for i, p := range tc.prices {
			in.Observations = append(in.Observations, valuationObservation(fmt.Sprint(i), "2026-11-10", p))
		}
		g := onlyMarket(t, in)
		if g.Fair.Sample != tc.count || (g.Fair.Cents != nil) != tc.known {
			t.Fatalf("prices=%v fair=%+v", tc.prices, g.Fair)
		}
	}
	in := marketInput()
	for i := 0; i < 5; i++ {
		day := "2026-11-10"
		if i == 4 {
			day = "2026-10-20"
		}
		in.Observations = append(in.Observations, valuationObservation(fmt.Sprint(i), day, 100))
	}
	g := onlyMarket(t, in)
	if g.Fair.Cents == nil || g.Fair.Days != 30 {
		t.Fatalf("30-day fallback=%+v", g.Fair)
	}
	in.Observations[4].Day = "2026-10-11"
	g = onlyMarket(t, in)
	if g.Fair.Cents != nil || g.Fair.Sample != 4 || g.Fair.DroppedSpec != nil {
		t.Fatalf("outside 30d=%+v", g.Fair)
	}
}

func TestMarketRelaxesOnlyLastSpecAndRetainsUnselectedSamples(t *testing.T) {
	in := marketInput()
	in.Profile.Specs = []ProfileSpec{{Name: "型号", Options: []string{"A", "B"}, Values: []string{"A"}}, {Name: "存储", Options: []string{"128", "256"}, Values: []string{"256"}}}
	for i := 0; i < 6; i++ {
		v := "128"
		if i < 2 {
			v = "256"
		}
		row := valuationObservation(fmt.Sprint(i), "2026-11-10", 100+int64(i))
		row.Specs = []SpecValue{{Name: "型号", Value: ptr("A")}, {Name: "存储", Value: ptr(v)}}
		in.Observations = append(in.Observations, row)
	}
	unknown := valuationObservation("unknown", "2026-11-10", 100)
	unknown.Specs = []SpecValue{{Name: "型号", Value: ptr("A")}}
	in.Observations = append(in.Observations, unknown)
	r := CalculateMarket(in)
	if len(r.Groups) != 2 {
		t.Fatalf("groups=%+v", r.Groups)
	}
	for _, g := range r.Groups {
		if g.Fair.Cents == nil || g.Fair.DroppedSpec == nil || *g.Fair.DroppedSpec != "存储" || g.Fair.Sample != 6 {
			t.Fatalf("relaxed=%+v", g)
		}
		if g.ConfigKey == "A / 128" && g.Selected {
			t.Fatal("unselected shown selected")
		}
	}
	for i := range in.Observations {
		if len(in.Observations[i].Specs) == 2 && *in.Observations[i].Specs[1].Value == "128" {
			in.Observations[i].Specs[0].Value = ptr("B")
		}
	}
	for _, g := range CalculateMarket(in).Groups {
		if g.Fair.Cents != nil {
			t.Fatal("recursively discarded two specs")
		}
	}
}

func TestMarketDailyTrendCheapestAndTimezone(t *testing.T) {
	in := marketInput()
	for day, n := range map[string]int64{"2026-10-20": 50, "2026-10-28": 100, "2026-11-10": 200} {
		for i := 0; i < 3; i++ {
			in.Observations = append(in.Observations, valuationObservation(fmt.Sprintf("%s-%d", day, i), day, n))
		}
	}
	in.LatestRunItemIDs = []string{"2026-11-10-0"}
	g := onlyMarket(t, in)
	if g.Trend == nil || g.Trend.FirstDay != "2026-10-28" || g.Trend.LastDay != "2026-11-10" || g.Trend.Change != 100 || g.Cheapest == nil || g.Cheapest.Price != 200 {
		t.Fatalf("trend/latest=%+v", g)
	}
	if r := CalculateMarket(in); len(r.TaskTrend) != 7 || r.TaskTrend[0].Day != "2026-11-04" || r.TaskTrend[0].Median != nil {
		t.Fatalf("task trend=%+v", r.TaskTrend)
	}
	in.NowMS = time.Date(2026, 11, 10, 0, 30, 0, 0, time.UTC).UnixMilli()
	in.TimeZone = "America/Los_Angeles"
	g = onlyMarket(t, in)
	for _, s := range g.Samples {
		if s.Day == "2026-11-10" {
			t.Fatal("future local day included")
		}
	}
	in.TimeZone = "bad/timezone"
	r := CalculateMarket(in)
	if len(r.Groups) != 0 || len(r.Warnings) != 1 {
		t.Fatalf("invalid timezone=%+v", r)
	}
}

func TestMarketNewUsedSeparationAdjacentAndCopies(t *testing.T) {
	in := marketInput()
	in.Profile.Specs = []ProfileSpec{{Name: "容量", Options: []string{"64", "256", "128"}}}
	for _, v := range []string{"64", "256", "128"} {
		for i := 0; i < 5; i++ {
			price := int64(100)
			if v == "256" {
				price = 200
			}
			if v == "128" {
				price = 300
			}
			row := valuationObservation(v+fmt.Sprint(i), "2026-11-10", price)
			row.Specs = []SpecValue{{Name: "容量", Value: ptr(v)}}
			in.Observations = append(in.Observations, row)
		}
	}
	newRow := valuationObservation("new", "2026-11-10", 500)
	newRow.Specs = []SpecValue{{Name: "容量", Value: ptr("128")}}
	newRow.BrandNew = ptr(true)
	in.Observations = append(in.Observations, newRow)
	r := CalculateMarket(in)
	if len(r.Groups) != 4 {
		t.Fatalf("new/used=%+v", r.Groups)
	}
	adj := AdjacentPrices(in.Profile, ItemSpecs{Specs: []SpecValue{{Name: "容量", Value: ptr("128")}}}, 250, r.Groups)
	if len(adj) != 1 || adj[0].From != "256" || adj[0].ItemPremium != 50 || adj[0].TypicalPremium != 100 {
		t.Fatalf("option adjacency=%+v", adj)
	}
	if adj := AdjacentPrices(in.Profile, ItemSpecs{Specs: []SpecValue{{Name: "容量", Value: ptr("128")}}, BrandNew: ptr(true)}, 250, r.Groups); len(adj) != 0 {
		t.Fatal("compared new against used")
	}
	*r.Groups[0].Specs[0].Value = "mutated"
	if *in.Observations[0].Specs[0].Value != "64" {
		t.Fatal("result aliases input")
	}
	if math.IsNaN(r.Groups[0].PriceBand.P25) {
		t.Fatal("invalid quantile")
	}
}

func TestMarketNewestUnknownPriceDoesNotUseOlderQuote(t *testing.T) {
	in := marketInput()
	old := valuationObservation("only", "2026-11-09", 999)
	latest := valuationObservation("only", "2026-11-10", 0)
	latest.Price = nil
	in.Observations = []MarketObservation{old, latest}
	if got := CalculateMarket(in); len(got.Groups) != 0 {
		t.Fatalf("unknown latest price reused old quote: %+v", got)
	}
	for i := 0; i < 5; i++ {
		in.Observations = append(in.Observations, valuationObservation(fmt.Sprint(i), "2026-11-10", 100))
	}
	if got := onlyMarket(t, in); got.Fair.Sample != 5 {
		t.Fatalf("unknown quote counted: %+v", got.Fair)
	}
}

func TestMarketCheapestMayBeOutlierAndDatesAreExplicit(t *testing.T) {
	in := marketInput()
	for i, p := range []int64{1, 100, 100, 100, 100, 100} {
		in.Observations = append(in.Observations, valuationObservation(fmt.Sprint(i), "2026-11-10", p))
	}
	in.LatestRunItemIDs = []string{"0"}
	g := onlyMarket(t, in)
	if g.Fair.Sample != 5 || g.Cheapest == nil || g.Cheapest.Price != 1 {
		t.Fatalf("current outlier cheapest hidden: %+v", g)
	}
	in.TimeZone = ""
	if got := CalculateMarket(in); len(got.Groups) != 0 || len(got.Warnings) != 1 {
		t.Fatalf("empty timezone accepted: %+v", got)
	}
	in = marketInput()
	in.Observations = []MarketObservation{valuationObservation("bad", "2026-11-40", 1)}
	if got := CalculateMarket(in); len(got.Groups) != 0 || len(got.Warnings) != 1 {
		t.Fatalf("invalid day accepted: %+v", got)
	}
}

func TestMarketNewUsedAndAllowedKindsDoNotMerge(t *testing.T) {
	in := marketInput()
	in.Profile.AllowedKinds = []Kind{KindBroken}
	for i := 0; i < 5; i++ {
		row := valuationObservation(fmt.Sprint(i), "2026-11-10", 100)
		row.Kind = KindBroken
		in.Observations = append(in.Observations, row)
	}
	newRow := valuationObservation("new", "2026-11-10", 200)
	newRow.BrandNew = ptr(true)
	in.Observations = append(in.Observations, newRow)
	got := CalculateMarket(in)
	if len(got.Groups) != 2 || got.Groups[0].Fair.Cents == nil || got.Groups[1].Fair.Cents != nil {
		t.Fatalf("new used/allowed kind=%+v", got)
	}
	in.Profile.AllowedKinds = nil
	got = CalculateMarket(in)
	if len(got.Groups) != 1 || !got.Groups[0].BrandNew {
		t.Fatalf("disallowed broken counted: %+v", got)
	}
}

func TestMarketTaskTrendUsesSelectedGroup(t *testing.T) {
	in := marketInput()
	in.Profile.Specs = []ProfileSpec{{Name: "型号", Options: []string{"A", "B"}, Values: []string{"A"}}}
	for i := 0; i < 8; i++ {
		value := "B"
		price := int64(200)
		if i < 3 {
			value = "A"
			price = 100
		}
		row := valuationObservation(fmt.Sprint(i), "2026-11-10", price)
		row.Specs = []SpecValue{{Name: "型号", Value: ptr(value)}}
		in.Observations = append(in.Observations, row)
	}
	got := CalculateMarket(in)
	if len(got.TaskTrend) != 7 || got.TaskTrend[6].Median == nil || *got.TaskTrend[6].Median != 100 {
		t.Fatalf("larger unselected group took chart: %+v", got.TaskTrend)
	}
	in.Observations = in.Observations[3:]
	got = CalculateMarket(in)
	for _, point := range got.TaskTrend {
		if point.Median != nil {
			t.Fatal("unselected chart shown")
		}
	}
}

func TestMarketFourteenDayTrendDoesNotFollowThirtyDayFairPrice(t *testing.T) {
	in := marketInput()
	for i := 0; i < 3; i++ {
		id := fmt.Sprint(i)
		in.Observations = append(in.Observations, valuationObservation(id, "2026-10-28", 100), valuationObservation(id, "2026-11-10", 200))
	}
	for i := 0; i < 2; i++ {
		in.Observations = append(in.Observations, valuationObservation("old"+fmt.Sprint(i), "2026-10-20", 100))
	}
	g := onlyMarket(t, in)
	if g.Fair.Days != 30 || g.Trend == nil || g.Trend.FirstDay != "2026-10-28" || g.Trend.FirstMedian != 100 || g.Trend.LastMedian != 200 {
		t.Fatalf("trend followed fair window: %+v", g)
	}
	if g.PriceBand.P25 != 100 || g.PriceBand.P75 != 200 {
		t.Fatalf("price band ignored 30d window: %+v", g.PriceBand)
	}
}

func TestMarketMedianCentsDoesNotOverflow(t *testing.T) {
	in := marketInput()
	for i := 0; i < 6; i++ {
		price := int64(math.MaxInt64 - 2)
		if i >= 3 {
			price++
		}
		in.Observations = append(in.Observations, valuationObservation(fmt.Sprint(i), "2026-11-10", price))
	}
	g := onlyMarket(t, in)
	if g.Fair.Cents == nil || *g.Fair.Cents != math.MaxInt64-1 {
		t.Fatalf("half-cent rounding overflowed: %+v", g.Fair)
	}
}

func TestAdjacentPricesNormalizesBothOptionAndResolvedValue(t *testing.T) {
	in := marketInput()
	in.Profile.Specs = []ProfileSpec{{Name: "容量", Options: []string{" ６４ ", "１２８ ", " ２５６ "}}}
	for _, capacity := range []string{"64", "128", "256"} {
		for i := 0; i < 5; i++ {
			price := int64(100)
			if capacity == "128" {
				price = 200
			}
			if capacity == "256" {
				price = 300
			}
			row := valuationObservation(capacity+fmt.Sprint(i), "2026-11-10", price)
			row.Specs = []SpecValue{{Name: "容量", Value: ptr(capacity)}}
			in.Observations = append(in.Observations, row)
		}
	}
	groups := CalculateMarket(in).Groups
	got := AdjacentPrices(in.Profile, ItemSpecs{Specs: []SpecValue{{Name: "容量", Value: ptr("256")}}}, 250, groups)
	if len(got) != 1 || got[0].LowerConfigKey != "128" || got[0].ItemPremium != 50 || got[0].TypicalPremium != 100 {
		t.Fatalf("full-width options lost adjacency: %+v", got)
	}
}

func TestMarketRejectsProcessLocalTimezone(t *testing.T) {
	in := marketInput()
	in.TimeZone = "Local"
	in.Observations = []MarketObservation{valuationObservation("1", "2026-11-10", 100)}
	got := CalculateMarket(in)
	if len(got.Groups) != 0 || len(got.Warnings) != 1 {
		t.Fatalf("不能使用进程隐式时区：%+v", got)
	}
}
