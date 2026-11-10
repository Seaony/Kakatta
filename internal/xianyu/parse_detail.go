package xianyu

import "strings"

// Detail 是详情接口里用到的字段（XIANYU.md 9.3 节）。
// WantKnown 为 false 时沿用搜索结果的想要人数。
// Images 只保存详情图；缺图时界面可沿用搜索缩略图，模型分析不能用它补图。
type Detail struct {
	Images       []string
	WantCnt      int
	BrowseCnt    int
	WantKnown    bool
	BrowseKnown  bool
	SellerID     string
	RegDays      int // 用 RegDuration 换算
	RegDaysKnown bool
	Zhima        string
	Description  string
}

// ParseDetail 解析详情接口正文里的 data.itemDO 和 data.sellerDO（scraper.py:1068-1117）。
// 字段缺失时用零值，只有正文不是 JSON 时才返回 error。
func ParseDetail(body []byte) (Detail, error) {
	root, err := decodeJSON(body)
	if err != nil {
		return Detail{}, err
	}
	paths := rules.Fields.Detail
	item := asMap(pathGet(root, paths.Item...))
	seller := asMap(pathGet(root, paths.Seller...))

	var d Detail
	for _, info := range asSlice(pathGet(item, paths.Images...)) {
		if u := FixImageURL(asText(pathGet(info, paths.ImageURL...))); u != "" {
			d.Images = append(d.Images, u)
		}
	}
	if n, ok := asInt(pathGet(item, paths.Want...)); ok {
		d.WantCnt, d.WantKnown = int(n), true
	}
	if n, ok := asInt(pathGet(item, paths.Browse...)); ok {
		d.BrowseCnt, d.BrowseKnown = int(n), true
	}
	d.SellerID = strings.TrimSpace(asScalarText(pathGet(seller, paths.SellerID...)))
	if n, ok := asInt(pathGet(seller, paths.RegDays...)); ok {
		d.RegDays, d.RegDaysKnown = int(n), true
	}
	d.Zhima = asText(pathGet(seller, paths.Zhima...))
	// 取第一个字符串，即使内容为空也不跳到后面的路径。
	for _, path := range paths.Descriptions {
		if s, ok := pathGet(item, path...).(string); ok {
			d.Description = s
			break
		}
	}
	return d, nil
}
