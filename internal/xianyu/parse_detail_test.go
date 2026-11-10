package xianyu

import (
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
		SellerID:    "2200000000123",
		RegDays:     1000,
		Zhima:       "芝麻信用极好",
		Description: "九成新，快门数 3000，送原装电池。",
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
		SellerID:    "2200000000456",
		RegDays:     365, // 数字字符串也接受
		Description: "只有富文本描述是字符串",
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
