package analysis

import (
	"reflect"
	"testing"
)

func TestRiskDictionaryAndGeneralDefaults(t *testing.T) {
	groups := []struct {
		attitude Attitude
		ids      []string
	}{
		{Confirm, []string{"fake", "expanded", "id_lock", "mdm", "carrier_lock", "console_banned"}},
		{Reject, []string{"assembled", "water_damage", "malfunction", "off_platform", "deposit"}},
		{Ignore, []string{"missing_parts", "washed", "notes_marks", "no_return"}},
		{Mind, []string{"refurbished", "official_replacement", "resource_device", "unknown_source", "account_bound", "repaired", "screen_replaced", "battery_replaced", "cosmetic_damage", "stains", "pilling", "odor", "mold", "mining", "size_mismatch", "photo_mismatch", "stock_photo", "vague", "pickup_only", "no_inspection", "seller_dealer", "seller_new", "seller_bad_reviews", "seller_partial_refund"}},
	}
	want := map[string]Attitude{}
	for _, g := range groups {
		for _, id := range g.ids {
			if _, duplicate := want[id]; duplicate {
				t.Fatalf("测试中的重复编号 %s", id)
			}
			want[id] = g.attitude
		}
	}
	got := RiskDictionary()
	if len(got) != 39 || len(want) != 39 {
		t.Fatalf("字典数量=%d，规格=%d", len(got), len(want))
	}
	withoutQuestion := map[string]bool{"off_platform": true, "deposit": true, "seller_new": true, "seller_bad_reviews": true, "seller_partial_refund": true}
	seen := map[string]bool{}
	for _, d := range got {
		if seen[d.ID] || want[d.ID] != d.DefaultAttitude {
			t.Errorf("字典默认态度或编号错误：%+v", d)
		}
		seen[d.ID] = true
		if d.Name == "" || d.Category == "" || d.Hint == "" {
			t.Errorf("字典内容不完整：%+v", d)
		}
		if (d.Question == "") != withoutQuestion[d.ID] {
			t.Errorf("问卖家的话错误：%s=%q", d.ID, d.Question)
		}
	}
	p := DefaultProfile()
	ids := make([]string, 0, len(p.Risks))
	for _, r := range p.Risks {
		ids = append(ids, r.ID)
		if r.Source != SourceSystem || r.Rev != 1 || r.Edited || r.Name != "" || r.Hint != "" || r.Question != "" {
			t.Errorf("通用项不应复制字典或标成已编辑：%+v", r)
		}
	}
	wantIDs := []string{"photo_mismatch", "stock_photo", "vague", "off_platform", "deposit", "pickup_only", "no_return", "no_inspection", "seller_dealer", "seller_new", "seller_bad_reviews", "seller_partial_refund"}
	if !reflect.DeepEqual(ids, wantIDs) || p.NextNo != 1 {
		t.Fatalf("通用项或序号：%v/%d", ids, p.NextNo)
	}
}

func TestRiskSnapshotsDoNotShareMutableState(t *testing.T) {
	before := RiskDictionary()
	RiskDictionary()[0].Name = "被调用方修改"
	if !reflect.DeepEqual(before, RiskDictionary()) {
		t.Fatal("字典快照修改影响下次读取")
	}
	p := DefaultProfile()
	p.Risks[0].Attitude = Ignore
	if DefaultProfile().Risks[0].Attitude != Mind {
		t.Fatal("默认清单共享切片")
	}
	custom := ProfileRisk{ID: "c1", Name: "压伤", Attitude: Mind, Source: SourceUser, Edited: true}
	p.Risks = []ProfileRisk{{ID: "id_lock", Name: "被覆盖的名称", Hint: "被覆盖的说明", Question: "被覆盖的问题", Attitude: Reject}, custom}
	original := append([]ProfileRisk(nil), p.Risks...)
	resolved := ResolveRisks(p)
	if resolved[0].Name != "ID 锁" || resolved[0].Attitude != Reject || resolved[0].Question == "被覆盖的问题" {
		t.Errorf("字典内容未解析：%+v", resolved[0])
	}
	if resolved[1].Name != "压伤" || resolved[1].Hint != "压伤" || resolved[1].Question != "" {
		t.Errorf("自定义说明未回退到名称：%+v", resolved[1])
	}
	resolved[0].Name = "修改解析结果"
	if !reflect.DeepEqual(p.Risks, original) {
		t.Fatal("解析改写原始清单")
	}
}
