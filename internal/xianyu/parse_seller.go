package xianyu

import (
	"sort"
	"strings"
	"time"
)

// 卖家商品只保存前 20 条，评价只保存最新 10 条（XIANYU.md 9.4 节，Mekke 的做法）。
const (
	SellerItemsKeep   = 20
	SellerRatingsKeep = 10
	// 评价摘要取前 60 个字。
	ratingSummaryRunes = 60
)

// SellerHead 是卖家主页头部（mtop.idle.web.user.page.head，parsers.py:126-144）。
type SellerHead struct {
	Nick, Avatar, Intro string
	ItemCount           int // tabs.item.number，上游叫「在售/已售商品数」，含义待确认
	RateCount           int // 收到的评价数
	SellerCredit        string
	SellerCreditLevel   string
	BuyerCredit         string
	BuyerCreditLevel    string
}

// ParseSellerHead 解析卖家头部，路径都在 data.module 下。字段缺失时用零值。
func ParseSellerHead(body []byte) (SellerHead, error) {
	root, err := decodeJSON(body)
	if err != nil {
		return SellerHead{}, err
	}
	module := asMap(pathGet(root, "data", "module"))
	base := asMap(module["base"])

	var h SellerHead
	h.Nick = asText(base["displayName"])
	h.Avatar = FixImageURL(asText(pathGet(base, "avatar", "avatar")))
	h.Intro = asText(base["introduction"])
	if n, ok := asInt(pathGet(module, "tabs", "item", "number")); ok {
		h.ItemCount = int(n)
	}
	if n, ok := asInt(pathGet(module, "tabs", "rate", "number")); ok {
		h.RateCount = int(n)
	}
	// 同一身份出现多次时和上游一样取最后一个。
	for _, tag := range asSlice(base["ylzTags"]) {
		text := asText(pathGet(tag, "text"))
		level := asScalarText(pathGet(tag, "attributes", "level"))
		switch asText(pathGet(tag, "attributes", "role")) {
		case "seller":
			h.SellerCredit, h.SellerCreditLevel = text, level
		case "buyer":
			h.BuyerCredit, h.BuyerCreditLevel = text, level
		}
	}
	return h, nil
}

// SellerItemStatus 是卖家商品的状态（itemStatus）。
type SellerItemStatus int

const (
	ItemStatusUnknown SellerItemStatus = iota
	ItemOnSale                         // 0 = 在售
	ItemSold                           // 1 = 已售
)

// SellerItem 是卖家主页商品列表里的一项。
type SellerItem struct {
	ID, Title  string
	PriceText  string // priceInfo.price 原文
	PriceCents int64
	PriceKnown bool
	Image      string
	Status     SellerItemStatus
}

// SellerItems 是商品列表的前 SellerItemsKeep 条，以及其中在售、已售的数量。
type SellerItems struct {
	Items  []SellerItem
	OnSale int
	Sold   int
}

// ParseSellerItems 解析卖家商品列表 data.cardList[].cardData（parsers.py:103-123）。
// 根是数组时（上游手写样例）直接当 cardList 用。缺商品 ID 的项跳过。
func ParseSellerItems(body []byte) (SellerItems, error) {
	cards, err := sellerCards(body)
	if err != nil {
		return SellerItems{}, err
	}
	var out SellerItems
	for _, card := range cards {
		if len(out.Items) == SellerItemsKeep {
			break
		}
		data := asMap(pathGet(card, "cardData"))
		it := SellerItem{ID: strings.TrimSpace(asScalarText(data["id"]))}
		if it.ID == "" {
			continue
		}
		it.Title = asText(data["title"])
		it.PriceText = asScalarText(pathGet(data, "priceInfo", "price"))
		it.PriceCents, it.PriceKnown = ParsePriceCents(it.PriceText)
		it.Image = FixImageURL(asText(pathGet(data, "picInfo", "picUrl")))
		if n, ok := asInt(data["itemStatus"]); ok {
			switch n {
			case 0:
				it.Status = ItemOnSale
				out.OnSale++
			case 1:
				it.Status = ItemSold
				out.Sold++
			}
		}
		out.Items = append(out.Items, it)
	}
	return out, nil
}

// RatingKind 是评价的好中差（rate：1 好评，0 中评，-1 差评）。
type RatingKind int

const (
	RatingUnknown RatingKind = iota
	RatingGood
	RatingNeutral
	RatingBad
)

// RatingRole 是被评价人在这笔交易里的身份。
type RatingRole int

const (
	RoleUnknown RatingRole = iota
	RoleSeller             // 作为卖家收到的评价
	RoleBuyer              // 作为买家收到的评价
)

// Rating 是评价列表里的一条（parsers.py:147-167）。
type Rating struct {
	ID           string
	Feedback     string // 完整内容
	Summary      string // 前 60 个字
	Kind         RatingKind
	RoleText     string // rateTagList[0].text 原文
	Role         RatingRole
	Rater        string
	CreatedText  string // gmtCreate 原文，真实格式待确认
	CreatedAt    time.Time
	CreatedKnown bool
	Images       []string
}

