package prompts

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
)

func titleSnapshot(t *testing.T) Snapshot {
	t.Helper()
	s, err := Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func titlePrice(value int64) *int64 { return &value }

func titleBlocks(t *testing.T, user string) (string, string) {
	t.Helper()
	rest, ok := strings.CutPrefix(user, "## 需求清单\n```json\n")
	if !ok {
		t.Fatalf("需求清单标题或JSON围栏不符：%q", user)
	}
	profile, rest, ok := strings.Cut(rest, "\n```\n\n## 商品（搜索列表）\n```json\n")
	if !ok {
		t.Fatalf("商品标题或JSON围栏不符：%q", user)
	}
	items, ok := strings.CutSuffix(rest, "\n```")
	if !ok {
		t.Fatalf("商品JSON缺结尾围栏：%q", user)
	}
	return profile, items
}

func titleCompact(t *testing.T, text string) string {
	t.Helper()
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(text)); err != nil {
		t.Fatal(err)
	}
	return compact.String()
}

func TestBuildTitleExactProjectionAndOrder(t *testing.T) {
	s := titleSnapshot(t)
	p := analysis.Profile{
		Summary: "不要发送清单概要", NextNo: 100,
		Specs:   []analysis.ProfileSpec{{Name: "容量", Options: []string{"512G", "256G"}, Values: []string{"256G"}, Source: analysis.SourceUser, Edited: true}, {Name: "颜色"}},
		Musts:   []analysis.Requirement{{ID: "m8", Text: "不要拆修", Source: analysis.SourceUser, Edited: true, Rev: 11}},
		Bonuses: []analysis.Requirement{{ID: "b9", Text: "不要发送加分项", Rev: 12}},
		Risks: []analysis.ProfileRisk{
			{ID: "fake", Name: "伪造字典名称", Attitude: analysis.Confirm, Rev: 13},
			{ID: "c2", Name: "电池鼓包", Attitude: analysis.Reject, Source: analysis.SourceUser, Edited: true, Rev: 14},
			{ID: "seller_new", Attitude: analysis.Confirm, Rev: 15},
			{ID: "repaired", Attitude: analysis.Mind, Rev: 16},
			{ID: "no_return", Attitude: analysis.Ignore, Rev: 17},
		},
		AllowedKinds: []analysis.Kind{analysis.KindBroken, analysis.KindAccessory},
	}
	items := []TitleSource{
		{Title: "<国行&二手>", PriceCents: titlePrice(101)},
		{Title: "价格未知", Tags: []string{"包邮", "验货宝"}},
		{Title: "已知零价", PriceCents: titlePrice(0), Tags: []string{}},
	}
	taskID := int64(42)
	r, err := BuildTitle(s, &taskID, p, items)
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != client.TitleScreen || r.Schema == nil || r.Schema.Name != "title_screen" || r.System != s.Contents[client.TitleScreen] || len(r.Images) != 0 || r.TaskID == nil || *r.TaskID != 42 {
		t.Fatalf("标题请求元数据不符：%+v", r)
	}
	profile, batch := titleBlocks(t, r.User)
	wantProfile := `{"allowed_kinds":["broken","accessory"],"musts":["不要拆修"],"reject_checks":["假货高仿","电池鼓包","新号"],"specs":[{"name":"容量","options":["512G","256G"],"values":["256G"]},{"name":"颜色","options":[],"values":[]}]}`
	wantBatch := `[{"no":1,"price":1.01,"tags":[],"title":"<国行&二手>"},{"no":2,"price":null,"tags":["包邮","验货宝"],"title":"价格未知"},{"no":3,"price":0,"tags":[],"title":"已知零价"}]`
	if got := titleCompact(t, profile); got != wantProfile {
		t.Fatalf("清单投影或键序不符：\n得到%s\n应为%s", got, wantProfile)
	}
	if got := titleCompact(t, batch); got != wantBatch {
		t.Fatalf("搜索列表投影或键序不符：\n得到%s\n应为%s", got, wantBatch)
	}
	if !strings.Contains(profile, "假货高仿") || !strings.Contains(batch, "<国行&二手>") || strings.Contains(r.User, `\u`) {
		t.Fatalf("中文和原文字面不能被转义：%s", r.User)
	}
}

