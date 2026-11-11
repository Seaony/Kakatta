package fake_test

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/xianyu"
	"github.com/Seaony/Kakatta/internal/xianyu/fake"
)

func TestOptionalSellerFixtureMatchesPageParsersAndKeepsOwnProfile(t *testing.T) {
	f := fake.Start()
	t.Cleanup(f.Close)
	read := func(path string, status int) []byte {
		t.Helper()
		response, err := http.Get(f.Home() + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != status {
			t.Fatalf("本地卖家页面响应：状态 %d，错误 %v", response.StatusCode, err)
		}
		return body
	}
	read("h5/mtop.idle.web.xyh.item.list/1.0/?userId=998", http.StatusNotFound)
	if got := f.SellerHits(); got != (fake.SellerVisitCounts{}) {
		t.Fatalf("默认场景不应记录卖家访问：%+v", got)
	}
	f.EnableSellerProfile()
	page := read("personal?userId=998", http.StatusOK)
	if !strings.Contains(string(page), "信用及评价") || !strings.Contains(string(page), "mtop.idle.web.trade.rate.list") {
		t.Fatal("卖家页面必须提供能请求评价接口的可见标签")
	}
	head, err := xianyu.ParseSellerHead(read("h5/mtop.idle.web.user.page.head/1.0/?userId=998", http.StatusOK))
	if err != nil || head.Nick != "本地卖家998" || head.Intro != "自用设备升级" || !head.ItemCountKnown || head.ItemCount != 2 || !head.RateCountKnown || head.RateCount != 1 || head.SellerCreditLevel != "4" {
		t.Fatalf("卖家头部不能被解析为不完整资料：%+v，%v", head, err)
	}
	items, err := xianyu.ParseSellerItems(read("h5/mtop.idle.web.xyh.item.list/1.0/?userId=998", http.StatusOK))
	if err != nil || !items.ListKnown || len(items.Items) != 2 || items.OnSale != 1 || items.Sold != 1 || items.Items[0].ID != "101" || !items.Items[0].PriceKnown || items.Items[0].PriceCents != 899900 {
		t.Fatalf("卖家商品第一页：%+v，%v", items, err)
	}
	ratings, err := xianyu.ParseSellerRatings(read("h5/mtop.idle.web.trade.rate.list/1.0/?userId=998", http.StatusOK))
	if err != nil || !ratings.ListKnown || len(ratings.Recent) != 1 || ratings.AsSeller.Good != 1 || ratings.AsSeller.Total != 1 || ratings.AsBuyer.Total != 0 || ratings.Recent[0].ID != "r998" || !ratings.Recent[0].CreatedKnown {
		t.Fatalf("卖家评价第一页：%+v，%v", ratings, err)
	}
	local, _ := url.Parse(f.Home())
	for _, address := range []string{head.Avatar, items.Items[0].Image, items.Items[1].Image} {
		parsed, err := url.Parse(address)
		if err != nil || parsed.Scheme != local.Scheme || parsed.Host != local.Host {
			t.Fatalf("卖家资源必须来自本地站点：%q，%v", address, err)
		}
	}
	read("personal", http.StatusOK)
	own, err := xianyu.ParseSellerHead(read("h5/mtop.idle.web.user.page.head/1.0/", http.StatusOK))
	if err != nil || own.Nick != "测试号" || own.ItemCountKnown || own.RateCountKnown {
		t.Fatalf("登录取昵称必须保留原来的稀疏样例：%+v，%v", own, err)
	}
	if got := f.SellerHits(); got != (fake.SellerVisitCounts{Pages: 1, Head: 1, Items: 1, Ratings: 1}) {
		t.Fatalf("卖家计数不能混入自己的主页：%+v", got)
	}
}

func TestAutomaticLoginFixtureRequiresExplicitOptIn(t *testing.T) {
	f := fake.Start()
	t.Cleanup(f.Close)
	request := func(expectLogin bool) {
		t.Helper()
		response, err := http.Get(f.Home())
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		found := false
		for _, cookie := range response.Cookies() {
			if cookie.Name == "unb" {
				found = true
				if !cookie.Expires.After(time.Now()) || cookie.Path != "/" {
					t.Fatal("本地登录标记必须能跨浏览器重启保留")
				}
			}
		}
		if found != expectLogin || response.StatusCode != http.StatusOK {
			t.Fatalf("自动登录必须显式开启：得到 %t，期望 %t", found, expectLogin)
		}
	}
	request(false)
	f.EnableAutomaticLogin()
	request(true)
}