// RatingStats 是某一身份下的好评数和评价总数。
type RatingStats struct {
	Good, Total int
}

// GoodRate 返回好评率（0–1）。总数为 0 时没有好评率。
func (s RatingStats) GoodRate() (float64, bool) {
	if s.Total == 0 {
		return 0, false
	}
	return float64(s.Good) / float64(s.Total), true
}

// SellerRatings 是第一页评价的统计和最新几条。统计的是「最近一页评价」，不是全部评价。
type SellerRatings struct {
	Recent   []Rating // 按时间从新到旧，最多 SellerRatingsKeep 条
	AsSeller RatingStats
	AsBuyer  RatingStats
}

// ParseSellerRatings 解析评价列表 data.cardList[].cardData，统计好评（parsers.py:69-100），
// 再按 gmtCreate 从新到旧取前 SellerRatingsKeep 条。时间解析不出的排在最后，保持原顺序。
// 根是数组时（上游手写样例）直接当 cardList 用。
func ParseSellerRatings(body []byte) (SellerRatings, error) {
	cards, err := sellerCards(body)
	if err != nil {
		return SellerRatings{}, err
	}
	var out SellerRatings
	var all []Rating
	for _, card := range cards {
		data := asMap(pathGet(card, "cardData"))
		if data == nil {
			continue
		}
		r := parseRating(data)
		switch r.Role {
		case RoleSeller:
			out.AsSeller.Total++
			if r.Kind == RatingGood {
				out.AsSeller.Good++
			}
		case RoleBuyer:
			out.AsBuyer.Total++
			if r.Kind == RatingGood {
				out.AsBuyer.Good++
			}
		}
		all = append(all, r)
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.CreatedKnown != b.CreatedKnown {
			return a.CreatedKnown
		}
		return a.CreatedAt.After(b.CreatedAt)
	})
	if len(all) > SellerRatingsKeep {
		all = all[:SellerRatingsKeep]
	}
	out.Recent = all
	return out, nil
}

func parseRating(data map[string]any) Rating {
	r := Rating{
		ID:          asScalarText(data["rateId"]),
		Feedback:    asText(data["feedback"]),
		Rater:       asText(data["raterUserNick"]),
		CreatedText: asScalarText(data["gmtCreate"]),
	}
	r.Summary = firstRunes(r.Feedback, ratingSummaryRunes)
	if n, ok := asInt(data["rate"]); ok {
		switch n {
		case 1:
			r.Kind = RatingGood
		case 0:
			r.Kind = RatingNeutral
		case -1:
			r.Kind = RatingBad
		}
	}
	tags := asSlice(data["rateTagList"])
	if len(tags) > 0 {
		r.RoleText = asText(pathGet(tags[0], "text"))
	}
	// 和上游一样先看「卖家」再看「买家」。
	switch {
	case strings.Contains(r.RoleText, "卖家"):
		r.Role = RoleSeller
	case strings.Contains(r.RoleText, "买家"):
		r.Role = RoleBuyer
	}
	r.CreatedAt, r.CreatedKnown = parseRatingTime(data["gmtCreate"])
	for _, p := range asSlice(data["pictCdnUrlList"]) {
		if u := FixImageURL(asText(p)); u != "" {
			r.Images = append(r.Images, u)
		}
	}
	return r
}

// 闲鱼的日期文字按北京时间理解。中国不用夏令时，用固定时区，免得依赖系统时区库。
var beijing = time.FixedZone("UTC+8", 8*60*60)

// 评价时间可能的文字格式。样例是 "2024-01-01"，真实格式待确认，所以多认几种。
var ratingTimeLayouts = []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"}

// parseRatingTime 解析 gmtCreate：数字或纯数字字符串当毫秒时间戳，否则按日期文字解析。结果是 UTC。
func parseRatingTime(v any) (time.Time, bool) {
	if s, ok := v.(string); ok {
		s = strings.TrimSpace(s)
		if s == "" {
			return time.Time{}, false
		}
		if !allDigits(s) {
			for _, layout := range ratingTimeLayouts {
				if t, err := time.ParseInLocation(layout, s, beijing); err == nil {
					return t.UTC(), true
				}
			}
			return time.Time{}, false
		}
	}
	return ParsePublishTime(v)
}

// firstRunes 取前 n 个字（按 Unicode 字符数，不按字节，避免把汉字截成乱码）。
func firstRunes(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// sellerCards 取卖家商品和评价接口的 data.cardList；根是数组时直接用根。
// 只有正文不是 JSON 时才返回 error。
func sellerCards(body []byte) ([]any, error) {
	root, err := decodeJSON(body)
	if err != nil {
		return nil, err
	}
	if arr, ok := root.([]any); ok {
		return arr, nil
	}
	return asSlice(pathGet(root, "data", "cardList")), nil
}
