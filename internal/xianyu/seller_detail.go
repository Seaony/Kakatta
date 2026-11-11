package xianyu

import (
	"math"
	"strconv"
	"strings"

	"github.com/Seaony/Kakatta/internal/limits"
)

// SellerDetailMetrics 保留详情里卖家指标的原值，不把尚未确认的单位换算成评价结论。
type SellerDetailMetrics struct {
	ItemCount           *int64   `json:"item_count,omitempty"`
	HasSoldNumInteger   *int64   `json:"has_sold_num_integer,omitempty"`
	AvgReply30dLong     *float64 `json:"avg_reply_30d_long,omitempty"`
	NewGoodRatioRate    *string  `json:"new_good_ratio_rate,omitempty"`
	LastVisitTime       *string  `json:"last_visit_time,omitempty"`
	IdleFishCreditLevel *string  `json:"idle_fish_credit_level,omitempty"`
	ZhimaAuth           *bool    `json:"zhima_auth,omitempty"`
}

type sellerMetricPaths struct {
	ItemCount           []string `json:"item_count"`
	HasSoldNumInteger   []string `json:"has_sold_num_integer"`
	AvgReply30dLong     []string `json:"avg_reply_30d_long"`
	NewGoodRatioRate    []string `json:"new_good_ratio_rate"`
	LastVisitTime       []string `json:"last_visit_time"`
	IdleFishCreditLevel []string `json:"idle_fish_credit_level"`
	ZhimaAuth           []string `json:"zhima_auth"`
}

func defaultSellerMetricPaths() *sellerMetricPaths {
	return &sellerMetricPaths{
		ItemCount: []string{"itemCount"}, HasSoldNumInteger: []string{"hasSoldNumInteger"},
		AvgReply30dLong: []string{"avgReply30dLong"}, NewGoodRatioRate: []string{"newGoodRatioRate"},
		LastVisitTime: []string{"lastVisitTime"}, IdleFishCreditLevel: []string{"idleFishCreditTag", "trackParams", "sellerLevel"},
		ZhimaAuth: []string{"zhimaAuth"},
	}
}

func parseSellerDetailMetrics(seller map[string]any) *SellerDetailMetrics {
	var out SellerDetailMetrics
	paths := rules.Fields.Detail.SellerMetrics
	for _, field := range []struct {
		path []string
		dst  **int64
	}{{paths.ItemCount, &out.ItemCount}, {paths.HasSoldNumInteger, &out.HasSoldNumInteger}} {
		if n, ok := asInt(pathGet(seller, field.path...)); ok && n >= 0 && n <= limits.MaxSafeInteger {
			*field.dst = &n
		}
	}
	if n, err := strconv.ParseFloat(asScalarText(pathGet(seller, paths.AvgReply30dLong...)), 64); err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 {
		out.AvgReply30dLong = &n
	}
	for _, field := range []struct {
		path []string
		dst  **string
	}{{paths.NewGoodRatioRate, &out.NewGoodRatioRate}, {paths.LastVisitTime, &out.LastVisitTime}} {
		if text, ok := pathGet(seller, field.path...).(string); ok && strings.TrimSpace(text) != "" {
			*field.dst = &text
		}
	}
	if text := strings.TrimSpace(asScalarText(pathGet(seller, paths.IdleFishCreditLevel...))); text != "" {
		out.IdleFishCreditLevel = &text
	}
	if value, ok := pathGet(seller, paths.ZhimaAuth...).(bool); ok {
		out.ZhimaAuth = &value
	}
	if out == (SellerDetailMetrics{}) {
		return nil
	}
	return &out
}
