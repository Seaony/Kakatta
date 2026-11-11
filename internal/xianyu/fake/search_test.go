package fake_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/xianyu"
	"github.com/Seaony/Kakatta/internal/xianyu/fake"
)

func TestCustomSearchFixtureKeepsPageOrderCopiesInputsAndSupportsEmptyPage(t *testing.T) {
	f := fake.Start()
	t.Cleanup(f.Close)
	first := []fake.SearchItem{{ID: "201", Title: "自用电脑 16G", Price: "6000"}, {ID: "202", Title: "自用电脑 32G", Price: "6500"}}
	second := []fake.SearchItem{{ID: "202", Title: "自用电脑 32G", Price: "6500"}, {ID: "203", Title: "自用电脑 16G", Price: "4800"}}
	f.SetSearchItems(first, second)
	first[0].Title = "不能修改已设置的页面"
	second[1].Price = "1"
	read := func(query string) []xianyu.SearchItem {
		t.Helper()
		response, err := http.Post(f.Home()+"h5/mtop.taobao.idlemtopsearch.pc.search/1.0/?"+query, "application/x-www-form-urlencoded", strings.NewReader("q=电脑"))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		items, err := xianyu.ParseSearch(body)
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	gotFirst := read("first=1")
	gotSecond := read("page=2")
	if len(gotFirst) != 2 || len(gotSecond) != 2 || gotFirst[0].ID != "201" || gotFirst[0].Title != "自用电脑 16G" || gotFirst[0].PriceCents != 600000 || gotFirst[1].ID != "202" || gotSecond[0].ID != "202" || gotSecond[1].ID != "203" || gotSecond[1].PriceCents != 480000 {
		t.Fatalf("两页覆盖必须保留重复项和原始配置：%+v；%+v", gotFirst, gotSecond)
	}
	f.SetSearchItems(nil, nil)
	if got := read("first=1"); len(got) != 0 {
		t.Fatalf("空配置必须返回有列表字段的空页：%+v", got)
	}
}

func TestCustomDetailFixtureKeepsIndependentSellersAndDefault(t *testing.T) {
	f := fake.Start()
	t.Cleanup(f.Close)
	f.SetSearchItems([]fake.SearchItem{{ID: "201", SellerID: "901"}, {ID: "202"}}, []fake.SearchItem{{ID: "203", SellerID: "903"}})
	for _, test := range []struct{ itemID, sellerID string }{{"201", "901"}, {"203", "903"}, {"202", "998"}, {"unknown", "998"}} {
		response, err := http.Get(f.Home() + "h5/mtop.taobao.idle.pc.detail/1.0/?id=" + test.itemID)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		detail, err := xianyu.ParseDetail(body)
		if err != nil || detail.SellerID != test.sellerID {
			t.Fatalf("商品 %s 的详情卖家：得到 %q，期望 %q，错误 %v", test.itemID, detail.SellerID, test.sellerID, err)
		}
	}
}
