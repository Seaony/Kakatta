package xianyu

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParsePriceCents(t *testing.T) {
	cases := []struct {
		in     string
		cents  int64
		wantOK bool
	}{
		{"13999", 1399900, true},
		{"¥13999", 1399900, true},
		{"¥1,299", 129900, true},
		{"￥1,299.50", 129950, true},
		{"1，299", 129900, true},
		{"当前价¥88", 8800, true},
		{" ¥ 88 ", 8800, true},
		{"1.2万", 1200000, true},
		{"¥1.2万", 1200000, true},
		{"3万", 3000000, true},
		{"1.23456万", 1234560, true},
		{"0.1", 10, true},
		{"0.7", 70, true}, // 浮点相乘会得到 69.99999，十进制解析不会
		{"19.99", 1999, true},
		{"9.995", 1000, true}, // 超出分的位数四舍五入
		{"9.994", 999, true},
		{".5", 50, true},
		{"5.", 500, true},
		{"0", 0, true},
		{"007", 700, true},
		{"面议", 0, false},
		{"", 0, false},
		{"¥", 0, false},
		{".", 0, false},
		{"万", 0, false},
		{"1.2.3", 0, false},
		{"-5", 0, false},
		{"1e3", 0, false},
		{"100-200", 0, false},
		{"万1", 0, false},
		{"1万万", 0, false},
		{"99999999999999999999", 0, false}, // 太大，不溢出
	}
	for _, c := range cases {
		got, ok := ParsePriceCents(c.in)
		if ok != c.wantOK || got != c.cents {
			t.Errorf("ParsePriceCents(%q) = %d, %v，期望 %d, %v", c.in, got, ok, c.cents, c.wantOK)
		}
	}
}

