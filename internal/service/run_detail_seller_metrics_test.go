package service

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/prompts"
	"github.com/Seaony/Kakatta/internal/xianyu"
)

func TestDetailSellerMetricsReachModelWithoutSellerPage(t *testing.T) {
	s, run, _ := detailTestRun(t, 1)
	detail, err := xianyu.ParseDetail([]byte(`{"data":{"sellerDO":{"sellerId":"local-seller","itemCount":0,"hasSoldNumInteger":8,"avgReply30dLong":1.25,"newGoodRatioRate":"未知口径98%","lastVisitTime":"刚刚来过","idleFishCreditTag":{"trackParams":{"sellerLevel":"L4"}},"zhimaAuth":false}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.detailSave(t.Context(), run.P[0], detail); err != nil {
		t.Fatal(err)
	}
	input, current, err := s.detailInput(t.Context(), run.P[0])
	if err != nil || !current || !reflect.DeepEqual(input.Item.SellerDetail, detail.SellerMetrics) {
		t.Fatalf("正式详情没有保存指标：%+v %v", input.Item.SellerDetail, err)
	}
	snapshot, err := prompts.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, cached := range []*SellerProfile{nil, {ItemCount: listingTestPtr(int64(99)), OnSaleCount: listingTestPtr(int64(7)), SellerCredit: listingTestPtr("主页信用文字")}} {
		source := detailProductSource(input.Item, cached)
		if source.Seller == nil || !reflect.DeepEqual(source.Seller.DetailMetrics, detail.SellerMetrics) {
			t.Fatalf("没有主页时丢掉详情资料：%+v", source.Seller)
		}
		if cached != nil && source.Seller.ItemCount != cached.ItemCount {
			t.Fatal("详情计数覆盖了主页采样")
		}
		request, originals, err := prompts.BuildAnalysis(snapshot, nil, analysis.Profile{}, source, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(source.Seller.DetailMetrics, detail.SellerMetrics) || originals.SellerIntroduction != "" {
			t.Fatal("原指标被改写或当作文字证据")
		}
		for _, value := range []string{`"detail_metrics":{`, `"item_count":0`, `"last_visit_time":"刚刚来过"`, `"zhima_auth":false`} {
			if !strings.Contains(request.User, value) {
				t.Fatalf("正式模型请求缺指标 %s：%s", value, request.User)
			}
		}
	}
}