func TestBuildTitleBatchBoundsAndLocalNumbers(t *testing.T) {
	s := titleSnapshot(t)
	for _, count := range []int{0, 1, 20, 21} {
		items := make([]TitleSource, count)
		for i := range items {
			items[i].Title = "本批商品"
		}
		r, err := BuildTitle(s, nil, analysis.Profile{}, items)
		if count == 0 || count == 21 {
			if err == nil {
				t.Fatalf("不允许%d件作为一批", count)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%d件应可构造：%v", count, err)
		}
		profile, batch := titleBlocks(t, r.User)
		if got := titleCompact(t, profile); got != `{"allowed_kinds":[],"musts":[],"reject_checks":[],"specs":[]}` {
			t.Fatalf("空清单也必须是四个空数组：%s", got)
		}
		var decoded []map[string]any
		if err := json.Unmarshal([]byte(batch), &decoded); err != nil {
			t.Fatal(err)
		}
		if len(decoded) != count {
			t.Fatalf("商品数量=%d，应为%d", len(decoded), count)
		}
		for i, item := range decoded {
			if item["no"] != float64(i+1) {
				t.Fatalf("每批编号须从1开始：%v", decoded)
			}
		}
	}
}

func TestBuildTitlePriceRemainsExactJSONNumber(t *testing.T) {
	s := titleSnapshot(t)
	prices := []int64{1, 101, 500000, math.MaxInt64}
	items := make([]TitleSource, len(prices))
	for i, price := range prices {
		items[i] = TitleSource{Title: "精确金额", PriceCents: titlePrice(price)}
	}
	r, err := BuildTitle(s, nil, analysis.Profile{}, items)
	if err != nil {
		t.Fatal(err)
	}
	_, batch := titleBlocks(t, r.User)
	var decoded []map[string]any
	decoder := json.NewDecoder(strings.NewReader(batch))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	want := []string{"0.01", "1.01", "5000", "92233720368547758.07"}
	for i, item := range decoded {
		price, ok := item["price"].(json.Number)
		if !ok || price.String() != want[i] {
			t.Fatalf("第%d项价格精度或类型不符：%T %v，应为%s", i, item["price"], item["price"], want[i])
		}
	}
}

func TestBuildTitleSnapshotsAndTolerantValidation(t *testing.T) {
	s := titleSnapshot(t)
	p := analysis.Profile{
		Specs:        []analysis.ProfileSpec{{Name: "型号", Options: []string{"原型号"}, Values: []string{"原型号"}}},
		Musts:        []analysis.Requirement{{ID: "m1", Text: "原要求", Rev: 1}},
		Risks:        []analysis.ProfileRisk{{ID: "c2", Name: "原风险", Attitude: analysis.Reject, Rev: 1}},
		AllowedKinds: []analysis.Kind{analysis.KindBroken},
	}
	price, taskID := int64(101), int64(9)
	items := []TitleSource{{Title: "原标题", PriceCents: &price, Tags: []string{"原标签"}}}
	r, err := BuildTitle(s, &taskID, p, items)
	if err != nil {
		t.Fatal(err)
	}
	originalUser, originalSystem := r.User, r.System
	originalSchema := append([]byte(nil), r.Schema.JSON...)
	p.Specs[0].Options[0], p.Specs[0].Values[0] = "已改", "已改"
	p.Musts[0].Text, p.Risks[0].Name = "已改", "已改"
	p.AllowedKinds[0] = analysis.KindRental
	items[0].Title, items[0].Tags[0] = "已改", "已改"
	price, taskID = 999, 10
	s.Contents[client.TitleScreen] = originalSystem + "\n后续模板"
	if r.User != originalUser || r.System != originalSystem || *r.TaskID != 9 {
		t.Fatal("来源快照变化影响了已构造请求")
	}
	for _, reply := range []string{`{"items":[]}`, `{"items":[{"no":1,"kind":"invalid"}]}`} {
		if _, warnings, parseErr := analysis.ParseTitleScreen([]byte(reply), 1); parseErr != nil || len(warnings) == 0 {
			t.Fatalf("回归样例应只有warning：%v %v", warnings, parseErr)
		}
		if err := r.Validate(reply); err != nil {
			t.Fatalf("单件坏条目或缺件warning不能触发额外请求：%v", err)
		}
	}
	if err := r.Validate(`{"items":null}`); err == nil {
		t.Fatal("缺少有效items数组必须校验失败")
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			if err := r.Validate(`{"items":[]}`); err != nil {
				t.Errorf("重复校验不能共享可变候选缓存：%v", err)
			}
		})
	}
	workers.Wait()
	r.Schema.JSON[0] = '!'
	fresh, err := BuildTitle(s, nil, p, items)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fresh.Schema.JSON, originalSchema) {
		t.Fatal("改动公开Schema污染了下次请求")
	}
}
