package prompts

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
)

func profileSnapshot(t *testing.T) Snapshot {
	t.Helper()
	s, err := Resolve(map[client.Kind]string{client.Profile: "自定义模板 <原文>：输出 \"specs\" 和 \"risks\""})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func profileSection(t *testing.T, user, name string) json.RawMessage {
	t.Helper()
	marker := "\n\n## " + name + "\n```json\n"
	start := strings.LastIndex(user, marker)
	if start < 0 {
		t.Fatalf("没有找到%s：%s", name, user)
	}
	start += len(marker)
	end := strings.Index(user[start:], "\n```")
	if end < 0 {
		t.Fatalf("%s没有结束围栏", name)
	}
	return json.RawMessage(user[start : start+end])
}

func TestBuildProfileProjectsDictionaryAndPreservesUserText(t *testing.T) {
	s := profileSnapshot(t)
	keyword := " \t平板 Ａ  "
	requirement := "第一行\n第二行 <细节> & 原样保留"
	req, err := BuildProfile(s, nil, keyword, requirement, analysis.Profile{})
	if err != nil {
		t.Fatal(err)
	}
	if req.Kind != client.Profile || req.TaskID != nil || len(req.Images) != 0 || req.System != s.Contents[client.Profile]+"\n\n"+SellerDataSystemSuffix || req.Schema == nil || req.Schema.Name != "task_profile" || req.Validate == nil {
		t.Fatalf("用途或模板配置错误：%+v", req)
	}
	if !strings.HasPrefix(req.User, "我想买："+keyword+"\n我的要求："+requirement+"\n\n## 检查项字典\n") {
		t.Fatalf("用户文字被修整或替换：%q", req.User)
	}
	var rows []map[string]any
	if err := json.Unmarshal(profileSection(t, req.User, "检查项字典"), &rows); err != nil {
		t.Fatal(err)
	}
	dictionary := analysis.RiskDictionary()
	if len(rows) != len(dictionary) {
		t.Fatalf("字典项缺失：%d/%d", len(rows), len(dictionary))
	}
	for i, risk := range dictionary {
		want := map[string]any{"id": risk.ID, "name": risk.Name, "category": risk.Category, "default_attitude": string(risk.DefaultAttitude), "hint": risk.Hint}
		if !reflect.DeepEqual(rows[i], want) {
			t.Fatalf("字典投影或顺序错误，第%d项：%+v", i, rows[i])
		}
	}
	if got := string(profileSection(t, req.User, "必须保留的条目")); got != "[]" {
		t.Fatalf("无编辑项必须传空数组：%s", got)
	}
	if strings.Contains(string(req.Schema.JSON), `"rev"`) {
		t.Fatal("内部版本进入了生成Schema")
	}
}

func TestBuildProfileRetainsEditedItemsInCategoryOrderWithoutRevision(t *testing.T) {
	current := analysis.Profile{
		Summary: "不发给模型的旧总结", NextNo: 100, AllowedKinds: []analysis.Kind{analysis.KindBroken},
		Specs: []analysis.ProfileSpec{
			{Name: "未改规格", Source: analysis.SourceAI},
			{Name: "存储", Options: []string{"１２８G", "256G"}, Values: []string{"256G"}, Source: analysis.SourceUser, Edited: true},
			{Name: "颜色", Options: []string{"红<&>"}, Values: []string{}, Source: analysis.SourceAI, Edited: true},
		},
		Musts: []analysis.Requirement{
			{ID: "m1", Text: "未改条件", Source: analysis.SourceUser, Rev: 1},
			{ID: "m30", Text: "国行", Source: analysis.SourceUser, Edited: true, Rev: 7},
			{ID: "m31", Text: "在保", Source: analysis.SourceUser, Edited: true, Rev: 8},
		},
		Bonuses: []analysis.Requirement{{ID: "b32", Text: "带盒", Source: analysis.SourceAI, Edited: true, Rev: 4}},
		Risks: []analysis.ProfileRisk{
			{ID: "off_platform", Source: analysis.SourceSystem, Attitude: analysis.Reject, Rev: 1},
			{ID: "id_lock", Source: analysis.SourceUser, Attitude: analysis.Ignore, Edited: true, Rev: 3},
			{ID: "c33", Name: "屏幕亮斑", Hint: "白底有亮斑", Question: "拍下白底<&>？", Source: analysis.SourceUser, Attitude: analysis.Confirm, Edited: true, Rev: 9},
		},
	}
	before, _ := json.Marshal(current)
	req, err := BuildProfile(profileSnapshot(t), nil, "平板", "需要国行", current)
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	raw := profileSection(t, req.User, "必须保留的条目")
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"name": "存储", "options": []any{"１２８G", "256G"}, "values": []any{"256G"}, "source": "user", "edited": true},
		{"name": "颜色", "options": []any{"红<&>"}, "values": []any{}, "source": "ai", "edited": true},
		{"id": "m30", "text": "国行", "source": "user", "edited": true},
		{"id": "m31", "text": "在保", "source": "user", "edited": true},
		{"id": "b32", "text": "带盒", "source": "ai", "edited": true},
		{"id": "id_lock", "attitude": "ignore", "source": "user", "edited": true},
		{"id": "c33", "name": "屏幕亮斑", "hint": "白底有亮斑", "question": "拍下白底<&>？", "attitude": "confirm", "source": "user", "edited": true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("编辑项有丢失、改写或排序变化：\n得到%+v\n应为%+v", got, want)
	}
	if strings.Contains(string(raw), `"rev"`) || strings.Contains(string(raw), `"next_no"`) || strings.Contains(string(raw), `"allowed_kinds"`) || strings.Contains(string(raw), `\u003c`) {
		t.Fatalf("多发内部字段或转义了原文：%s", raw)
	}
	after, _ := json.Marshal(current)
	if string(before) != string(after) {
		t.Fatal("构造请求修改了原清单")
	}
}

