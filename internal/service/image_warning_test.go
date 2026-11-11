package service

import (
	"context"
	"reflect"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/locales"
)

func TestImageEvidenceWarningSurvivesRunSaveAndRead(t *testing.T) {
	s, run, now := detailTestRun(t, 1)
	detailAI(t, s, run, now)
	const response = `{"checks":[],"musts":[{"id":"m1","status":"met","source":"image","evidence":"第1张图：国行"}],"adjust":{"value":0},"summary":"核对证据"}`
	calls := 0
	s.models = detailModelsFunc(func(_ context.Context, request client.Request) (client.Result, error) {
		calls++
		if len(request.Images) != 0 {
			t.Fatal("样例应明确没有提供图片")
		}
		if err := request.Validate(response); err != nil {
			t.Fatal("缺少图片证据不能触发额外模型请求", err)
		}
		return client.Result{Text: response}, nil
	})
	if err := s.runDetail(t.Context(), run); err != nil {
		t.Fatal("证据警告未能沿正式收尾保存", err)
	}
	finding, err := s.GetFinding(t.Context(), 1, FindingQuery{View: "all"})
	if err != nil {
		t.Fatal(err)
	}
	want := []locales.Message{{Code: "analysis.image_evidence_missing", Args: map[string]any{"item": "m1"}}}
	if calls != 1 || finding.Decision != analysis.DecisionScored || finding.Analysis == nil || finding.Analysis.Musts[0].Status != analysis.ReqUnknown || !reflect.DeepEqual(finding.AnalysisWarnings, want) {
		t.Fatalf("最终事实或可见警告丢失：%+v，调用%d", finding, calls)
	}
	var raw string
	if err := s.db.QueryRowContext(t.Context(), `SELECT analysis_json FROM findings WHERE id=1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	_, warnings, err := decodeFindingFacts(raw)
	if err != nil || !reflect.DeepEqual(warnings, want) {
		t.Fatal("重读持久事实没有保留图片警告", warnings, err)
	}
	for _, invalid := range []locales.Message{
		{Code: "analysis.image_evidence_missing", Args: map[string]any{"item": 1}},
		{Code: "analysis.image_evidence_missing", Args: map[string]any{"item": "m1", "extra": "不能保存"}},
		{Code: "model.injected_warning", Args: map[string]any{"item": "m1"}},
	} {
		if _, err := encodeFindingFacts(*finding.Analysis, []locales.Message{invalid}); err == nil {
			t.Fatalf("放行图片警告时吞掉非法代码或参数：%+v", invalid)
		}
	}
}
