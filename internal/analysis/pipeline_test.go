package analysis

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestVerifiedFactsFeedScoringAndCannotBypassCaps(t *testing.T) {
	generated, _, err := ParseGeneratedProfile([]byte(`{"summary":"会帮你找：国行电脑","specs":[{"name":"存储","options":["256G"],"values":["256G"],"source":"user"}],"musts":[{"text":"国行","source":"user"}],"bonuses":[],"risks":[{"id":"id_lock","name":null,"hint":null,"question":null,"attitude":"confirm","source":"user"}],"allowed_kinds":[],"budget_min":null,"budget_max":null,"keyword":null,"exclude_words":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	profile, _, _, err := MergeGeneratedProfile(Profile{}, generated, "电脑", "国行，256G")
	if err != nil {
		t.Fatal(err)
	}
	factsJSON := fmt.Sprintf(`{"specs":[{"name":"存储","value":"２５６Ｇ"}],"brand_new":false,"musts":[{"id":%q,"status":"met","source":"text","evidence":"国行"}],"bonuses":[],"checks":[{"key":"id_lock","status":"risk","source":"text","evidence":"有ID锁"}],"seller_signals":[],"price_note":"急需用钱","extra_findings":[{"name":"亮斑","evidence":"有亮斑","source":"text"}],"adjust":{"value":8,"reason":"配件齐全"},"summary":"先核查账号"}`, profile.Musts[0].ID)
	facts, warnings, err := ParseFacts([]byte(factsJSON), profile, EvidenceOriginals{Title: "国行256G自用电脑", Description: "配件齐全"})
	if err != nil {
		t.Fatal(err)
	}
	if facts.Checks[0].Status != CheckSuspect || facts.PriceNote != nil || len(facts.ExtraFindings) != 0 || len(warnings) < 3 {
		t.Fatalf("未核对的引用未被降级/丢弃：%+v %v", facts, warnings)
	}
	observations := []MarketObservation{}
	for i, price := range []int64{98000, 99000, 100000, 101000, 102000, 103000} {
		observations = append(observations, MarketObservation{ItemID: fmt.Sprint(i), Title: "国行电脑", Day: "2026-11-10", Price: ptr(price), Kind: KindSale, Specs: []SpecValue{{Name: "存储", Value: ptr("256G")}}})
	}
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC).UnixMilli()
	market := CalculateMarket(MarketInput{Mode: ModeAI, Profile: profile, Observations: observations, NowMS: now, TimeZone: "UTC"})
	if len(market.Groups) != 1 || market.Groups[0].Fair.Cents == nil || *market.Groups[0].Fair.Cents != 100500 {
		t.Fatalf("合理价未按同配置计算：%+v", market)
	}
	input := ScoreInput{Profile: profile, Facts: facts, Item: ResolveSpecs(profile, facts.Specs, facts.BrandNew, SpecDetail), Price: ptr(int64(70000)), Fair: &market.Groups[0].Fair, Seller: JudgeSeller(SellerInput{RegistrationDays: ptr(100)}, nil, "电脑")}
	got := Score(input)
	if got.Score == nil || *got.Score != 89 || got.Grade == nil || *got.Grade != GradeGood || len(got.Breakdown.Gates) != 0 {
		t.Fatalf("伪造文字不能硬拒绝或突破确认封顶：%+v", got)
	}
	question := false
	for _, q := range got.Questions {
		question = question || q.Key == "id_lock"
	}
	if !question {
		t.Fatal("降级为疑似后没有追问")
	}
	if again := Score(input); !reflect.DeepEqual(got, again) {
		t.Fatal("同一事实重算结果不一致")
	}
	if got.Checks[0].Evidence == nil {
		t.Fatal("降级后应保留原引用供核查")
	}
	*got.Checks[0].Evidence = "修改结果"
	if facts.Checks[0].Evidence == nil || *facts.Checks[0].Evidence != "有ID锁" {
		t.Fatal("结果修改影响已保存事实")
	}
}

func TestMustOnlyProfileUsesUnconfiguredMarket(t *testing.T) {
	profile := Profile{Musts: []Requirement{{ID: "m1", Text: "国行", Source: SourceUser}}, NextNo: 2}
	item := ResolveSpecs(profile, nil, nil, SpecDetail)
	if item.ConfigKey == nil || *item.ConfigKey != "*" {
		t.Fatalf("无维度不是规格未知：%+v", item)
	}
	rows := []MarketObservation{}
	for i := 0; i < 5; i++ {
		rows = append(rows, MarketObservation{ItemID: fmt.Sprint(i), Title: "电脑", Kind: KindSale, Price: ptr(int64(10000 + i*100)), Day: "2026-11-10"})
	}
	market := CalculateMarket(MarketInput{Mode: ModeAI, Profile: profile, Observations: rows, NowMS: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC).UnixMilli(), TimeZone: "UTC"})
	if len(market.Groups) != 1 || market.Groups[0].ConfigKey != "*" || market.Groups[0].Fair.Cents == nil || market.Groups[0].Fair.DroppedSpec != nil {
		t.Fatalf("无规格AI任务应直接同组比价：%+v", market)
	}
	facts, _, err := ParseFacts([]byte(`{"checks":[],"musts":[{"id":"m1","status":"unmet","source":"text","evidence":"不是国行"}],"adjust":{"value":8,"reason":"便宜"},"summary":"非国行"}`), profile, EvidenceOriginals{Title: "不是国行"})
	if err != nil {
		t.Fatal(err)
	}
	got := Score(ScoreInput{Profile: profile, Facts: facts, Item: item, Price: ptr(int64(7000)), Fair: &market.Groups[0].Fair})
	if got.Decision != DecisionRejected || !got.Excluded || got.Score != nil || got.Grade != nil {
		t.Fatalf("可比价不能突破硬条件：%+v", got)
	}
}
