package fake

import (
	"fmt"
	"time"
)

type SearchItem struct {
	ID, Title, Price, SellerID string
}

type SearchResponseBehavior struct {
	Ret           string
	Status        int
	MissingFields bool
}

func (f *Xianyu) SetSearchResponseBehavior(behavior SearchResponseBehavior) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searchResponse = behavior
}

// SetSearchItems 替换两页结果；传空页时，页面接口仍返回已知的空列表。
func (f *Xianyu) SetSearchItems(page1, page2 []SearchItem) {
	pages := &[2][]SearchItem{append([]SearchItem(nil), page1...), append([]SearchItem(nil), page2...)}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searchPages = pages
}

func searchItemsJSON(baseURL string, items []SearchItem) map[string]any {
	list := make([]any, 0, len(items))
	for i, item := range items {
		list = append(list, searchItemJSON(baseURL, item.ID, item.Title, item.Price, time.Hour+time.Duration(i)*time.Minute))
	}
	return map[string]any{"ret": []string{"SUCCESS::调用成功"}, "data": map[string]any{"resultList": list}}
}

func omitSearchOptionalFields(response map[string]any) {
	data := response["data"].(map[string]any)
	for _, row := range data["resultList"].([]any) {
		item := row.(map[string]any)["data"].(map[string]any)["item"].(map[string]any)
		ex := item["main"].(map[string]any)["exContent"].(map[string]any)
		for _, key := range []string{"price", "userNickName", "area", "picUrl"} {
			delete(ex, key)
		}
	}
}

func searchItemJSON(baseURL, id, title, price string, age time.Duration) map[string]any {
	return map[string]any{"data": map[string]any{"item": map[string]any{"main": map[string]any{
		"exContent":  map[string]any{"itemId": id, "title": title, "price": []any{map[string]any{"text": "¥"}, map[string]any{"text": price}}, "area": "上海", "userNickName": "卖家" + id, "picUrl": baseURL + "/images/" + id + ".jpg"},
		"clickParam": map[string]any{"args": map[string]any{"publishTime": fmt.Sprint(time.Now().Add(-age).UnixMilli()), "wantNum": 2}},
		"targetUrl":  "fleamarket://item?id=" + id,
	}}}}
}