func TestBuildProfileCopiesTaskSchemaAndInputSnapshot(t *testing.T) {
	s := profileSnapshot(t)
	taskID := int64(17)
	current := analysis.Profile{Specs: []analysis.ProfileSpec{{Name: "存储", Options: []string{"128G"}, Values: []string{"128G"}, Source: analysis.SourceUser, Edited: true}}}
	first, err := BuildProfile(s, &taskID, "平板", "原要求", current)
	if err != nil {
		t.Fatal(err)
	}
	taskID = 99
	current.Specs[0].Name = "后来改的规格"
	current.Specs[0].Options[0] = "后来改的选项"
	current.Specs[0].Values[0] = "后来改的值"
	s.Contents[client.Profile] = "后来改的模板"
	if first.TaskID == nil || *first.TaskID != 17 || strings.Contains(first.User, "后来改") || strings.Contains(first.System, "后来改") {
		t.Fatal("请求仍引用调用方的可变输入")
	}
	first.Schema.Name = "改坏的名字"
	first.Schema.JSON[0] = '!'
	second, err := BuildProfile(profileSnapshot(t), nil, "平板", "原要求", analysis.Profile{})
	if err != nil || second.Schema == nil || second.Schema.Name != "task_profile" || !json.Valid(second.Schema.JSON) {
		t.Fatalf("改单次请求污染了后续Schema：%+v %v", second.Schema, err)
	}
}

func TestBuildProfileValidateOnlyParsesAndHasNoSideEffects(t *testing.T) {
	current := analysis.Profile{NextNo: math.MaxInt, Musts: []analysis.Requirement{{ID: "m1", Text: "国行", Source: analysis.SourceUser, Edited: true, Rev: 7}}}
	req, err := BuildProfile(profileSnapshot(t), nil, "平板", "国行", current)
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"summary":"会帮你找：平板","specs":[],"musts":[{"text":"模型补充的条件","source":"ai"}],"bonuses":[],"risks":[],"allowed_kinds":[],"budget_min":null,"budget_max":null,"keyword":null,"exclude_words":[]}`
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errors <- req.Validate(valid) }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("Validate不应合并或尝试分配新编号：%v", err)
		}
	}
	if current.NextNo != math.MaxInt || current.Musts[0].ID != "m1" || current.Musts[0].Rev != 7 {
		t.Fatal("校验回调改变了清单编号或版本")
	}
	for _, invalid := range []string{`{"specs":[]}`, "<think>未闭合", "<think>仅有思考</think>"} {
		if err := req.Validate(invalid); err == nil {
			t.Fatalf("非法生成结果被接受：%s", invalid)
		}
	}
}

func TestBuildProfileRejectsInvalidSnapshot(t *testing.T) {
	for _, snapshot := range []Snapshot{{}, {AssetVersion: AssetVersion + 1, Contents: map[client.Kind]string{client.Profile: "x"}}} {
		if _, err := BuildProfile(snapshot, nil, "平板", "国行", analysis.Profile{}); err == nil {
			t.Fatal("无效模板快照被接受")
		}
	}
}
