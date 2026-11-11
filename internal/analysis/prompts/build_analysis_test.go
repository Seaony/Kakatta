package prompts

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/locales"
)

func analysisTestPtr[T any](value T) *T { return &value }

func analysisTestSnapshot(t *testing.T) Snapshot {
	t.Helper()
	snapshot, err := Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func analysisTestBlocks(t *testing.T, user string) (map[string]any, map[string]any, []string) {
	t.Helper()
	parts := strings.Split(user, "```json\n")
	if len(parts) != 3 || parts[0] != "## 需求清单\n" {
		t.Fatalf("分析消息结构错误：%s", user)
	}
	values := make([]map[string]any, 2)
	blocks := make([]string, 2)
	for i := range 2 {
		block, after, found := strings.Cut(parts[i+1], "\n```")
		if !found || i == 0 && after != "\n\n## 商品信息\n" {
			t.Fatalf("缺少固定分段：%s", parts[i+1])
		}
		blocks[i] = block
		decoder := json.NewDecoder(strings.NewReader(block))
		decoder.UseNumber()
		if err := decoder.Decode(&values[i]); err != nil {
			t.Fatal(err)
		}
	}
	return values[0], values[1], blocks
}

func analysisTestKeys(t *testing.T, object map[string]any, expected ...string) {
	t.Helper()
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	sort.Strings(expected)
	if !slices.Equal(keys, expected) {
		t.Fatalf("字段白名单错误：得到%v，应为%v", keys, expected)
	}
}

func TestBuildAnalysisProjectsWhitelistAndPreciseMoney(t *testing.T) {
	snapshot := analysisTestSnapshot(t)
	profile := analysis.Profile{
		Summary: "内部摘要", NextNo: 99,
		Specs:   []analysis.ProfileSpec{{Name: "存储", Options: []string{"128G", "256G"}, Values: []string{"256G"}, Source: analysis.SourceAI, Edited: true}},
		Musts:   []analysis.Requirement{{ID: "m2", Text: "国行", Source: analysis.SourceUser, Edited: true, Rev: 4}},
		Bonuses: []analysis.Requirement{{ID: "b4", Text: "带票", Source: analysis.SourceAI, Rev: 5}},
		Risks: []analysis.ProfileRisk{
			{ID: "id_lock", Name: "不该使用", Hint: "不该使用", Attitude: analysis.Ignore, Source: analysis.SourceAI, Rev: 6},
			{ID: "seller_new", Attitude: analysis.Confirm, Source: analysis.SourceSystem, Rev: 1},
			{ID: "c8", Name: "亮斑", Question: "问句不发", Attitude: analysis.Ignore, Source: analysis.SourceUser, Edited: true, Rev: 7},
		},
	}
	feedback := strings.Repeat("鱼", 60) + "不该发出的评价尾部"
	item := ProductSource{
		Title: "中文<鱼>&", PriceCents: analysisTestPtr(int64(101)), OriginalPriceCents: analysisTestPtr(int64(0)),
		Description: analysisTestPtr(""), PublishedAtMS: analysisTestPtr(int64(0)), Area: analysisTestPtr(""), Tags: []string{}, WantCount: analysisTestPtr(int64(0)), URL: analysisTestPtr(""),
		Seller: &SellerSource{
			Nickname: analysisTestPtr(""), RegistrationDays: analysisTestPtr(365), Introduction: analysisTestPtr("工作室"), ZhimaLevel: analysisTestPtr(""), SellerCredit: analysisTestPtr("信用极好"),
			ItemCount: analysisTestPtr(int64(0)), OnSaleCount: analysisTestPtr(int64(0)), SoldCount: analysisTestPtr(int64(0)), RateCount: analysisTestPtr(int64(0)), SellerGoodRate: analysisTestPtr(0.0), BuyerGoodRate: analysisTestPtr(1.0),
			RecentRates: []RecentRateSource{
				{Feedback: &feedback, Role: analysisTestPtr("buyer"), Kind: analysisTestPtr("bad"), PublishedAtMS: analysisTestPtr(int64(0))},
				{Feedback: analysisTestPtr(""), Role: analysisTestPtr("unknown"), Kind: analysisTestPtr("unknown")},
			},
			RecentItems: []RecentItemSource{{Title: "闲置", PriceCents: analysisTestPtr(int64(100)), Status: analysisTestPtr("on_sale")}},
		},
	}
	fair := &analysis.FairPrice{Cents: analysisTestPtr(int64(12345)), Sample: 6, Basis: locales.Message{Code: "analysis.fair_same_config", Args: map[string]any{"days": 14, "count": 14}}}
	id := int64(7)
	request, originals, err := BuildAnalysis(snapshot, &id, profile, item, fair, []string{"data:image/jpeg;base64,AA=="})
	if err != nil {
		t.Fatal(err)
	}
	if request.Kind != client.Analysis || request.TaskID == nil || *request.TaskID != id || request.System != snapshot.Contents[client.Analysis]+"\n\n"+SellerDataSystemSuffix || request.Schema == nil || request.Schema.Name != "listing_facts" || request.Validate == nil {
		t.Fatalf("分析请求元数据错误：%+v", request)
	}
	wireProfile, product, blocks := analysisTestBlocks(t, request.User)
	analysisTestKeys(t, wireProfile, "specs", "musts", "bonuses", "checks", "fair_price")
	if got := wireProfile["fair_price"].(map[string]any); got["price"] != json.Number("123.45") || got["sample"] != json.Number("6") || got["basis"] != "同配置 · 近 14 天" {
		t.Fatalf("合理价没有按元/中文依据发送：%v", got)
	}
	checks := wireProfile["checks"].([]any)
	if len(checks) != 2 {
		t.Fatalf("只排卖家检查项，不能丢掉ignore：%v", checks)
	}
	first, second := checks[0].(map[string]any), checks[1].(map[string]any)
	analysisTestKeys(t, first, "key", "name", "hint")
	if first["key"] != "id_lock" || first["name"] != "ID 锁" || first["hint"] != "Apple ID、小米账号、华为账号没退出，或有激活锁" || second["hint"] != "亮斑" {
		t.Fatalf("没有使用ResolveRisks名称和hint：%v", checks)
	}
	analysisTestKeys(t, wireProfile["musts"].([]any)[0].(map[string]any), "id", "text")
	analysisTestKeys(t, wireProfile["bonuses"].([]any)[0].(map[string]any), "id", "text")
	analysisTestKeys(t, wireProfile["specs"].([]any)[0].(map[string]any), "name", "options", "values")
	analysisTestKeys(t, product, "title", "price", "original_price", "description", "published_at", "area", "tags", "want_count", "url", "seller")
	if product["price"] != json.Number("1.01") || product["original_price"] != json.Number("0") || product["published_at"] != json.Number("0") || product["want_count"] != json.Number("0") || product["description"] != "" || product["area"] != "" || len(product["tags"].([]any)) != 0 {
		t.Fatalf("已知0或空值被当成未知：%v", product)
	}
	seller := product["seller"].(map[string]any)
	analysisTestKeys(t, seller, "nickname", "registration", "introduction", "zhima_level", "seller_credit", "item_count", "on_sale_count", "sold_count", "rate_count", "seller_good_rate", "buyer_good_rate", "recent_rates", "recent_items")
	if seller["registration"] != "来闲鱼 1 年 0 个月" || seller["seller_good_rate"] != json.Number("0") || seller["buyer_good_rate"] != json.Number("1") || seller["seller_credit"] != "信用极好" {
		t.Fatalf("卖家原有文字/时长/0–1比例被改动：%v", seller)
	}
	rate := seller["recent_rates"].([]any)[0].(map[string]any)
	analysisTestKeys(t, rate, "feedback", "role", "kind", "published_at")
	if rate["feedback"] != strings.Repeat("鱼", 60) || !reflect.DeepEqual(originals.RecentRateTexts, []string{strings.Repeat("鱼", 60), ""}) || originals.Title != item.Title || originals.SellerIntroduction != "工作室" || originals.Description != "" {
		t.Fatalf("模型和证据没有同源60字摘要：%v %+v", rate, originals)
	}
	recent := seller["recent_items"].([]any)[0].(map[string]any)
	analysisTestKeys(t, recent, "title", "price", "status")
	if recent["price"] != json.Number("1") {
		t.Fatalf("卖家商品价格没有转元：%v", recent)
	}
	for _, block := range blocks {
		if strings.Contains(block, `\u003c`) || strings.Contains(block, `\u9c7c`) || strings.Contains(block, `"rev"`) {
			t.Fatalf("内部版本或被转义中文进入模型消息：%s", block)
		}
	}
	if strings.Contains(string(request.Schema.JSON), `"rev"`) || strings.Contains(request.User, analysisNoImages) {
		t.Fatal("模型Schema包含rev或有图时追加了无图说明")
	}
}

func TestBuildAnalysisDistinguishesUnknownAndEmptyData(t *testing.T) {
	for _, seller := range []*SellerSource{nil, {}, {RegistrationDays: analysisTestPtr(0)}} {
		request, originals, err := BuildAnalysis(analysisTestSnapshot(t), nil, analysis.Profile{}, ProductSource{Title: "普通", Seller: seller}, &analysis.FairPrice{Sample: 9}, nil)
		if err != nil {
			t.Fatal(err)
		}
		profile, product, blocks := analysisTestBlocks(t, request.User)
		if profile["fair_price"] != nil {
			t.Fatal("没有Cents时整个合理价应为null")
		}
		if seller == nil {
			analysisTestKeys(t, product, "title")
		} else {
			analysisTestKeys(t, product, "title", "seller")
			analysisTestKeys(t, product["seller"].(map[string]any))
		}
		if blocks[0] != `{"bonuses":[],"checks":[],"fair_price":null,"musts":[],"specs":[]}` || !strings.HasSuffix(request.User, "\n\n"+analysisNoImages) || strings.Count(request.User, analysisNoImages) != 1 {
			t.Fatalf("空数组/固定JSON顺序/无图说明错误：%s", request.User)
		}
		if originals.Title != "普通" || originals.Description != "" || originals.SellerIntroduction != "" || len(originals.RecentRateTexts) != 0 {
			t.Fatalf("未知资料被构造成伪证据：%+v", originals)
		}
	}
	item := ProductSource{Title: "普通", Tags: []string{}, Seller: &SellerSource{RecentRates: []RecentRateSource{}, RecentItems: []RecentItemSource{}}}
	request, _, err := BuildAnalysis(analysisTestSnapshot(t), nil, analysis.Profile{}, item, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, product, _ := analysisTestBlocks(t, request.User)
	seller := product["seller"].(map[string]any)
	if len(product["tags"].([]any)) != 0 || len(seller["recent_rates"].([]any)) != 0 || len(seller["recent_items"].([]any)) != 0 {
		t.Fatal("已取得的空列表应保留空数组")
	}
}

func TestBuildAnalysisLimitsSellerRowsAndUsesOnlyTheirSummaries(t *testing.T) {
	item := ProductSource{Title: "商品", Seller: &SellerSource{}}
	for i := range 15 {
		item.Seller.RecentRates = append(item.Seller.RecentRates, RecentRateSource{Feedback: analysisTestPtr(fmt.Sprintf("第%d条%s", i, strings.Repeat("长", 80)))})
	}
	for i := range 25 {
		item.Seller.RecentItems = append(item.Seller.RecentItems, RecentItemSource{Title: fmt.Sprintf("第%d件", i)})
	}
	request, originals, err := BuildAnalysis(analysisTestSnapshot(t), nil, analysis.Profile{}, item, nil, []string{"data:image/jpeg;base64,AA=="})
	if err != nil {
		t.Fatal(err)
	}
	_, product, _ := analysisTestBlocks(t, request.User)
	seller := product["seller"].(map[string]any)
	rates, items := seller["recent_rates"].([]any), seller["recent_items"].([]any)
	if len(rates) != 10 || len(items) != 20 || len(originals.RecentRateTexts) != 10 {
		t.Fatalf("卖家列表应保留前10/20条：%d/%d/%d", len(rates), len(items), len(originals.RecentRateTexts))
	}
	for i, row := range rates {
		if row.(map[string]any)["feedback"] != originals.RecentRateTexts[i] || len([]rune(originals.RecentRateTexts[i])) != 60 {
			t.Fatalf("第%d条不是同源60字摘要：%v", i, row)
		}
	}
	if len(item.Seller.RecentRates) != 15 || len(item.Seller.RecentItems) != 25 || len(*item.Seller.RecentRates[0].Feedback) <= 60 {
		t.Fatal("构造请求改写了来源快照")
	}
}

func TestBuildAnalysisKeepsMoneyPrecision(t *testing.T) {
	for _, value := range []int64{0, 1, 100, 101, math.MaxInt64} {
		request, _, err := BuildAnalysis(analysisTestSnapshot(t), nil, analysis.Profile{}, ProductSource{Title: "商品", PriceCents: &value}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, product, _ := analysisTestBlocks(t, request.User)
		got := product["price"].(json.Number).String()
		want := map[int64]string{0: "0", 1: "0.01", 100: "1", 101: "1.01", math.MaxInt64: "92233720368547758.07"}[value]
		if got != want {
			t.Fatalf("%d分变成%s元，应为%s", value, got, want)
		}
	}
}

func TestBuildAnalysisValidateUsesIndependentProfileRevisionsAndOriginals(t *testing.T) {
	profile := analysis.Profile{
		Musts:   []analysis.Requirement{{ID: "m1", Text: "国行", Rev: 3}},
		Bonuses: []analysis.Requirement{{ID: "b2", Text: "带票", Rev: 4}},
		Risks:   []analysis.ProfileRisk{{ID: "c3", Name: "锁", Attitude: analysis.Confirm, Rev: 5}},
	}
	feedback := "发货慢"
	item := ProductSource{Title: "国行有锁", Description: analysisTestPtr("带票"), Seller: &SellerSource{RecentRates: []RecentRateSource{{Feedback: &feedback}}}}
	id := int64(7)
	images := []string{"data:image/jpeg;base64,AA==", "data:image/jpeg;base64,AQ=="}
	request, originals, err := BuildAnalysis(analysisTestSnapshot(t), &id, profile, item, nil, images)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"musts":[{"id":"m1","status":"met","source":"text","evidence":"国行","rev":999}],"bonuses":[{"id":"b2","status":"met","source":"text","evidence":"带票","rev":999}],"checks":[{"key":"c3","status":"risk","source":"text","evidence":"有锁","rev":999}],"seller_signals":[{"key":"slow_ship","evidence":"发货慢"}],"adjust":{"value":0},"summary":"核查"}`
	if err := request.Validate(body); err != nil {
		t.Fatal(err)
	}
	facts, _, err := analysis.ParseFacts([]byte(body), profile, originals)
	if err != nil || facts.Musts[0].Rev != 3 || facts.Bonuses[0].Rev != 4 || facts.Checks[0].Rev != 5 {
		t.Fatalf("版本应由请求清单补入，不信模型rev：%+v %v", facts, err)
	}
	profile.Musts[0].Rev = -1
	profile.Bonuses[0].Rev = -1
	profile.Risks[0].Rev = -1
	id = 99
	images[0] = "改变来源图片"
	if *request.TaskID != 7 || request.Images[0] != "data:image/jpeg;base64,AA==" {
		t.Fatal("请求与调用方的任务或图片切片共享")
	}
	request.Images[1] = "改变请求图片"
	if images[1] != "data:image/jpeg;base64,AQ==" {
		t.Fatal("请求图片修改影响来源")
	}
	var wg sync.WaitGroup
	wg.Add(1)
	var validationErr error
	go func() {
		defer wg.Done()
		for range 100 {
			if err := request.Validate(body); err != nil {
				validationErr = err
				return
			}
		}
	}()
	for i := range 100 {
		originals.RecentRateTexts[0] = fmt.Sprint(i)
	}
	wg.Wait()
	if validationErr != nil {
		t.Fatalf("调用方修改清单或Originals影响Validate快照：%v", validationErr)
	}
}

func TestBuildAnalysisWarningsAreNotValidationFailures(t *testing.T) {
	profile := analysis.Profile{Risks: []analysis.ProfileRisk{{ID: "id_lock", Attitude: analysis.Confirm, Rev: 1}}}
	request, originals, err := BuildAnalysis(analysisTestSnapshot(t), nil, profile, ProductSource{Title: "正常商品"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"checks":[{"key":"id_lock","status":"risk","source":"text","evidence":"原文不存在"}],"adjust":{"value":0},"summary":"核查"}`
	_, warnings, parseErr := analysis.ParseFacts([]byte(body), profile, originals)
	if parseErr != nil || len(warnings) == 0 || request.Validate(body) != nil {
		t.Fatalf("证据warning不能触发模型重试：%v %v", warnings, parseErr)
	}
	for _, bad := range []string{`{}`, `{"checks":null,"adjust":{"value":0},"summary":"x"}`, `{"checks":[],"adjust":{"value":"bad"},"summary":"x"}`} {
		if request.Validate(bad) == nil {
			t.Fatalf("必需字段不合法应失败：%s", bad)
		}
	}
}

func TestBuildAnalysisRejectsInvalidRateOrFairBasis(t *testing.T) {
	for _, rate := range []float64{-0.1, 1.1, math.NaN(), math.Inf(1)} {
		_, _, err := BuildAnalysis(analysisTestSnapshot(t), nil, analysis.Profile{}, ProductSource{Seller: &SellerSource{SellerGoodRate: &rate}}, nil, nil)
		if err == nil {
			t.Fatalf("无效好评率不能发出请求：%v", rate)
		}
	}
	_, _, err := BuildAnalysis(analysisTestSnapshot(t), nil, analysis.Profile{}, ProductSource{}, &analysis.FairPrice{Cents: analysisTestPtr(int64(100)), Basis: locales.Message{Code: "not.a.real.code"}}, nil)
	if err == nil {
		t.Fatal("无法格式化中文合理价依据时不应发送机器代码")
	}
	if _, _, err := BuildAnalysis(Snapshot{}, nil, analysis.Profile{}, ProductSource{}, nil, nil); err == nil {
		t.Fatal("坏提示词快照不能构造请求")
	}
}
