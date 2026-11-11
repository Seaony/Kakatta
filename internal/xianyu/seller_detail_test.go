package xianyu

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestParseDetailSellerMetricsPreserveRawValues(t *testing.T) {
	d, err := ParseDetail([]byte(`{"data":{"sellerDO":{"itemCount":0,"hasSoldNumInteger":12,"avgReply30dLong":1.5,"newGoodRatioRate":"99.5%","lastVisitTime":"刚刚来过","idleFishCreditTag":{"trackParams":{"sellerLevel":"4","pageUserId":"不进入指标"}},"zhimaAuth":false}}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(d.SellerMetrics)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"item_count":0,"has_sold_num_integer":12,"avg_reply_30d_long":1.5,"new_good_ratio_rate":"99.5%","last_visit_time":"刚刚来过","idle_fish_credit_level":"4","zhima_auth":false}`
	if string(got) != want {
		t.Fatalf("卖家指标原值丢失或混入账号：%s", got)
	}
	if d.Zhima != "" {
		t.Fatal("未确认的闲鱼等级不能冒充芝麻信用")
	}
}

func TestParseDetailSellerMetricsDoNotInventValues(t *testing.T) {
	for _, raw := range []string{`{}`, `{"itemCount":null,"hasSoldNumInteger":false,"avgReply30dLong":{},"newGoodRatioRate":8,"lastVisitTime":[],"zhimaAuth":"true"}`, `{"itemCount":-1,"hasSoldNumInteger":0.5,"avgReply30dLong":-2,"newGoodRatioRate":" ","lastVisitTime":"","idleFishCreditTag":{"trackParams":{"sellerLevel":{}}}}`} {
		d, err := ParseDetail([]byte(`{"data":{"sellerDO":` + raw + `}}`))
		if err != nil || d.SellerMetrics != nil {
			t.Fatalf("未知值被编成指标：%+v %v", d.SellerMetrics, err)
		}
	}
	d, err := ParseDetail([]byte(`{"data":{"sellerDO":{"zhimaLevelInfo":{"levelName":"芝麻信用极好"},"itemCount":3}}}`))
	if err != nil || d.Zhima != "芝麻信用极好" || d.SellerMetrics == nil || !reflect.DeepEqual(*d.SellerMetrics.ItemCount, int64(3)) {
		t.Fatalf("芝麻字段与指标未独立保留：%+v %v", d, err)
	}
}

func TestDetailSellerMetricsUseRulePaths(t *testing.T) {
	old := rules.Fields.Detail.SellerMetrics
	t.Cleanup(func() { rules.Fields.Detail.SellerMetrics = old })
	rules.Fields.Detail.SellerMetrics = &sellerMetricPaths{
		ItemCount: []string{"metrics", "items"}, HasSoldNumInteger: []string{"metrics", "sold"},
		AvgReply30dLong: []string{"metrics", "reply"}, NewGoodRatioRate: []string{"metrics", "ratio"},
		LastVisitTime: []string{"metrics", "visit"}, IdleFishCreditLevel: []string{"metrics", "level"}, ZhimaAuth: []string{"metrics", "auth"},
	}
	d, err := ParseDetail([]byte(`{"data":{"sellerDO":{"itemCount":999,"metrics":{"items":2,"sold":3,"reply":0,"ratio":"原比例","visit":"原访问","level":"原等级","auth":false}}}}`))
	if err != nil || d.SellerMetrics == nil || *d.SellerMetrics.ItemCount != 2 || *d.SellerMetrics.HasSoldNumInteger != 3 || *d.SellerMetrics.AvgReply30dLong != 0 || *d.SellerMetrics.NewGoodRatioRate != "原比例" || *d.SellerMetrics.LastVisitTime != "原访问" || *d.SellerMetrics.IdleFishCreditLevel != "原等级" || *d.SellerMetrics.ZhimaAuth {
		t.Fatalf("详情指标绕过规则数据：%+v %v", d.SellerMetrics, err)
	}
}

func TestOldV4RulesKeepConfirmedSellerMetricPaths(t *testing.T) {
	var source map[string]any
	if err := json.Unmarshal(bundledRules, &source); err != nil {
		t.Fatal(err)
	}
	detail := source["fields"].(map[string]any)["detail"].(map[string]any)
	delete(detail, "seller_metrics")
	body, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeRules(body)
	if err != nil || !reflect.DeepEqual(got.Fields.Detail.SellerMetrics, defaultSellerMetricPaths()) {
		t.Fatalf("旧规则不能读取已确认指标：%+v %v", got.Fields.Detail.SellerMetrics, err)
	}
	for _, value := range []any{nil, map[string]any{"item_count": []any{}}, "不是路径对象"} {
		detail["seller_metrics"] = value
		body, _ = json.Marshal(source)
		if _, err := decodeRules(body); err == nil {
			t.Fatalf("显式坏指标规则被默认值吞掉：%s", body)
		}
	}
}
