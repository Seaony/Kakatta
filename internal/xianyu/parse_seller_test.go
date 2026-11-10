package xianyu

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseSellerHead(t *testing.T) {
	h, err := ParseSellerHead(readFixture(t, "seller_head.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := SellerHead{
		Nick: "卖家一号", Avatar: "https://img.alicdn.com/avatar.jpg",
		ItemCount: 36, ItemCountKnown: true,
		SellerCredit: "卖家信用极好", SellerCreditLevel: "4",
		BuyerCredit: "买家信用良好", BuyerCreditLevel: "2",
	}
	if !reflect.DeepEqual(h, want) {
		t.Errorf("\n得到 %+v\n期望 %+v", h, want)
	}
}

// 上游手写样例（复制自 ai-goofish-monitor tests/fixtures/user_head.json，MIT）。
func TestParseSellerHeadUpstreamFixture(t *testing.T) {
	h, err := ParseSellerHead(readFixture(t, "upstream_user_head.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := SellerHead{
		Nick: "seller_01", Avatar: "https://img.example.com/avatar.jpg", Intro: "Trusted seller",
		ItemCount: 12, ItemCountKnown: true, RateCount: 88, RateCountKnown: true,
		SellerCredit: "S3", SellerCreditLevel: "3",
		BuyerCredit: "B2", BuyerCreditLevel: "2",
	}
	if !reflect.DeepEqual(h, want) {
		t.Errorf("\n得到 %+v\n期望 %+v", h, want)
	}
}

func TestParseSellerItems(t *testing.T) {
	got, err := ParseSellerItems(readFixture(t, "seller_items.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := SellerItems{
		Items: []SellerItem{
			{ID: "10001", Title: "镜头 24-70", PriceText: "3500", PriceCents: 350000, PriceKnown: true, Image: "https://img.alicdn.com/lens.jpg", Status: ItemOnSale},
			{ID: "10002", Title: "手柄", PriceText: "599.9", PriceCents: 59990, PriceKnown: true, Image: "https://img.alicdn.com/grip.jpg", Status: ItemSold},
			{ID: "10003", Title: "状态未知", PriceText: "面议", Status: ItemStatusUnknown},
			{ID: "10004", Title: "没有状态", Status: ItemStatusUnknown},
		},
		ListKnown: true, OnSale: 1, Sold: 1,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("\n得到 %+v\n期望 %+v", got, want)
	}
}

// 上游手写样例的根是数组（复制自 ai-goofish-monitor tests/fixtures/user_items.json，MIT）。
func TestParseSellerItemsUpstreamFixture(t *testing.T) {
	got, err := ParseSellerItems(readFixture(t, "upstream_user_items.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 || got.OnSale != 1 || got.Sold != 1 ||
		got.Items[0].ID != "10001" || got.Items[0].PriceCents != 350000 || got.Items[1].Status != ItemSold {
		t.Errorf("解析结果不对：%+v", got)
	}
}

// 只保存前 20 条，在售和已售只统计这 20 条。
func TestParseSellerItemsKeepsFirst20(t *testing.T) {
	var cards []string
	for i := 1; i <= 25; i++ {
		cards = append(cards, fmt.Sprintf(`{"cardData":{"id":"%d","itemStatus":%d}}`, i, i%2))
	}
	body := `{"ret":["SUCCESS"],"data":{"cardList":[` + strings.Join(cards, ",") + `]}}`
	got, err := ParseSellerItems([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != SellerItemsKeep || got.Items[19].ID != "20" {
		t.Fatalf("得到 %d 条，最后一条 %+v", len(got.Items), got.Items[len(got.Items)-1])
	}
	if got.OnSale != 10 || got.Sold != 10 {
		t.Errorf("在售 %d、已售 %d，期望各 10", got.OnSale, got.Sold)
	}
}

func TestParseSellerRatings(t *testing.T) {
	got, err := ParseSellerRatings(readFixture(t, "seller_ratings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, r := range got.Recent {
		order = append(order, r.ID)
	}
	// r3 是毫秒时间戳 2024-06-01，r4 的时间认不出，排最后。
	if want := []string{"r3", "r2", "5", "r1", "r6", "r4"}; !reflect.DeepEqual(order, want) {
		t.Errorf("顺序 = %v，期望 %v", order, want)
	}
	if got.AsSeller != (RatingStats{Good: 1, Total: 3}) {
		t.Errorf("作为卖家 = %+v，期望 1/3", got.AsSeller)
	}
	if got.AsBuyer != (RatingStats{Good: 1, Total: 2}) {
		t.Errorf("作为买家 = %+v，期望 1/2", got.AsBuyer)
	}

	r2 := got.Recent[1]
	wantR2 := Rating{
		ID: "r2", Feedback: "付款爽快", Summary: "付款爽快", Kind: RatingGood,
		RoleText: "来自买家", Role: RoleBuyer, Rater: "卖家乙",
		CreatedText: "2024-03-01 12:30:00", CreatedAt: time.Date(2024, 3, 1, 4, 30, 0, 0, time.UTC), CreatedKnown: true,
		Images: []string{"https://img.alicdn.com/rate.jpg"},
	}
	if !reflect.DeepEqual(r2, wantR2) {
		t.Errorf("\n得到 %+v\n期望 %+v", r2, wantR2)
	}
	kinds := map[string]RatingKind{"r1": RatingGood, "r3": RatingNeutral, "r4": RatingBad, "r6": RatingUnknown}
	for _, r := range got.Recent {
		if k, ok := kinds[r.ID]; ok && r.Kind != k {
			t.Errorf("%s 的好中差 = %v，期望 %v", r.ID, r.Kind, k)
		}
		if r.ID == "5" && r.Role != RoleUnknown {
			t.Errorf("没有身份标签时应为未知身份")
		}
	}
}

// 上游手写样例的根是数组（复制自 ai-goofish-monitor tests/fixtures/ratings.json，MIT）。
// 上游对它的统计结果：作为卖家 1/2，作为买家 1/1。
func TestParseSellerRatingsUpstreamFixture(t *testing.T) {
	got, err := ParseSellerRatings(readFixture(t, "upstream_ratings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got.AsSeller != (RatingStats{Good: 1, Total: 2}) || got.AsBuyer != (RatingStats{Good: 1, Total: 1}) {
		t.Errorf("统计 = %+v / %+v", got.AsSeller, got.AsBuyer)
	}
	if len(got.Recent) != 3 || got.Recent[0].ID != "r3" || got.Recent[2].ID != "r1" {
		t.Errorf("顺序不对：%+v", got.Recent)
	}
	if rate, ok := got.AsSeller.GoodRate(); !ok || rate != 0.5 {
		t.Errorf("卖家好评率 = %v, %v", rate, ok)
	}
}

// 只保存最新 10 条，但好评统计用整页。
func TestParseSellerRatingsKeepsNewest10(t *testing.T) {
	var cards []string
	for i := 1; i <= 12; i++ {
		cards = append(cards, fmt.Sprintf(`{"cardData":{"rateId":"r%d","rate":1,"rateTagList":[{"text":"卖家"}],"gmtCreate":"2024-01-%02d"}}`, i, i))
	}
	body := `{"ret":["SUCCESS"],"data":{"cardList":[` + strings.Join(cards, ",") + `]}}`
	got, err := ParseSellerRatings([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Recent) != SellerRatingsKeep || got.Recent[0].ID != "r12" || got.Recent[9].ID != "r3" {
		t.Errorf("保留结果不对：首条 %s，共 %d 条", got.Recent[0].ID, len(got.Recent))
	}
	if got.AsSeller != (RatingStats{Good: 12, Total: 12}) {
		t.Errorf("统计 = %+v，期望 12/12", got.AsSeller)
	}
}

func TestRatingSummaryRunes(t *testing.T) {
	long := strings.Repeat("好", 70)
	got, err := ParseSellerRatings([]byte(`{"data":{"cardList":[{"cardData":{"rateId":"x","feedback":"` + long + `"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	r := got.Recent[0]
	if r.Feedback != long || r.Summary != strings.Repeat("好", 60) {
		t.Errorf("摘要长度 = %d 字", len([]rune(r.Summary)))
	}
}

func TestGoodRateEmpty(t *testing.T) {
	if _, ok := (RatingStats{}).GoodRate(); ok {
		t.Error("总数为 0 时不应有好评率")
	}
}

func TestSellerParsersRejectBadJSON(t *testing.T) {
	bad := []byte(`{"data":`)
	if _, err := ParseSellerHead(bad); err == nil {
		t.Error("ParseSellerHead 应当报错")
	}
	if _, err := ParseSellerItems(bad); err == nil {
		t.Error("ParseSellerItems 应当报错")
	}
	if _, err := ParseSellerRatings(bad); err == nil {
		t.Error("ParseSellerRatings 应当报错")
	}
}

func TestParseSellerHeadCountsKnown(t *testing.T) {
	for _, tc := range []struct {
		value string
		known bool
		count int
	}{
		{`0`, true, 0},
		{`"0"`, true, 0},
		{`"12"`, true, 12},
		{`null`, false, 0},
		{`"未知"`, false, 0},
		{`{}`, false, 0},
		{`1.5`, false, 0},
	} {
		t.Run(tc.value, func(t *testing.T) {
			body := `{"data":{"module":{"tabs":{"item":{"number":` + tc.value + `},"rate":{"number":` + tc.value + `}}}}}`
			h, err := ParseSellerHead([]byte(body))
			if err != nil || h.ItemCount != tc.count || h.RateCount != tc.count || h.ItemCountKnown != tc.known || h.RateCountKnown != tc.known {
				t.Fatalf("计数是否已知不正确：%+v %v", h, err)
			}
		})
	}
	h, err := ParseSellerHead([]byte(`{"data":{"module":{"tabs":{"item":{},"rate":{}}}}}`))
	if err != nil || h.ItemCountKnown || h.RateCountKnown {
		t.Fatalf("缺失计数不能当作已知 0：%+v %v", h, err)
	}
}

func TestSellerListsKnown(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		known      bool
	}{
		{"missing", `{}`, false},
		{"null", `{"data":{"cardList":null}}`, false},
		{"object", `{"data":{"cardList":{}}}`, false},
		{"string", `{"data":{"cardList":""}}`, false},
		{"empty", `{"data":{"cardList":[]}}`, true},
		{"root array", `[]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items, err := ParseSellerItems([]byte(tc.body))
			if err != nil || items.ListKnown != tc.known || len(items.Items) != 0 || items.OnSale != 0 || items.Sold != 0 {
				t.Fatalf("商品列表是否已知不正确：%+v %v", items, err)
			}
			ratings, err := ParseSellerRatings([]byte(tc.body))
			if err != nil || ratings.ListKnown != tc.known || len(ratings.Recent) != 0 || ratings.AsSeller != (RatingStats{}) || ratings.AsBuyer != (RatingStats{}) {
				t.Fatalf("评价列表是否已知不正确：%+v %v", ratings, err)
			}
		})
	}
}

func TestParseSellerHeadUsesRulePaths(t *testing.T) {
	previous := rules
	t.Cleanup(func() { rules = previous })
	f := &rules.Fields.Seller.Head
	f.Module, f.Base = []string{"payload", "header"}, []string{"person"}
	f.Nick, f.Avatar, f.Intro = []string{"name"}, []string{"picture", "src"}, []string{"about"}
	f.ItemCount, f.RateCount = []string{"counts", "items"}, []string{"counts", "ratings"}
	f.Tags = []string{"credits"}
	f.TagText, f.TagLevel, f.TagRole = []string{"label"}, []string{"rank"}, []string{"role"}
	body := []byte(`{"payload":{"header":{"person":{"name":"规则卖家","picture":{"src":"//example.test/avatar.jpg"},"about":"简介","credits":[{"label":"卖家原文","rank":4,"role":"seller"},{"label":"买家原文","rank":"2","role":"buyer"}]},"counts":{"items":0,"ratings":"12"}}}}`)
	got, err := ParseSellerHead(body)
	want := SellerHead{
		Nick: "规则卖家", Avatar: "https://example.test/avatar.jpg", Intro: "简介",
		ItemCountKnown: true, RateCount: 12, RateCountKnown: true,
		SellerCredit: "卖家原文", SellerCreditLevel: "4", BuyerCredit: "买家原文", BuyerCreditLevel: "2",
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("未使用头部字段规则：%+v %v", got, err)
	}
}

func TestParseSellerItemsUsesRulePaths(t *testing.T) {
	previous := rules
	t.Cleanup(func() { rules = previous })
	f := &rules.Fields.Seller
	f.Cards, f.CardData = []string{"payload", "rows"}, []string{"entry"}
	f.Items.ID, f.Items.Title = []string{"identifier"}, []string{"label"}
	f.Items.Price, f.Items.Image, f.Items.Status = []string{"amount", "value"}, []string{"picture", "src"}, []string{"state"}
	body := []byte(`{"payload":{"rows":[{"entry":{"identifier":9007199254740993,"label":"规则商品","amount":{"value":"12.345"},"picture":{"src":"//example.test/item.jpg"},"state":"0"}},{"entry":{"identifier":"sold","state":1}}]}}`)
	got, err := ParseSellerItems(body)
	want := SellerItems{
		ListKnown: true, OnSale: 1, Sold: 1,
		Items: []SellerItem{
			{ID: "9007199254740993", Title: "规则商品", PriceText: "12.345", PriceCents: 1235, PriceKnown: true, Image: "https://example.test/item.jpg", Status: ItemOnSale},
			{ID: "sold", Status: ItemSold},
		},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("未使用商品字段规则：%+v %v", got, err)
	}
}

func TestParseSellerRatingsUsesRulePaths(t *testing.T) {
	previous := rules
	t.Cleanup(func() { rules = previous })
	f := &rules.Fields.Seller
	f.Cards, f.CardData = []string{"payload", "rows"}, []string{"entry"}
	f.Ratings.ID, f.Ratings.Feedback, f.Ratings.Rater = []string{"identifier"}, []string{"body"}, []string{"author"}
	f.Ratings.Created, f.Ratings.Kind = []string{"written"}, []string{"score"}
	f.Ratings.Tags, f.Ratings.TagText, f.Ratings.Images = []string{"identities"}, []string{"label"}, []string{"pictures"}
	body := []byte(`{"payload":{"rows":[{"entry":{"identifier":9007199254740993,"body":"原文不改","author":"评价人","written":"1717200000000","score":"1","identities":[{"label":"作为卖家"}],"pictures":["//example.test/rating.jpg"]}}]}}`)
	got, err := ParseSellerRatings(body)
	want := SellerRatings{
		ListKnown: true, AsSeller: RatingStats{Good: 1, Total: 1},
		Recent: []Rating{{
			ID: "9007199254740993", Feedback: "原文不改", Summary: "原文不改", Rater: "评价人", Kind: RatingGood,
			RoleText: "作为卖家", Role: RoleSeller, CreatedText: "1717200000000", CreatedKnown: true,
			CreatedAt: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC), Images: []string{"https://example.test/rating.jpg"},
		}},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("未使用评价字段规则：%+v %v", got, err)
	}
}
