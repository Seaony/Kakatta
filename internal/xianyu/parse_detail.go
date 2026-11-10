package xianyu

import "strings"

// Detail 是详情接口里用到的字段（XIANYU.md 9.3 节）。
// WantKnown 为 false 时沿用搜索结果的想要人数；Images 为空时沿用搜索结果的主图。
type Detail struct {
	Images      []string
	WantCnt     int
	BrowseCnt   int
	WantKnown   bool
	BrowseKnown bool
	SellerID    string
	RegDays     int // 缺失时为 0，用 RegDuration 换算
	Zhima       string
	Description string
}

// 商品描述依次试的字段。三个都是 Mekke 猜的，待用真实页面确认（XIANYU.md 9.3 节）。
var descriptionKeys = []string{"desc", "description", "richTextDesc"}

// ParseDetail 解析详情接口正文里的 data.itemDO 和 data.sellerDO（scraper.py:1068-1117）。
// 字段缺失时用零值，只有正文不是 JSON 时才返回 error。
func ParseDetail(body []byte) (Detail, error) {
	root, err := decodeJSON(body)
	if err != nil {
		return Detail{}, err
	}
	item := asMap(pathGet(root, "data", "itemDO"))
	seller := asMap(pathGet(root, "data", "sellerDO"))

	var d Detail
	for _, info := range asSlice(item["imageInfos"]) {
		if u := FixImageURL(asText(pathGet(info, "url"))); u != "" {
			d.Images = append(d.Images, u)
		}
	}
	if n, ok := asInt(item["wantCnt"]); ok {
		d.WantCnt, d.WantKnown = int(n), true
	}
	if n, ok := asInt(item["browseCnt"]); ok {
		d.BrowseCnt, d.BrowseKnown = int(n), true
	}
	d.SellerID = strings.TrimSpace(asScalarText(seller["sellerId"]))
	if n, ok := asInt(seller["userRegDay"]); ok {
		d.RegDays = int(n)
	}
	d.Zhima = asText(pathGet(seller, "zhimaLevelInfo", "levelName"))
	// 取第一个是字符串的字段，照 Mekke 的写法。
	for _, k := range descriptionKeys {
		if s, ok := item[k].(string); ok {
			d.Description = s
			break
		}
	}
	return d, nil
}
