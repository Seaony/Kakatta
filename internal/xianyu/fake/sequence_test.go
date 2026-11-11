package fake_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Seaony/Kakatta/internal/xianyu"
	"github.com/Seaony/Kakatta/internal/xianyu/fake"
)

func TestKeywordSequenceAdvancesOnlyOnInitialSearch(t *testing.T) {
	f := fake.Start()
	t.Cleanup(f.Close)
	rounds := []fake.SearchRound{
		{Page1: []fake.SearchItem{{ID: "401", Title: "第一轮", Price: "100", SellerID: "seller401"}}, Page2: []fake.SearchItem{{ID: "402", Title: "第一页后的商品", Price: "90", SellerID: "seller402"}}},
		{Page1: []fake.SearchItem{{ID: "403", Title: "第二轮", Price: "80", SellerID: "seller403"}}},
	}
	f.SetSearchSequence("MacBook 甲", rounds)
	f.SetSearchSequence("MacBook 乙", []fake.SearchRound{{Page1: []fake.SearchItem{{ID: "501", Title: "另一个词", Price: "200", SellerID: "seller501"}}}})
	rounds[0].Page1[0].ID = "不能改变已保存的序列"
	search := func(keyword, query string) []xianyu.SearchItem {
		t.Helper()
		response, err := http.Post(f.Home()+"h5/mtop.taobao.idlemtopsearch.pc.search/1.0/?"+query, "application/x-www-form-urlencoded", strings.NewReader(url.Values{"q": {keyword}}.Encode()))
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
	for _, step := range []struct{ keyword, query, item string }{
		{"MacBook 甲", "first=1", "401"}, {"MacBook 乙", "first=1", "501"},
		{"MacBook 甲", "publish=最新&final=1", "401"}, {"MacBook 甲", "step=personal", "401"},
		{"MacBook 甲", "page=2", "402"}, {"MacBook 甲", "first=1", "403"},
		{"MacBook 乙", "step=personal", "501"},
	} {
		items := search(step.keyword, step.query)
		if len(items) != 1 || items[0].ID != step.item {
			t.Fatalf("关键词/轮次/翻页混用了数据：%+v，商品=%+v", step, items)
		}
	}
	if got := search("MacBook 甲", "page=2"); len(got) != 0 || f.SearchSequenceStarts("MacBook 甲") != 2 || f.SearchSequenceStarts("MacBook 乙") != 1 {
		t.Fatal("筛选翻页不能推进轮次，空页仍需保留列表字段")
	}
	if got := search("未配置", "first=1"); len(got) != 2 || got[0].ID != "101" {
		t.Fatal("未配置序列的搜索词必须保留原默认样例")
	}
	response, err := http.Get(f.Home() + "h5/mtop.taobao.idle.pc.detail/1.0/?id=401&n=1")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var detail struct {
		Data struct {
			Seller struct {
				ID string `json:"sellerId"`
			} `json:"sellerDO"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&detail); err != nil || detail.Data.Seller.ID != "seller401" {
		t.Fatalf("翻到新轮后旧商品的卖家映射必须仍正确：%+v，%v", detail, err)
	}
}

func TestKeywordSequencesAreIndependentUnderConcurrentRequests(t *testing.T) {
	f := fake.Start()
	t.Cleanup(f.Close)
	for _, word := range []string{"甲", "乙"} {
		f.SetSearchSequence(word, []fake.SearchRound{{Page1: []fake.SearchItem{{ID: word, Title: word, Price: "1"}}}})
	}
	var workers sync.WaitGroup
	for _, word := range []string{"甲", "乙"} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 8 {
				response, err := http.Post(f.Home()+"h5/mtop.taobao.idlemtopsearch.pc.search/1.0/?first=1", "application/x-www-form-urlencoded", strings.NewReader(url.Values{"q": {word}}.Encode()))
				if err != nil {
					t.Error(err)
					return
				}
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
			}
		}()
	}
	workers.Wait()
	if f.SearchSequenceStarts("甲") != 8 || f.SearchSequenceStarts("乙") != 8 {
		t.Fatal("并发搜索词互相推进了轮次，或序列耗尽后偷偷重置计数")
	}
}