func TestWebLink(t *testing.T) {
	cases := map[string]string{
		"fleamarket://item?id=123456":       "https://www.goofish.com/item?id=123456",
		"https://www.goofish.com/item?id=1": "https://www.goofish.com/item?id=1",
		"":                                  "",
		"other://fleamarket://item?id=1":    "other://fleamarket://item?id=1",
		"fleamarket://item?id=99&categoryId=50025969": "https://www.goofish.com/item?id=99&categoryId=50025969",
	}
	for in, want := range cases {
		if got := WebLink(in); got != want {
			t.Errorf("WebLink(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// 期望值由本机 python3 按上游 utils.py:105-114 的算法（re.search + urllib.parse.quote）算出，逐字节比对。
func TestAppLinkMatchesUpstream(t *testing.T) {
	cases := map[string]string{
		"123456":       "https://pages.goofish.com/sharexy?loadingVisible=false&bft=item&bfs=idlepc.item&spm=a21ybx.item.0.0&bfp=%7B%22id%22%3A123456%7D",
		"987654321012": "https://pages.goofish.com/sharexy?loadingVisible=false&bft=item&bfs=idlepc.item&spm=a21ybx.item.0.0&bfp=%7B%22id%22%3A987654321012%7D",
	}
	for id, want := range cases {
		if got := AppLink(id); got != want {
			t.Errorf("AppLink(%q)\n得到 %s\n期望 %s", id, got, want)
		}
	}
	for _, id := range []string{"", "abc", "12a", " 12"} {
		if got := AppLink(id); got != "" {
			t.Errorf("AppLink(%q) = %q，不是纯数字时应返回空", id, got)
		}
	}
}

func TestParsePublishTime(t *testing.T) {
	want := time.UnixMilli(1710000000000).UTC()
	ok := []any{"1710000000000", " 1710000000000 ", json.Number("1710000000000"), float64(1710000000000), int64(1710000000000), "1710000000000.0"}
	for _, v := range ok {
		got, known := ParsePublishTime(v)
		if !known || !got.Equal(want) || got.Location() != time.UTC {
			t.Errorf("ParsePublishTime(%#v) = %v, %v，期望 %v", v, got, known, want)
		}
	}
	bad := []any{nil, "", "昨天", "1710000000000.5", true, 0, "-1", map[string]any{}, json.Number("1e400")}
	for _, v := range bad {
		if got, known := ParsePublishTime(v); known {
			t.Errorf("ParsePublishTime(%#v) = %v，期望未知", v, got)
		}
	}
}

// 期望值由本机 python3 按上游 utils.py:131-156 的算法算出。
func TestRegDuration(t *testing.T) {
	cases := []struct{ days, years, months int }{
		{1, 0, 0},
		{15, 0, 0},
		{16, 0, 1},
		{45, 0, 1},
		{46, 0, 2},
		{350, 0, 11},
		{351, 1, 0}, // 月数四舍五入到 12，进位成 1 年
		{365, 1, 0},
		{366, 1, 0},
		{700, 1, 11},
		{730, 2, 0},
		{1000, 2, 9},
		{1461, 4, 0},
		{3652, 10, 0},
	}
	for _, c := range cases {
		y, m, ok := RegDuration(c.days)
		if !ok || y != c.years || m != c.months {
			t.Errorf("RegDuration(%d) = %d 年 %d 月 %v，期望 %d 年 %d 月", c.days, y, m, ok, c.years, c.months)
		}
	}
	for _, d := range []int{0, -3} {
		if _, _, ok := RegDuration(d); ok {
			t.Errorf("RegDuration(%d) 应为未知", d)
		}
	}
}

func TestFixImageURL(t *testing.T) {
	cases := map[string]string{
		"//img.alicdn.com/a.jpg":       "https://img.alicdn.com/a.jpg",
		" //img.alicdn.com/a.jpg ":     "https://img.alicdn.com/a.jpg",
		"https://img.alicdn.com/a.jpg": "https://img.alicdn.com/a.jpg",
		"http://img.alicdn.com/a.jpg":  "http://img.alicdn.com/a.jpg",
		"":                             "",
	}
	for in, want := range cases {
		if got := FixImageURL(in); got != want {
			t.Errorf("FixImageURL(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestAsInt(t *testing.T) {
	ok := map[string]struct {
		in   any
		want int64
	}{
		"json 数字":       {json.Number("12"), 12},
		"数字字符串":         {" 12 ", 12},
		"浮点整数":          {float64(12), 12},
		"带零小数的字符串":      {"12.0", 12},
		"负数":            {json.Number("-1"), -1},
		"大整数不丢精度":       {json.Number("2200000000000000123"), 2200000000000000123},
		"带小数的大整数不丢精度":   {json.Number("2200000000000000123.0"), 2200000000000000123},
		"带小数的数字字符串不丢精度": {"9007199254740993.0", 9007199254740993},
		"指数形式不丢精度":      {json.Number("9007199254740993e0"), 9007199254740993},
		"最大整数带小数":       {"9223372036854775807.0", 9223372036854775807},
	}
	for name, c := range ok {
		got, known := asInt(c.in)
		if !known || got != c.want {
			t.Errorf("%s：asInt(%#v) = %d, %v，期望 %d", name, c.in, got, known, c.want)
		}
	}
	for _, v := range []any{nil, "", "abc", "1.5", float64(1.5), true, []any{}, "NaN", "Inf", json.Number("1e30"), "9007199254740993.1", "9223372036854775808.0", "12/1"} {
		if got, known := asInt(v); known {
			t.Errorf("asInt(%#v) = %d，期望转换失败", v, got)
		}
	}
}

func TestAsScalarText(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"abc", "abc"},
		{json.Number("880000000002"), "880000000002"},
		{float64(123456), "123456"},
		{nil, ""},
		{true, ""},
		{map[string]any{}, ""},
	}
	for _, c := range cases {
		if got := asScalarText(c.in); got != c.want {
			t.Errorf("asScalarText(%#v) = %q，期望 %q", c.in, got, c.want)
		}
	}
}
