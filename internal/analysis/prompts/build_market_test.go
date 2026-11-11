package prompts

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/locales"
)

func marketSnapshot(system string) Snapshot {
	return Snapshot{AssetVersion: AssetVersion, Contents: map[client.Kind]string{
		client.TitleScreen: `"items" "kind" "verdict"`,
		client.Analysis:    `"checks" "adjust" "summary"`,
		client.Profile:     `"specs" "risks"`,
		client.Market:      system,
	}}
}

func marketPointer[T any](v T) *T { return &v }

func marketValidGroup() analysis.MarketGroup {
	return analysis.MarketGroup{
		Selected: true,
		Fair: analysis.FairPrice{Cents: marketPointer(int64(100)), Sample: 5,
			Basis: locales.Message{Code: "analysis.fair_same_config", Args: map[string]any{"days": 14, "count": 14}}},
	}
}

func marketDecode(t *testing.T, text string) map[string]any {
	t.Helper()
	var result map[string]any
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func marketGroup(t *testing.T, payload map[string]any, index int) map[string]any {
	t.Helper()
	groups, ok := payload["groups"].([]any)
	if !ok || len(groups) <= index {
		t.Fatalf("没有行情组%d：%+v", index, payload)
	}
	group, ok := groups[index].(map[string]any)
	if !ok {
		t.Fatalf("行情组不是对象：%+v", groups[index])
	}
	return group
}

func marketNumber(t *testing.T, value any, want string) {
	t.Helper()
	number, ok := value.(json.Number)
	if !ok || string(number) != want {
		t.Fatalf("金额或比率不是精确数值：得到%#v，应为%s", value, want)
	}
}

func TestMarketBuildSelectedOverviewOnly(t *testing.T) {
	system := " \n自定义模板 <保留>&，不额外限制格式\n"
	id, snapshotAt := int64(7), int64(1_700_000_000_000)
	group := marketValidGroup()
	group.ConfigKey = "不应外发的配置键"
	group.BrandNew = true
	group.Specs = []analysis.SpecValue{{Name: "型号", Value: marketPointer("平板 <A> & 256G")}, {Name: "存储", Value: marketPointer("256G")}}
	group.Fair.Cents = marketPointer(int64(199950))
	group.Fair.Basis = locales.Message{Code: "analysis.fair_relaxed", Args: map[string]any{"days": 30, "count": 30, "name": "网络"}}
	group.Fair.ItemIDs = []string{"private-fair-item"}
	group.Samples = []analysis.MarketSample{{ItemID: "private-sample-item", SellerID: "private-seller"}}
	group.PriceBand = &analysis.PriceBand{P25: 199949.5, P75: 200000.25}
	group.Cheapest = &analysis.MarketSample{ItemID: "private-cheapest-item", SellerID: "private-seller", Price: 199949}
	group.Daily = []analysis.DailyPrice{{Day: "2026-11-10"}, {Day: "2026-11-11", Median: marketPointer(199949.5), Sample: 5}}
	group.Trend = &analysis.PriceTrend{FirstDay: "2026-10-29", LastDay: "2026-11-11", FirstMedian: 200000.25, LastMedian: 199949.5, Change: -50.75, Percent: marketPointer(-0.0002537496828128965)}
	unselected := marketValidGroup()
	unselected.Selected = false
	unselected.Specs = []analysis.SpecValue{{Name: "private-unselected", Value: marketPointer("private-value")}}
	unselected.PriceBand = &analysis.PriceBand{P25: math.NaN()}
	stats := analysis.MarketResult{Groups: []analysis.MarketGroup{unselected, group}, SnapshotAt: &snapshotAt,
		TaskTrend: []analysis.DailyPrice{{Day: "private-task-trend"}},
		Warnings:  []locales.Message{{Code: "private-warning"}}}
	request, err := BuildMarket(marketSnapshot(system), &id, stats)
	if err != nil {
		t.Fatal(err)
	}
	if request.Kind != client.Market || request.System != system+"\n\n"+SellerDataSystemSuffix || request.TaskID == nil || *request.TaskID != 7 || request.TaskID == &id || request.Images != nil || request.Schema != nil || request.Validate != nil {
		t.Fatalf("行情请求配置错误：%+v", request)
	}
	payload := marketDecode(t, request.User)
	if len(payload) != 2 || len(payload["groups"].([]any)) != 1 {
		t.Fatalf("只应发送选中组和快照时间：%+v", payload)
	}
	marketNumber(t, payload["snapshot_at"], "1700000000000")
	wire := marketGroup(t, payload, 0)
	if len(wire) != 7 || wire["brand_new"] != true {
		t.Fatalf("一览字段不符：%+v", wire)
	}
	fair := wire["fair_price"].(map[string]any)
	marketNumber(t, fair["price"], "1999.5")
	marketNumber(t, fair["sample"], "5")
	if fair["basis"] != "放宽到 30 天 · 不分网络" {
		t.Fatalf("依据应是共享中文文案：%+v", fair)
	}
	band := wire["price_band"].(map[string]any)
	marketNumber(t, band["p25"], "1999.495")
	marketNumber(t, band["p75"], "2000.0025")
	marketNumber(t, wire["cheapest_price"], "1999.49")
	trend := wire["trend"].(map[string]any)
	marketNumber(t, trend["first_median"], "2000.0025")
	marketNumber(t, trend["last_median"], "1999.495")
	marketNumber(t, trend["change"], "-0.5075")
	percent, ok := trend["percent"].(json.Number)
	if !ok {
		t.Fatalf("原比率必须保留数值：%#v", trend["percent"])
	}
	parsedPercent, err := percent.Float64()
	if err != nil || parsedPercent != *group.Trend.Percent {
		t.Fatalf("比率被改成了百分数：%s", percent)
	}
	if trend["first_day"] != group.Trend.FirstDay || trend["last_day"] != group.Trend.LastDay {
		t.Fatalf("不能重算或扩展走势日期：%+v", trend)
	}
	daily := wire["daily"].([]any)
	if len(daily) != 2 || daily[0].(map[string]any)["median"] != nil || daily[0].(map[string]any)["day"] != "2026-11-10" {
		t.Fatalf("无样本日应保留null及原序：%+v", daily)
	}
	marketNumber(t, daily[0].(map[string]any)["sample"], "0")
	marketNumber(t, daily[1].(map[string]any)["median"], "1999.495")
	if !strings.Contains(request.User, "平板 <A> & 256G") || strings.Contains(request.User, `\u003c`) {
		t.Fatalf("中文和HTML字符应原样保留：%s", request.User)
	}
	for _, private := range []string{"private-", "不应外发的配置键", "item_ids", "warnings", "task_trend", "samples"} {
		if strings.Contains(request.User, private) {
			t.Fatalf("泄露模型不需要的内部字段%s：%s", private, request.User)
		}
	}
}

func TestMarketBuildUnavailableFairAndEmptyStatistics(t *testing.T) {
	group := marketValidGroup()
	group.Fair.Cents = nil
	group.Fair.Sample = 3
	group.Fair.Basis = locales.Message{Code: "analysis.fair_unavailable", Args: map[string]any{"count": 3}}
	request, err := BuildMarket(marketSnapshot(""), nil, analysis.MarketResult{Groups: []analysis.MarketGroup{group}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"groups":[{"brand_new":false,"cheapest_price":null,"daily":[],"fair_price":{"basis":"无法比价（同配置只有 3 个）","price":null,"sample":3},"price_band":null,"specs":[],"trend":null}],"snapshot_at":null}`
	if strings.TrimSpace(request.User) != want {
		t.Fatalf("无法比价应保留对象、样本和依据，键按字母序：\n%s\n%s", request.User, want)
	}
	if request.System != "\n\n"+SellerDataSystemSuffix || request.TaskID != nil {
		t.Fatal("不能补回默认系统消息或伪造任务编号")
	}
	request, err = BuildMarket(marketSnapshot("行情"), nil, analysis.MarketResult{})
	if err != nil || strings.TrimSpace(request.User) != `{"groups":[],"snapshot_at":null}` {
		t.Fatalf("空统计应保留空数组和null：%s %v", request.User, err)
	}
}

func TestMarketBuildPreservesHalfCentsAndLargeIntegerPrices(t *testing.T) {
	group := marketValidGroup()
	group.Fair.Cents = marketPointer(int64(math.MaxInt64))
	group.PriceBand = &analysis.PriceBand{P25: 0.5, P75: 101.25}
	group.Daily = []analysis.DailyPrice{{Day: "2026-11-11", Median: marketPointer(100.5), Sample: 5}}
	group.Trend = &analysis.PriceTrend{FirstMedian: 101, LastMedian: 100.5, Change: -0.5}
	request, err := BuildMarket(marketSnapshot("行情"), nil, analysis.MarketResult{Groups: []analysis.MarketGroup{group}})
	if err != nil {
		t.Fatal(err)
	}
	wire := marketGroup(t, marketDecode(t, request.User), 0)
	marketNumber(t, wire["fair_price"].(map[string]any)["price"], "92233720368547758.07")
	marketNumber(t, wire["price_band"].(map[string]any)["p25"], "0.005")
	marketNumber(t, wire["price_band"].(map[string]any)["p75"], "1.0125")
	marketNumber(t, wire["daily"].([]any)[0].(map[string]any)["median"], "1.005")
	trend := wire["trend"].(map[string]any)
	marketNumber(t, trend["change"], "-0.005")
	if trend["percent"] != nil {
		t.Fatal("没有比率时不能计算补值")
	}
}

func TestMarketBuildRejectsInvalidNumericStatistics(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*analysis.MarketGroup, float64)
	}{
		{"p25", func(g *analysis.MarketGroup, v float64) { g.PriceBand = &analysis.PriceBand{P25: v} }},
		{"p75", func(g *analysis.MarketGroup, v float64) { g.PriceBand = &analysis.PriceBand{P75: v} }},
		{"daily", func(g *analysis.MarketGroup, v float64) { g.Daily = []analysis.DailyPrice{{Median: &v}} }},
		{"trend_first", func(g *analysis.MarketGroup, v float64) { g.Trend = &analysis.PriceTrend{FirstMedian: v} }},
		{"trend_last", func(g *analysis.MarketGroup, v float64) { g.Trend = &analysis.PriceTrend{LastMedian: v} }},
		{"trend_change", func(g *analysis.MarketGroup, v float64) { g.Trend = &analysis.PriceTrend{Change: v} }},
		{"trend_percent", func(g *analysis.MarketGroup, v float64) { g.Trend = &analysis.PriceTrend{Percent: &v} }},
	} {
		for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
			t.Run(tc.name+"/"+marketNumberName(value), func(t *testing.T) {
				group := marketValidGroup()
				tc.set(&group, value)
				request, err := BuildMarket(marketSnapshot("行情"), nil, analysis.MarketResult{Groups: []analysis.MarketGroup{group}})
				if err == nil || request.Kind != "" {
					t.Fatalf("无效统计不能得到可发送的请求：%+v %v", request, err)
				}
			})
		}
	}
}

func marketNumberName(value float64) string {
	if math.IsNaN(value) {
		return "nan"
	}
	if math.IsInf(value, 1) {
		return "positive_inf"
	}
	return "negative_inf"
}

func TestMarketBuildPropagatesInvalidSnapshotAndBasis(t *testing.T) {
	for _, tc := range []struct {
		name     string
		snapshot Snapshot
		group    analysis.MarketGroup
	}{
		{"version", Snapshot{AssetVersion: AssetVersion + 1}, marketValidGroup()},
		{"missing_market", Snapshot{AssetVersion: AssetVersion, Contents: map[client.Kind]string{}}, marketValidGroup()},
		{"unknown_basis", marketSnapshot("行情"), analysis.MarketGroup{Selected: true, Fair: analysis.FairPrice{Basis: locales.Message{Code: "missing.market.basis"}}}},
		{"basis_args", marketSnapshot("行情"), analysis.MarketGroup{Selected: true, Fair: analysis.FairPrice{Basis: locales.Message{Code: "analysis.fair_same_config"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := BuildMarket(tc.snapshot, nil, analysis.MarketResult{Groups: []analysis.MarketGroup{tc.group}})
			if err == nil || request.Kind != "" {
				t.Fatalf("共享校验失败不能生成请求：%+v %v", request, err)
			}
		})
	}
}

func TestMarketBuildDoesNotAliasOrMutateCallerData(t *testing.T) {
	id := int64(8)
	group := marketValidGroup()
	group.Specs = []analysis.SpecValue{{Name: "型号", Value: marketPointer("原型号")}}
	group.Daily = []analysis.DailyPrice{{Day: "2026-11-11", Median: marketPointer(100.5), Sample: 5}}
	stats := analysis.MarketResult{Groups: []analysis.MarketGroup{group}, SnapshotAt: marketPointer(int64(123))}
	before, err := json.Marshal(stats)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := marketSnapshot("原模板")
	request, err := BuildMarket(snapshot, &id, stats)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(stats)
	if err != nil || string(after) != string(before) {
		t.Fatalf("构造器改了来源快照：%s %s %v", before, after, err)
	}
	originalUser := request.User
	id = 99
	snapshot.Contents[client.Market] = "新模板"
	*stats.Groups[0].Specs[0].Value = "新型号"
	*stats.Groups[0].Daily[0].Median = 999
	*stats.SnapshotAt = 999
	if request.TaskID == nil || *request.TaskID != 8 || request.System != "原模板\n\n"+SellerDataSystemSuffix || request.User != originalUser {
		t.Fatalf("请求保留了调用方可变引用：%+v", request)
	}
}
