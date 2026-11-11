package prompts

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
)

func TestBuildAnalysisRejectsImageEvidenceWithoutProvidedSource(t *testing.T) {
	profile := analysis.Profile{
		Musts:   []analysis.Requirement{{ID: "m1", Rev: 1}, {ID: "m2", Rev: 1}},
		Bonuses: []analysis.Requirement{{ID: "b3", Rev: 1}},
		Risks:   []analysis.ProfileRisk{{ID: "id_lock", Rev: 1}, {ID: "repaired", Rev: 1}, {ID: "water_damage", Rev: 1}},
	}
	for _, test := range []struct {
		name, evidence string
		images         int
		invalid        bool
	}{
		{"没有图片也没有序号", "看到了原装配件", 0, true},
		{"没有图片但写了序号", "第1张图：原装配件", 0, true},
		{"图片序号为零", "第0张图：原装配件", 1, true},
		{"图片序号为负数", "第-1张图：原装配件", 1, true},
		{"全角负数", "第－１张图：原装配件", 1, true},
		{"数学负号", "第−1张图：原装配件", 1, true},
		{"中文数字前的负号", "第-三张图：原装配件", 1, true},
		{"阿拉伯数字前的中文负号", "第负1张图：原装配件", 1, true},
		{"图片序号超过实际数", "第2张图：原装配件", 1, true},
		{"中文第三张越界", "第三张图：原装配件", 1, true},
		{"中文零号", "第零张图：原装配件", 1, true},
		{"中文〇号", "第〇张图：原装配件", 1, true},
		{"中文负数", "第负一张图：原装配件", 1, true},
		{"中文多处有越界", "第一张图和第三张图：原装配件", 2, true},
		{"中英文数字混用引用", "第1张图和第三张图：原装配件", 2, true},
		{"中文数字带空白", "第\u3000十\t二 张\n图：原装配件", 1, true},
		{"中文第一张有效", "第一张图：原装配件", 1, false},
		{"中文两张有效", "第两张图：原装配件", 2, false},
		{"不是明确编号不猜", "第二代相机，十一张图的拼图", 1, false},
		{"全角与空白", "第\u3000２\t张\n图：原装配件", 1, true},
		{"多处引用有一个越界", "第1张图与第3张图：原装配件", 2, true},
		{"多处引用有一个为零", "第1张图与第0张图：原装配件", 2, true},
		{"超大序号", "第" + strings.Repeat("9", 200) + "张图：原装配件", 2, true},
		{"前导零不改变越界", "第0002张图：原装配件", 1, true},
		{"全零不是第一张", "第0000张图：原装配件", 1, true},
		{"提供的图片", "第1张图：原装配件", 1, false},
		{"多处引用全部有效", "第1张图与第2张图：原装配件", 2, false},
		{"有图未明确编号不猜测", "照片里能看到原装配件", 1, false},
		{"合法前导零", "第" + strings.Repeat("0", 200) + "1张图：原装配件", 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			pictures := make([]string, test.images)
			for i := range pictures {
				pictures[i] = "data:image/jpeg;base64,AA=="
			}
			request, originals, err := BuildAnalysis(analysisTestSnapshot(t), nil, profile, ProductSource{Title: "普通商品"}, nil, pictures)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(map[string]any{
				"image_count": 999999,
				"musts": []any{
					map[string]any{"id": "m1", "status": "met", "source": "image", "evidence": test.evidence},
					map[string]any{"id": "m2", "status": "unmet", "source": "image", "evidence": test.evidence},
				},
				"bonuses": []any{map[string]any{"id": "b3", "status": "met", "source": "image", "evidence": test.evidence}},
				"checks": []any{
					map[string]any{"key": "id_lock", "status": "ok", "source": "image", "evidence": test.evidence},
					map[string]any{"key": "repaired", "status": "risk", "source": "image", "evidence": test.evidence},
					map[string]any{"key": "water_damage", "status": "suspect", "source": "image", "evidence": test.evidence},
				},
				"extra_findings": []any{map[string]any{"name": "屏幕亮斑", "source": "image", "evidence": test.evidence}},
				"adjust":         map[string]any{"value": 0}, "summary": "请对照证据",
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := request.Validate(string(body)); err != nil {
				t.Fatal("证据警告不能触发模型重试", err)
			}
			facts, warnings, err := analysis.ParseFacts(body, profile, originals)
			if err != nil {
				t.Fatal(err)
			}
			if facts.Musts[1].Status != analysis.ReqUnknown || facts.Musts[1].Source != nil || facts.Musts[1].Evidence != nil {
				t.Fatal("仅看图的不满足仍应按未知处理")
			}
			if test.invalid {
				if facts.Musts[0].Status != analysis.ReqUnknown || facts.Bonuses[0].Status != analysis.ReqUnknown || facts.Checks[0].Status != analysis.CheckUnknown || facts.Checks[1].Status != analysis.CheckSuspect || facts.Checks[2].Status != analysis.CheckSuspect || len(facts.ExtraFindings) != 0 || len(warnings) != 7 {
					t.Fatalf("未提供或越界图片仍被当成证据：%+v，警告=%+v", facts, warnings)
				}
				for _, warning := range warnings {
					if warning.Code != "analysis.image_evidence_missing" {
						t.Fatalf("图片证据不能报成文字原文缺失：%+v", warning)
					}
				}
				for _, requirement := range []analysis.RequirementFact{facts.Musts[0], facts.Bonuses[0]} {
					if requirement.Source != nil || requirement.Evidence != nil {
						t.Fatal("未知需求仍带伪造的图片来源或证据")
					}
				}
			} else if facts.Musts[0].Status != analysis.ReqMet || facts.Bonuses[0].Status != analysis.ReqMet || facts.Checks[0].Status != analysis.CheckOK || facts.Checks[1].Status != analysis.CheckRisk || len(facts.ExtraFindings) != 1 || len(warnings) != 0 {
				t.Fatalf("有效图片或未明确编号的语义被擅自否定：%+v，警告=%+v", facts, warnings)
			}
			for _, malformed := range []string{`{}`, `{"checks":null,"adjust":{"value":0},"summary":"x"}`, `{"checks":[],"adjust":{"value":"bad"},"summary":"x"}`} {
				if request.Validate(malformed) == nil {
					t.Fatal("图片证据降级吞掉了必需字段错误")
				}
			}
		})
	}
}

func TestImageEvidenceChineseNumbersRespectKnownImageCount(t *testing.T) {
	profile := analysis.Profile{Risks: []analysis.ProfileRisk{{ID: "id_lock", Rev: 1}}}
	for _, test := range []struct {
		text  string
		value int64
	}{
		{"一", 1}, {"二", 2}, {"两", 2}, {"三", 3}, {"四", 4}, {"五", 5}, {"六", 6}, {"七", 7}, {"八", 8}, {"九", 9},
		{"十", 10}, {"十一", 11}, {"十二", 12}, {"二十", 20}, {"二十三", 23}, {"九十九", 99},
		{"一百", 100}, {"一百零一", 101}, {"一百〇一", 101}, {"一百一十", 110}, {"一百一十一", 111}, {"两百零二", 202},
		{"一千", 1000}, {"一千零一", 1001}, {"一千零一十", 1010}, {"一千一百", 1100}, {"一千二百三十四", 1234},
		{"一万", 10000}, {"一万零一", 10001}, {"一万零一十", 10010}, {"一万零一百", 10100}, {"一万一千", 11000}, {"一万一千零一", 11001},
		{"一千二百三十四万五千六百七十八", 12345678}, {"一亿", 100000000}, {"一亿零一", 100000001}, {"一亿零一万", 100010000}, {"一亿一千万", 110000000},
		{"十亿", 1000000000}, {"一万亿", 1000000000000}, {"一万亿零一", 1000000000001},
		{"九千九百九十九万九千九百九十九亿九千九百九十九万九千九百九十九", 9999999999999999},
	} {
		t.Run(test.text, func(t *testing.T) {
			body := []byte(`{"checks":[{"key":"id_lock","status":"ok","source":"image","evidence":"第` + test.text + `张图：没有锁"}],"adjust":{"value":0},"summary":"请核对"}`)
			for _, exact := range []bool{false, true} {
				count := int(test.value)
				if !exact {
					count--
				}
				facts, warnings, err := analysis.ParseFacts(body, profile, analysis.EvidenceOriginals{ImageCount: &count})
				if err != nil {
					t.Fatal(err)
				}
				if exact {
					if facts.Checks[0].Status != analysis.CheckOK || len(warnings) != 0 {
						t.Fatalf("中文序号不等于其精确数值 %d：%+v %+v", test.value, facts, warnings)
					}
				} else if facts.Checks[0].Status != analysis.CheckUnknown || len(warnings) != 1 || warnings[0].Code != "analysis.image_evidence_missing" {
					t.Fatalf("中文序号越过实际图片数 %d：%+v %+v", count, facts, warnings)
				}
			}
		})
	}
	for _, text := range []string{"一百一", "一千一", "一千二百三", "一万一", "一万零一千", "一亿一万", "一亿亿", "百", "千", "十十", "一一", "零一", "一百零", "一百零零一", "一百零一十", "二千百", "一二三"} {
		t.Run("不猜测"+text, func(t *testing.T) {
			count := 1
			body := []byte(`{"checks":[{"key":"id_lock","status":"ok","source":"image","evidence":"第` + text + `张图：没有锁"}],"adjust":{"value":0},"summary":"请核对"}`)
			facts, warnings, err := analysis.ParseFacts(body, profile, analysis.EvidenceOriginals{ImageCount: &count})
			if err != nil || facts.Checks[0].Status != analysis.CheckOK || len(warnings) != 0 {
				t.Fatal("把歧义或非法写法猜成了确定编号", facts, warnings, err)
			}
		})
	}
}

func TestBuildAnalysisKeepsImageCountInIndependentValidationSnapshot(t *testing.T) {
	profile := analysis.Profile{Risks: []analysis.ProfileRisk{{ID: "id_lock", Rev: 1}}}
	for _, pictures := range [][]string{nil, {"data:image/jpeg;base64,AA=="}} {
		request, originals, err := BuildAnalysis(analysisTestSnapshot(t), nil, profile, ProductSource{Title: "普通商品"}, nil, pictures)
		if err != nil || originals.ImageCount == nil || *originals.ImageCount != len(request.Images) {
			t.Fatalf("实际请求没有明确的图片数：%+v %v", originals, err)
		}
		encoded, err := json.Marshal(originals)
		if err != nil || strings.Contains(string(encoded), "image_count") || strings.Contains(request.User, "image_count") {
			t.Fatal("内部图片数进入了可由模型回答的字段", err)
		}
		const body = `{"checks":[{"key":"id_lock","status":"ok","source":"image","evidence":"第1张图：没有锁"}],"adjust":{"value":0},"summary":"请核对"}`
		var workers sync.WaitGroup
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 1000 {
				if err := request.Validate(body); err != nil {
					t.Error(err)
					return
				}
			}
		}()
		for i := range 1000 {
			*originals.ImageCount = i
		}
		workers.Wait()
	}
}

func TestFactsWithoutHistoricalImageMetadataKeepExistingContract(t *testing.T) {
	profile := analysis.Profile{Risks: []analysis.ProfileRisk{{ID: "id_lock", Rev: 1}}}
	const body = `{"checks":[{"key":"id_lock","status":"ok","source":"image","evidence":"第9张图：没有锁"}],"adjust":{"value":0},"summary":"旧资料"}`
	facts, warnings, err := analysis.ParseFacts([]byte(body), profile, analysis.EvidenceOriginals{})
	if err != nil || len(warnings) != 0 || facts.Checks[0].Status != analysis.CheckOK {
		t.Fatal("不知道原始图片数时不能伪造已知零张", facts, warnings, err)
	}
}
