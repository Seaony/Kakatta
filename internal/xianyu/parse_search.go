package xianyu

import (
	"errors"
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
	OriPriceText         string
	Image, Area          string
	SellerNick           string
	SellerID             string
	WebURL, AppURL       string
	PublishedAt          time.Time
	PublishedKnown       bool
	WantNum              int
	WantKnown            bool
	FreeShip, Inspected  bool // 包邮、验货宝
}

// ErrSearchStructure 表示搜索响应没有合法的商品列表，调用方应提示页面结构可能变了。
var ErrSearchStructure = errors.New("搜索响应缺少有效的 resultList 列表")

// ParseSearch 解析搜索接口正文里的 data.resultList（parsers.py:12-66）。
// 按项解析：一项坏了只跳过这一项，缺商品 ID 的跳过，同一个 ID 只留第一次出现的。
// 列表缺失或类型错误和正常空列表分开，避免页面改版时误报没有商品。
func ParseSearch(body []byte) ([]SearchItem, error) {
	root, err := decodeJSON(body)
	if err != nil {
		return nil, err
	}
	list, ok := pathGet(root, rules.Fields.Search.List...).([]any)
	if !ok {
		return nil, ErrSearchStructure
	}
	var items []SearchItem
	seen := map[string]bool{}
	for _, raw := range list {
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
	paths := rules.Fields.Search
	main := asMap(pathGet(raw, paths.Main...))
	ex := asMap(pathGet(main, paths.Content...))
	args := asMap(pathGet(main, paths.Args...))

	var it SearchItem
	it.ID = strings.TrimSpace(asScalarText(pathGet(ex, paths.ID...)))
	if it.ID == "" {
		return it, false
	}
	it.Title = asText(pathGet(ex, paths.Title...))
	it.PriceText = searchPriceText(pathGet(ex, paths.Price...))
	it.PriceCents, it.PriceKnown = ParsePriceCents(it.PriceText)
	it.OriPriceText = asText(pathGet(ex, paths.OriginalPrice...))
	it.OriPriceCents, it.OriPriceKnown = ParsePriceCents(it.OriPriceText)
	it.Image = FixImageURL(asText(pathGet(ex, paths.Image...)))
	it.Area = asText(pathGet(ex, paths.Area...))
	it.SellerNick = asText(pathGet(ex, paths.SellerNick...))
	it.SellerID = strings.TrimSpace(asScalarText(pathGet(args, paths.SellerID...)))
	it.WebURL = WebLink(asText(pathGet(main, paths.URL...)))
	it.AppURL = AppLink(it.ID)
	if it.AppURL == "" {
		it.AppURL = it.WebURL
	}
	it.PublishedAt, it.PublishedKnown = ParsePublishTime(pathGet(args, paths.Published...))
	if n, ok := asInt(pathGet(args, paths.Want...)); ok {
		it.WantNum, it.WantKnown = int(n), true
	}
	it.FreeShip = asText(pathGet(args, paths.FreeShipTag...)) == "freeship"
	for _, tag := range asSlice(pathGet(ex, paths.Tags...)) {
		if strings.Contains(asText(pathGet(tag, paths.TagText...)), "验货宝") {
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
		b.WriteString(asScalarText(pathGet(p, rules.Fields.Search.PriceText...)))
	}
	return strings.TrimSpace(strings.ReplaceAll(b.String(), "当前价", ""))
}
