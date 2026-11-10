package xianyu

import (
	"strings"
	"time"
)

// SearchItem 是搜索结果里的一个商品（XIANYU.md 9.2 节）。字段缺失时存空值，显示时再换成对应语言的文字。
type SearchItem struct {
	ID, Title, PriceText string
	PriceCents           int64
	PriceKnown           bool
	OriPriceCents        int64
	OriPriceKnown        bool
	Image, Area          string
	SellerNick           string
	WebURL, AppURL       string
	PublishedAt          time.Time
	PublishedKnown       bool
	WantNum              int
	WantKnown            bool
	FreeShip, Inspected  bool // 包邮、验货宝
}

// ParseSearch 解析搜索接口正文里的 data.resultList（parsers.py:12-66）。
// 按项解析：一项坏了只跳过这一项，缺商品 ID 的跳过，同一个 ID 只留第一次出现的。
// 只有正文不是 JSON 时才返回 error；没有 resultList 时和上游一样当作没有结果。
func ParseSearch(body []byte) ([]SearchItem, error) {
	root, err := decodeJSON(body)
	if err != nil {
		return nil, err
	}
	var items []SearchItem
	seen := map[string]bool{}
	for _, raw := range asSlice(pathGet(root, "data", "resultList")) {
		it, ok := parseSearchItem(raw)
		if !ok || seen[it.ID] {
			continue
		}
		seen[it.ID] = true
		items = append(items, it)
	}
	return items, nil
}

// parseSearchItem 解析一项；不是对象或没有商品 ID 时 ok 为 false。
func parseSearchItem(raw any) (SearchItem, bool) {
	main := asMap(pathGet(raw, "data", "item", "main"))
	ex := asMap(main["exContent"])
	args := asMap(pathGet(main, "clickParam", "args"))

	var it SearchItem
	it.ID = strings.TrimSpace(asScalarText(ex["itemId"]))
	if it.ID == "" {
		return it, false
	}
	it.Title = asText(ex["title"])
	it.PriceText = searchPriceText(ex["price"])
	it.PriceCents, it.PriceKnown = ParsePriceCents(it.PriceText)
	it.OriPriceCents, it.OriPriceKnown = ParsePriceCents(asText(ex["oriPrice"]))
	it.Image = FixImageURL(asText(ex["picUrl"]))
	it.Area = asText(ex["area"])
	it.SellerNick = asText(ex["userNickName"])
	it.WebURL = WebLink(asText(main["targetUrl"]))
	it.AppURL = AppLink(it.ID)
	if it.AppURL == "" {
		it.AppURL = it.WebURL
	}
	it.PublishedAt, it.PublishedKnown = ParsePublishTime(args["publishTime"])
	if n, ok := asInt(args["wantNum"]); ok {
		it.WantNum, it.WantKnown = int(n), true
	}
	it.FreeShip = asText(args["tag"]) == "freeship"
	for _, tag := range asSlice(pathGet(ex, "fishTags", "r1", "tagList")) {
		if strings.Contains(asText(pathGet(tag, "data", "content")), "验货宝") {
			it.Inspected = true
			break
		}
	}
	return it, true
}

// searchPriceText 把 ex.price 数组里每项的 text 按顺序拼起来，去掉「当前价」和首尾空白。
// 不是数组时返回空字符串（上游写「价格异常」，我们存空值）。
func searchPriceText(v any) string {
	var b strings.Builder
	for _, p := range asSlice(v) {
		b.WriteString(asScalarText(pathGet(p, "text")))
	}
	return strings.TrimSpace(strings.ReplaceAll(b.String(), "当前价", ""))
}
