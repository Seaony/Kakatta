package analysis

import (
	"math"
	"reflect"
	"testing"
)

func TestKeywordNormalizationAndBoundaries(t *testing.T) {
	if got := ParseKeywords(" Ｑ１，q1\n  iPad\t MINI,收音机,  "); !reflect.DeepEqual(got, []string{"q1", "ipad mini", "收音机"}) {
		t.Fatal(got)
	}
	for _, tt := range []struct {
		text, word string
		want       bool
	}{
		{"q1r5", "q1", false}, {"q1r5 q1", "q1", true}, {"q1 q1r5", "q1", true}, {"aq1 q1b", "q1", false}, {"中Ｑ１文", "q1", true}, {"iPad\t mini 6", "ipad mini", true}, {"收音机", "音机", true},
	} {
		t.Run(tt.text+tt.word, func(t *testing.T) {
			got := MatchingKeywords(tt.text, []string{tt.word})
			if (len(got) > 0) != tt.want {
				t.Fatal(got)
			}
		})
	}
	if got := JudgeKeyword("Q1", "有锁", []string{"q1"}, []string{"有锁"}); !got.Rejected || got.RejectKind != RejectExcluded {
		t.Fatal(got)
	}
	if got := JudgeKeyword("普通商品", "", nil, nil); got.Rejected || got.Reason == nil {
		t.Fatal(got)
	}
	if got := JudgeKeyword("普通商品", "", []string{"q1"}, nil); !got.Rejected || got.RejectKind != RejectKeywordMiss {
		t.Fatal(got)
	}
	if got := JudgeKeyword("普通商品", "Q1", []string{"q1"}, nil); got.Rejected || len(got.MatchedKeywords) != 1 {
		t.Fatal(got)
	}
}

func TestKeywordJunkKinds(t *testing.T) {
	for _, tt := range []struct {
		title string
		kind  Kind
	}{
		{"求购相机", KindWanted}, {"收购相机", KindWanted}, {"收一台", KindWanted}, {"收个镜头", KindWanted}, {"收台电脑", KindWanted}, {"收部手机", KindWanted}, {"收只手表", KindWanted}, {"收套衣服", KindWanted}, {"收件大衣", KindWanted}, {"收副耳机", KindWanted}, {"收张卡", KindWanted}, {"北京高价收电脑", KindWanted}, {"电脑回收", KindRecycle}, {"相机出租", KindRental}, {"相机租赁", KindRental}, {"收音机", ""}, {"收纳柜", ""}, {"出售手机", ""}, {"高价收回收出租", KindWanted},
	} {
		if got := KeywordKind(tt.title); got != tt.kind {
			t.Errorf("%s: %q != %q", tt.title, got, tt.kind)
		}
	}
}

func TestFirstLayerOrderAndUnknowns(t *testing.T) {
	in := FirstLayerInput{Mode: ModeKeyword, Title: "有锁", Price: ptr(int64(50)), MinPrice: ptr(int64(100)), FreeShippingOnly: true, PublishedAt: ptr(int64(0)), PublishDays: 1, NowMS: 2 * 86400000, Province: "广东", City: "全广东", Area: "浙江", Matches: []string{"q1"}, Excludes: []string{"有锁"}}
	wantCodes := []string{"analysis.filter_price", "analysis.filter_shipping", "analysis.filter_published", "analysis.filter_region", "analysis.keyword_excluded", "analysis.keyword_miss"}
	for i, code := range wantCodes {
		got := FirstLayer(in)
		if !got.Rejected || got.Reason.Code != code {
			t.Fatalf("%d: %+v", i, got)
		}
		switch i {
		case 0:
			in.Price = nil
		case 1:
			in.FreeShipping = true
		case 2:
			in.PublishedAt = nil
		case 3:
			in.Area = ""
		case 4:
			in.Excludes = nil
		}
	}
	in.Matches = nil
	if got := FirstLayer(in); got.Rejected {
		t.Fatal(got)
	}
	in = FirstLayerInput{NowMS: 86400000, PublishDays: 1, PublishedAt: ptr(int64(0))}
	if FirstLayer(in).Rejected {
		t.Fatal("time boundary must pass")
	}
	in.NowMS++
	if !FirstLayer(in).Rejected {
		t.Fatal("older must reject")
	}
	in = FirstLayerInput{Price: ptr(int64(100)), MinPrice: ptr(int64(100)), MaxPrice: ptr(int64(100))}
	if FirstLayer(in).Rejected {
		t.Fatal("price endpoints")
	}
	in = FirstLayerInput{NowMS: math.MinInt64, PublishDays: math.MaxInt64, PublishedAt: ptr(int64(0))}
	if FirstLayer(in).Rejected {
		t.Fatal("overflow")
	}
}

