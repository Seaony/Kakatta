package xianyu

import (
	"fmt"
	"reflect"
	"testing"
)

func TestParseDetail(t *testing.T) {
	d, err := ParseDetail(readFixture(t, "detail.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := Detail{
		Images:  []string{"https://img.alicdn.com/bao/uploaded/1.jpg", "https://img.alicdn.com/bao/uploaded/2.jpg"},
		WantCnt: 15, WantKnown: true,
		BrowseCnt: 321, BrowseKnown: true,
		SellerID:     "2200000000123",
		RegDays:      1000,
		RegDaysKnown: true,
		Zhima:        "芝麻信用极好",
		Description:  "九成新，快门数 3000，送原装电池。",
	}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("\n得到 %+v\n期望 %+v", d, want)
	}
}

func TestParseDetailSparse(t *testing.T) {
	d, err := ParseDetail(readFixture(t, "detail_sparse.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := Detail{
		SellerID:     "2200000000456",
		RegDays:      365, // 数字字符串也接受
		RegDaysKnown: true,
		Description:  "只有富文本描述是字符串",
	}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("\n得到 %+v\n期望 %+v", d, want)
	}
}

func TestParseDetailEdges(t *testing.T) {
	d, err := ParseDetail([]byte(`{"ret":["SUCCESS"],"data":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d, Detail{}) {
		t.Errorf("全部缺失时应为零值，得到 %+v", d)
	}
	d, err = ParseDetail([]byte(`{"data":{"itemDO":{"description":"第二个路径"}}}`))
	if err != nil || d.Description != "第二个路径" {
		t.Errorf("描述 = %q, err = %v", d.Description, err)
	}
	if _, err := ParseDetail([]byte(`{"data":`)); err == nil {
		t.Error("半截 JSON 应当报错")
	}
}

func TestParseDetailKnownNumbers(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        int
		known       bool
	}{
		{"zero", "0", 0, true},
		{"zero string", `"0"`, 0, true},
		{"negative", "-1", -1, true},
		{"integer string", `"12.0"`, 12, true},
		{"null", "null", 0, false},
		{"boolean", "false", 0, false},
		{"empty", `""`, 0, false},
		{"fraction", "1.5", 0, false},
		{"object", "{}", 0, false},
		{"array", "[]", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"data":{"itemDO":{"wantCnt":%s,"browseCnt":%s},"sellerDO":{"userRegDay":%s}}}`, tc.value, tc.value, tc.value)
			d, err := ParseDetail([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			if d.WantCnt != tc.want || d.BrowseCnt != tc.want || d.RegDays != tc.want ||
				d.WantKnown != tc.known || d.BrowseKnown != tc.known || d.RegDaysKnown != tc.known {
				t.Fatalf("数字已知状态错误：%+v", d)
			}
		})
	}
}

func TestParseDetailDescriptionKeepsFirstEmptyString(t *testing.T) {
	d, err := ParseDetail([]byte(`{"data":{"itemDO":{"desc":"","description":"不能用这个替换空串","richTextDesc":"也不能用这个"}}}`))
	if err != nil || d.Description != "" {
		t.Fatalf("第一个字符串即使为空也应保留：%q %v", d.Description, err)
	}
}

func TestParseDetailDoesNotFillMissingImages(t *testing.T) {
	for _, value := range []string{"null", "[]", `"不是数组"`, `[{"url":""},{"other":"//img.example/detail.jpg"}]`} {
		body := fmt.Sprintf(`{"data":{"itemDO":{"imageInfos":%s,"picUrl":"//img.example/search-thumbnail.jpg"}}}`, value)
		d, err := ParseDetail([]byte(body))
		if err != nil || len(d.Images) != 0 {
			t.Fatalf("详情缺图时不得把其他缩略图补进详情图：%+v %v", d, err)
		}
	}
}

func TestParseDetailUsesConfiguredPaths(t *testing.T) {
	previous := rules.Fields.Detail
	defer func() { rules.Fields.Detail = previous }()
	paths := &rules.Fields.Detail
	paths.Item = []string{"payload", "product"}
	paths.Seller = []string{"payload", "account"}
	paths.Images = []string{"gallery", "entries"}
	paths.ImageURL = []string{"image", "source"}
	paths.Want = []string{"counts", "wanted"}
	paths.Browse = []string{"counts", "viewed"}
	paths.SellerID = []string{"identity", "id"}
	paths.RegDays = []string{"age", "days"}
	paths.Zhima = []string{"credit", "label"}
	paths.Descriptions = [][]string{{"copy", "first"}, {"copy", "second"}}
	d, err := ParseDetail([]byte(`{
		"data":{"itemDO":{"imageInfos":[{"url":"//img.example/old.jpg"}],"wantCnt":99,"browseCnt":88,"desc":"旧描述"},"sellerDO":{"sellerId":"old","userRegDay":77,"zhimaLevelInfo":{"levelName":"旧信用"}}},
		"payload":{
			"product":{"gallery":{"entries":[{"image":{"source":"//img.example/new.jpg"}}]},"counts":{"wanted":0,"viewed":"9"},"copy":{"first":false,"second":"新描述"}},
			"account":{"identity":{"id":9007199254740993123},"age":{"days":0},"credit":{"label":"新信用"}}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := Detail{
		Images: []string{"https://img.example/new.jpg"}, WantCnt: 0, WantKnown: true,
		BrowseCnt: 9, BrowseKnown: true, SellerID: "9007199254740993123", RegDaysKnown: true,
		Zhima: "新信用", Description: "新描述",
	}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("解析没有完全使用配置路径：\n得到 %+v\n期望 %+v", d, want)
	}
}
