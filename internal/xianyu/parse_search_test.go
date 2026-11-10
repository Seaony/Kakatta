package xianyu

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestParseSearch(t *testing.T) {
	items, err := ParseSearch(readFixture(t, "search.json"))
	if err != nil {
		t.Fatal(err)
	}
	published := time.UnixMilli(1710000000000).UTC()
	want := []SearchItem{
		{
			ID: "880000000001", Title: "富士 X100V 银色 箱说全", PriceText: "¥1,299",
			PriceCents: 129900, PriceKnown: true, OriPriceCents: 1699900, OriPriceKnown: true, OriPriceText: "¥16999",
			Image: "https://img.alicdn.com/bao/uploaded/x100v.jpg", Area: "上海", SellerNick: "卖家一号",
			WebURL:      "https://www.goofish.com/item?id=880000000001",
			AppURL:      "https://pages.goofish.com/sharexy?loadingVisible=false&bft=item&bfs=idlepc.item&spm=a21ybx.item.0.0&bfp=%7B%22id%22%3A880000000001%7D",
			PublishedAt: published, PublishedKnown: true, WantNum: 7, WantKnown: true,
			FreeShip: true, Inspected: true,
		},
		{
			ID: "880000000002", Title: "数字 ID，价格带万", PriceText: "¥1.2万",
			PriceCents: 1200000, PriceKnown: true,
			Image:       "https://img.alicdn.com/b.jpg",
			WebURL:      "https://www.goofish.com/item?id=880000000002",
			AppURL:      "https://pages.goofish.com/sharexy?loadingVisible=false&bft=item&bfs=idlepc.item&spm=a21ybx.item.0.0&bfp=%7B%22id%22%3A880000000002%7D",
			PublishedAt: published, PublishedKnown: true, WantNum: 3, WantKnown: true,
		},
		{
			ID: "880000000003", Title: "当前价", PriceText: "¥88",
			PriceCents: 8800, PriceKnown: true,
			WebURL: "https://www.goofish.com/item?id=880000000003",
			AppURL: "https://pages.goofish.com/sharexy?loadingVisible=false&bft=item&bfs=idlepc.item&spm=a21ybx.item.0.0&bfp=%7B%22id%22%3A880000000003%7D",
		},
		{
			ID: "880000000004", Title: "面议", PriceText: "面议", OriPriceText: "暂无",
			AppURL: "https://pages.goofish.com/sharexy?loadingVisible=false&bft=item&bfs=idlepc.item&spm=a21ybx.item.0.0&bfp=%7B%22id%22%3A880000000004%7D",
		},
		{
			ID: "880000000005", Title: "价格不是数组",
			AppURL: "https://pages.goofish.com/sharexy?loadingVisible=false&bft=item&bfs=idlepc.item&spm=a21ybx.item.0.0&bfp=%7B%22id%22%3A880000000005%7D",
		},
	}
	if len(items) != len(want) {
		t.Fatalf("得到 %d 项，期望 %d 项：%+v", len(items), len(want), items)
	}
	for i := range want {
		if !reflect.DeepEqual(items[i], want[i]) {
			t.Errorf("第 %d 项\n得到 %+v\n期望 %+v", i, items[i], want[i])
		}
	}
}

// 上游手写样例（复制自 ai-goofish-monitor tests/fixtures/search_results.json，MIT）。
func TestParseSearchUpstreamFixture(t *testing.T) {
	items, err := ParseSearch(readFixture(t, "upstream_search_results.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("得到 %d 项，期望 1 项", len(items))
	}
	it := items[0]
	if it.ID != "123456" || it.Title != "Sony A7M4 Body" || it.PriceText != "¥13999" || it.PriceCents != 1399900 ||
		it.OriPriceCents != 1699900 || it.Area != "Shanghai" || it.SellerNick != "seller_01" ||
		it.Image != "https://img.example.com/a7m4.jpg" || it.WantNum != 12 || !it.FreeShip || !it.Inspected {
		t.Errorf("解析结果不对：%+v", it)
	}
	if it.WebURL != "https://www.goofish.com/item?id=123456" {
		t.Errorf("WebURL = %q", it.WebURL)
	}
	if it.AppURL != "https://pages.goofish.com/sharexy?loadingVisible=false&bft=item&bfs=idlepc.item&spm=a21ybx.item.0.0&bfp=%7B%22id%22%3A123456%7D" {
		t.Errorf("AppURL = %q", it.AppURL)
	}
	if !it.PublishedKnown || !it.PublishedAt.Equal(time.UnixMilli(1710000000000)) {
		t.Errorf("发布时间 = %v, %v", it.PublishedAt, it.PublishedKnown)
	}
}

func TestParseSearchJSONP(t *testing.T) {
	items, err := ParseSearch(readFixture(t, "search_jsonp.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "990001" || items[0].PriceCents != 5000 {
		t.Errorf("JSONP 解析结果不对：%+v", items)
	}
}

func TestParseSearchEdges(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		count   int
		wantErr bool
	}{
		{"没有 resultList", `{"ret":["SUCCESS"],"data":{}}`, 0, true},
		{"resultList 不是数组", `{"ret":["SUCCESS"],"data":{"resultList":{}}}`, 0, true},
		{"根是数组", `[]`, 0, true},
		{"正常空列表", `{"ret":["SUCCESS"],"data":{"resultList":[]}}`, 0, false},
		{"不是 JSON", `not json`, 0, true},
		{"非数字 ID 用网页链接当 App 链接", `{"data":{"resultList":[{"data":{"item":{"main":{"exContent":{"itemId":"abc"},"targetUrl":"fleamarket://item?id=abc"}}}}]}}`, 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			items, err := ParseSearch([]byte(c.body))
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v", err)
			}
			if len(items) != c.count {
				t.Fatalf("得到 %d 项，期望 %d 项", len(items), c.count)
			}
			if c.count == 1 && items[0].AppURL != items[0].WebURL {
				t.Errorf("AppURL = %q，期望和 WebURL 相同", items[0].AppURL)
			}
		})
	}
}

func TestParseSearchReportsChangedStructure(t *testing.T) {
	for _, body := range []string{`{}`, `[]`, `{"data":{"resultList":null}}`, `{"data":{"resultList":{}}}`} {
		if _, err := ParseSearch([]byte(body)); !errors.Is(err, ErrSearchStructure) {
			t.Errorf("%s 应返回商品列表结构错误，实际：%v", body, err)
		}
	}
}
