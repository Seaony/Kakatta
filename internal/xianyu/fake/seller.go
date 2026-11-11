package fake

import (
	"encoding/json"
	"net/http"
)

const fakeSeller = `<!doctype html><meta charset="utf-8"><title>卖家主页</title>
<div>本地卖家998</div>
<ul><li id="ratings" style="width:180px;height:40px"><div>信用及评价</div></li></ul>
<div style="height:2400px">卖家商品</div>
<script>
const seller = '?userId=' + encodeURIComponent(new URLSearchParams(location.search).get('userId'));
fetch(API + '/h5/mtop.idle.web.user.page.head/1.0/' + seller);
fetch(API + '/h5/mtop.idle.web.xyh.item.list/1.0/' + seller);
document.getElementById('ratings').addEventListener('click', () => {
  fetch(API + '/h5/mtop.idle.web.trade.rate.list/1.0/' + seller);
});
</script>`

type SellerVisitCounts struct {
	Pages, Head, Items, Ratings int
}

// EnableSellerProfile 让卖家主页具备商品和评价，自己的主页仍使用原来的登录样例。
func (f *Xianyu) EnableSellerProfile() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sellerProfile = true
}

func (f *Xianyu) SellerHits() SellerVisitCounts {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sellerHits
}

func (f *Xianyu) serveSellerAPI(w http.ResponseWriter, r *http.Request, kind string) bool {
	f.mu.Lock()
	if !f.sellerProfile || r.URL.Query().Get("userId") == "" {
		f.mu.Unlock()
		return false
	}
	switch kind {
	case "head":
		f.sellerHits.Head++
	case "items":
		f.sellerHits.Items++
	case "ratings":
		f.sellerHits.Ratings++
	}
	f.mu.Unlock()
	var data map[string]any
	switch kind {
	case "head":
		data = map[string]any{"module": map[string]any{
			"base": map[string]any{
				"displayName": "本地卖家" + r.URL.Query().Get("userId"), "introduction": "自用设备升级",
				"avatar":  map[string]any{"avatar": f.srv.URL + "/images/avatar.jpg"},
				"ylzTags": []any{map[string]any{"text": "卖家信用极好", "attributes": map[string]any{"role": "seller", "level": 4}}},
			},
			"tabs": map[string]any{"item": map[string]any{"number": 2}, "rate": map[string]any{"number": 1}},
		}}
	case "items":
		data = map[string]any{"nextPage": false, "cardList": []any{
			map[string]any{"cardData": map[string]any{"id": "101", "title": "MacBook Pro M3", "itemStatus": 0, "priceInfo": map[string]any{"price": "8999"}, "picInfo": map[string]any{"picUrl": f.srv.URL + "/images/101.jpg"}}},
			map[string]any{"cardData": map[string]any{"id": "104", "title": "旧键盘", "itemStatus": 1, "priceInfo": map[string]any{"price": "99"}, "picInfo": map[string]any{"picUrl": f.srv.URL + "/images/104.jpg"}}},
		}}
	case "ratings":
		data = map[string]any{"nextPage": false, "cardList": []any{
			map[string]any{"cardData": map[string]any{"rateId": "r998", "feedback": "描述准确，发货及时", "rate": 1, "rateTagList": []any{map[string]any{"text": "卖家"}}, "raterUserNick": "本地买家", "gmtCreate": "2026-11-01", "pictCdnUrlList": []any{}}},
		}}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ret": []string{"SUCCESS::调用成功"}, "data": data})
	return true
}