func TestMoneyArgumentKeepsIntegerPrecision(t *testing.T) {
	if got := moneyArg(ptr(int64(math.MaxInt64))); got != "92233720368547758.07" {
		t.Fatal(got)
	}
}

func TestFirstLayerRegions(t *testing.T) {
	cities := []RegionCity{{"吉林省", "朝阳市"}, {"广东省", "深圳市"}, {"浙江省", "宁波市"}, {"辽宁省", "沈阳市"}, {"河北省", "石家庄市"}}
	for _, tt := range []struct {
		province, city, area string
		pass                 bool
	}{
		{"全国", "全国", "海外", true}, {"海外", "日本", "美国", true}, {"广东省", "全广东", "深圳市", true}, {"广东省", "广州市", "深圳", true}, {"全国", "江浙沪", "宁波市", true}, {"全国", "珠三角", "深圳", true}, {"全国", "京津冀", "石家庄", true}, {"全国", "东三省", "沈阳", true}, {"全国", "东三省", "广东", false}, {"广东省", "全广东", "浙江宁波", false}, {"广东省", "全广东", "", true}, {"浙江省", "杭州市", "宁波", true}, {"吉林省", "全吉林", "北京朝阳区", false}, {"吉林省", "全吉林", "辽宁朝阳市", false},
	} {
		got := FirstLayer(FirstLayerInput{Province: tt.province, City: tt.city, Area: tt.area, Cities: cities})
		if got.Rejected == tt.pass {
			t.Errorf("%+v: %+v", tt, got)
		}
	}
}

func TestSpecsAndTitleGates(t *testing.T) {
	p := Profile{Specs: []ProfileSpec{{Name: "型号", Options: []string{"A", "B"}, Values: []string{"A"}}, {Name: "容量", Options: []string{"256G"}}}}
	resolved := ResolveSpecs(p, []SpecValue{{"容量", ptr("２５６Ｇ")}, {"型号", ptr("B")}}, ptr(false), SpecTitle)
	if resolved.ConfigKey == nil || *resolved.ConfigKey != "B / 256G" || resolved.Excluded {
		t.Fatal(resolved)
	}
	item := TitleItem{Kind: KindSale, Specs: resolved.Specs, Verdict: "pass", Reason: "不是国行"}
	got := JudgeTitle(p, item)
	if !got.Result.Rejected || got.Specs.Excluded {
		t.Fatal("spec mismatch must precede AI verdict", got)
	}
	item.Verdict = "unmatched"
	got = JudgeTitle(p, item)
	if !got.Specs.Excluded || got.Result.Reason.Code != "analysis.spec_unmatched" {
		t.Fatal("combined title gate", got)
	}
	item.Specs[0].Value = ptr("A")
	got = JudgeTitle(p, item)
	if !got.Specs.Excluded {
		t.Fatal(got)
	}
	item.Kind = KindAccessory
	got = JudgeTitle(p, item)
	if got.Result.RejectKind != RejectJunk {
		t.Fatal(got)
	}
	p.AllowedKinds = []Kind{KindAccessory}
	item.Verdict = "pass"
	item.Specs[0].Value = ptr("C")
	got = JudgeTitle(p, item)
	if got.Result.Rejected || got.Specs.ConfigKey != nil {
		t.Fatal(got)
	}
	if got := ResolveSpecs(Profile{}, nil, nil, SpecDetail); got.ConfigKey == nil || *got.ConfigKey != "*" {
		t.Fatal(got)
	}
	got.Specs.Specs[0].Name = "changed"
	if p.Specs[0].Name != "型号" || item.Specs[0].Name != "型号" {
		t.Fatal("input mutated")
	}
}

func TestTitleUnknownAndDuplicateSpecsAreWarnings(t *testing.T) {
	p := Profile{Specs: []ProfileSpec{{Name: "容量", Options: []string{"256G"}}}}
	item := TitleItem{Kind: KindSale, Verdict: "pass", Specs: []SpecValue{{"外来", ptr("x")}, {"容量", ptr("256G")}, {"容量", ptr("128G")}}}
	got := JudgeTitle(p, item)
	if got.Result.Rejected || len(got.Warnings) != 2 || got.Specs.Specs[0].Value == nil || *got.Specs.Specs[0].Value != "256G" {
		t.Fatal(got)
	}
}
