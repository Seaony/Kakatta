package xianyu

import (
	"encoding/json"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 价格文字里要去掉的部分（XIANYU.md 9.5 节）。全角逗号也去掉，和半角逗号一样只是千分位。
var priceCleaner = strings.NewReplacer("当前价", "", "¥", "", "￥", "", ",", "", "，", "")

// ParsePriceCents 把价格文字换算成「分」（XIANYU.md 9.5 节）。
// 支持「¥1,299」「当前价¥88」「1.2万」这类写法；不是合法数字（如「面议」）时 ok 为 false。
// 全程按十进制字符串计算，不经过浮点数，避免 0.1 这类误差；超出分的位数四舍五入。
func ParsePriceCents(s string) (cents int64, ok bool) {
	s = strings.TrimSpace(priceCleaner.Replace(s))
	scale := 2 // 元 → 分
	if strings.HasSuffix(s, "万") {
		s = strings.TrimSpace(strings.TrimSuffix(s, "万"))
		scale = 6 // 万元 → 分
	}
	return scaleDecimal(s, scale)
}

// scaleDecimal 把「123.45」这样的非负十进制数乘以 10^scale 取整，多出的小数位四舍五入。
func scaleDecimal(s string, scale int) (int64, bool) {
	intPart, frac, _ := strings.Cut(s, ".")
	if intPart == "" && frac == "" {
		return 0, false
	}
	if !allDigits(intPart) || !allDigits(frac) {
		return 0, false
	}
	intPart = strings.TrimLeft(intPart, "0")
	// int64 最多 19 位，留一位余量防止进位溢出。
	if len(intPart)+scale > 18 {
		return 0, false
	}
	digits := intPart
	roundUp := false
	if len(frac) > scale {
		roundUp = frac[scale] >= '5'
		frac = frac[:scale]
	}
	digits += frac + strings.Repeat("0", scale-len(frac))
	var v int64
	if digits != "" {
		n, err := strconv.ParseInt(digits, 10, 64)
		if err != nil {
			return 0, false
		}
		v = n
	}
	if roundUp {
		v++
	}
	return v, true
}

// allDigits 判断字符串是否只含 ASCII 数字。空字符串返回 true，由调用方另外判断。
func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// WebLink 把 main.targetUrl 换成网页链接（XIANYU.md 9.7 节）：fleamarket:// 换成 https://www.goofish.com/，其他原样保留。
func WebLink(targetURL string) string {
	if rest, ok := strings.CutPrefix(targetURL, "fleamarket://"); ok {
		return "https://www.goofish.com/" + rest
	}
	return targetURL
}

// AppLink 用商品 ID 拼手机 App 的分享链接，和上游 utils.py:105-114 的结果逐字节一致。
// 上游只认纯数字 ID（正则 \d+），JSON 里写数字不加引号；不是纯数字时返回空字符串，
// 调用方改用网页链接（上游取不到 ID 时也是返回原网页链接）。
func AppLink(itemID string) string {
	if itemID == "" || !allDigits(itemID) {
		return ""
	}
	// Python 的 quote 和 Go 的 QueryEscape 对这串字符的编码结果相同，十六进制都是大写。
	return "https://pages.goofish.com/sharexy?loadingVisible=false&bft=item&bfs=idlepc.item&spm=a21ybx.item.0.0&bfp=" +
		url.QueryEscape(`{"id":`+itemID+`}`)
}

// ParsePublishTime 把毫秒时间戳（字符串或数字）转成 UTC 时间点（XIANYU.md 9.6 节）。
// 缺失、不是整数、不是正数时 ok 为 false。
func ParsePublishTime(v any) (time.Time, bool) {
	ms, ok := asInt(v)
	if !ok || ms <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(ms).UTC(), true
}

// RegDuration 把注册天数换算成「X 年 Y 个月」（XIANYU.md 9.8 节，照上游 utils.py:131-156）。
// 天数不是正数时 ok 为 false，显示「未知」。月数用 RoundToEven，和 Python 的 round 一致。
func RegDuration(days int) (years, months int, ok bool) {
	if days <= 0 {
		return 0, 0, false
	}
	const daysInYear = 365.25
	const daysInMonth = daysInYear / 12
	y := math.Floor(float64(days) / daysInYear)
	m := math.RoundToEven((float64(days) - y*daysInYear) / daysInMonth)
	years, months = int(y), int(m)
	if months == 12 {
		years++
		months = 0
	}
	return years, months, true
}

// FixImageURL 给 // 开头的图片地址补上 https:（XIANYU.md 9.2 节，Mekke 的做法）。
func FixImageURL(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "//") {
		return "https:" + s
	}
	return s
}

// 下面是宽容取值的辅助函数：路径缺失、类型不对时返回零值，不报错（XIANYU.md 9.1 节）。

// pathGet 沿对象键逐层取值，任何一层不是对象或键不存在就返回 nil。
func pathGet(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// asMap 取对象，不是对象时返回 nil。
func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// asSlice 取数组，不是数组时返回 nil。
func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// asText 只认字符串，其他类型返回空字符串。
func asText(v any) string {
	s, _ := v.(string)
	return s
}

// asScalarText 把字符串或数字转成文字（ID 可能是数字），其他类型返回空字符串。
func asScalarText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	}
	return ""
}

// asInt 把数字或数字字符串转成整数。带小数部分（如 1.5）、布尔值、空值都返回 ok=false；
// 「12.0」这种小数部分为 0 的照样接受。
func asInt(v any) (int64, bool) {
	switch t := v.(type) {
	case int:
		return int64(t), true
	case int64:
		return t, true
	case float64:
		return floatToInt(t)
	case json.Number:
		return textToInt(t.String())
	case string:
		return textToInt(strings.TrimSpace(t))
	}
	return 0, false
}

func textToInt(s string) (int64, bool) {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, true
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return floatToInt(f)
}

func floatToInt(f float64) (int64, bool) {
	// 2^63 用浮点数正好能精确表示，作为上界（不含）。
	if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || f < math.MinInt64 || f >= math.MaxInt64 {
		return 0, false
	}
	return int64(f), true
}
