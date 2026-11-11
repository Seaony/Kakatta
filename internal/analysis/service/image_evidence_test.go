package modelservice

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/prompts"
)

func TestActualModelReplyUsesProvidedImageCountOnFinalParse(t *testing.T) {
	profile := analysis.Profile{Musts: []analysis.Requirement{{ID: "m1", Rev: 1}}, Risks: []analysis.ProfileRisk{{ID: "id_lock", Attitude: analysis.Confirm, Rev: 1}}}
	for _, test := range []struct {
		name, evidence string
		images         int
		invalid        bool
	}{
		{"没有图片", "没有锁", 0, true},
		{"引用未提供图片", "第2张图：没有锁", 1, true},
		{"数学负号图片序号", "第−1张图：没有锁", 1, true},
		{"引用已提供图片", "第1张图：没有锁", 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newModelFixture(t)
			body := `{"image_count":100,"musts":[{"id":"m1","status":"met","source":"image","evidence":"` + test.evidence + `"}],"checks":[{"key":"id_lock","status":"ok","source":"image","evidence":"` + test.evidence + `"}],"extra_findings":[{"name":"亮斑","source":"image","evidence":"` + test.evidence + `"}],"adjust":{"value":0},"summary":"请核对"}`
			upstream := trialServer(t, func(w http.ResponseWriter, _ *http.Request, _ int, _ map[string]any) {
				trialReply(w, body, "stop", trialUsage(2, 3, nil))
			})
			trialProvider(t, f, upstream)
			snapshot, err := f.s.PromptSnapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			pictures := make([]string, test.images)
			for i := range pictures {
				pictures[i] = "data:image/jpeg;base64,AA=="
			}
			request, originals, err := prompts.BuildAnalysis(snapshot, nil, profile, prompts.ProductSource{Title: "普通商品"}, nil, pictures)
			if err != nil {
				t.Fatal(err)
			}
			result, err := f.s.Complete(t.Context(), request)
			if err != nil {
				t.Fatal("图片证据降级不能变成解析失败或重试", err)
			}
			facts, warnings, err := analysis.ParseFacts([]byte(result.Text), profile, originals)
			if err != nil || len(upstream.snapshot()) != 1 || len(result.Calls) != 1 {
				t.Fatal("真实客户端没有保持一次请求和最终核对", facts, warnings, err)
			}
			if test.invalid {
				if facts.Musts[0].Status != analysis.ReqUnknown || facts.Checks[0].Status != analysis.CheckUnknown || len(facts.ExtraFindings) != 0 || len(warnings) != 3 {
					t.Fatal("最终保存前核对丢失了实际图片数", facts, warnings)
				}
			} else if facts.Musts[0].Status != analysis.ReqMet || facts.Checks[0].Status != analysis.CheckOK || len(facts.ExtraFindings) != 1 || len(warnings) != 0 {
				t.Fatal("实际已提供的图片证据被误删", facts, warnings)
			}
			score := analysis.Score(analysis.ScoreInput{Profile: profile, Facts: facts, Price: modelPointer(int64(65000)), Fair: &analysis.FairPrice{Cents: modelPointer(int64(100000)), Sample: 8}})
			if test.invalid && (score.Score == nil || *score.Score > 89) {
				t.Fatal("不存在的图片证据突破了严格检查项封顶", score)
			}
			if modelScalar[int](t, f, `SELECT count(*) FROM ai_calls WHERE ok=1 AND input_tokens=2 AND output_tokens=3`) != 1 {
				t.Fatal("证据警告改变了实际用量记账")
			}
		})
	}
}

func TestProviderTrialReportsImageReferenceBeyondSample(t *testing.T) {
	f := newModelFixture(t)
	upstream := trialServer(t, func(w http.ResponseWriter, _ *http.Request, _ int, request map[string]any) {
		if trialIsTitle(request) {
			trialReply(w, trialTitleJSON, "stop", trialUsage(2, 3, nil))
			return
		}
		body := strings.ReplaceAll(trialFactsJSON, `"source":"text"`, `"source":"image"`)
		for _, quote := range []string{"功能正常", "带充电器", "外壳有细微划痕"} {
			body = strings.ReplaceAll(body, `"evidence":"`+quote+`"`, `"evidence":"第2张图：`+quote+`"`)
		}
		trialReply(w, body, "stop", trialUsage(2, 3, nil))
	})
	provider := trialProvider(t, f, upstream)
	report, err := f.s.TestProvider(t.Context(), provider.ID)
	if err != nil || !report.Passed || len(report.Vision.Warnings) != 3 || len(upstream.snapshot()) != 2 {
		t.Fatal("试分析没有用实际发送的一张示例图核对引用，或警告触发了重试", report, err)
	}
	for _, warning := range report.Vision.Warnings {
		if warning.Code != "analysis.image_evidence_missing" {
			t.Fatal("试分析没有保留图片专用警告", warning)
		}
	}
}
