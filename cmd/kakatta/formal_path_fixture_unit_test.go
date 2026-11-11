package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/prompts"
	modelservice "github.com/Seaony/Kakatta/internal/analysis/service"
)

func TestFormalFixtureSupportsRealProviderTrialAndProfileGeneration(t *testing.T) {
	f := newFormalFixtures(t.Context())
	t.Cleanup(f.close)
	g := graphFixture(t)
	key, text := formalFixtureKey, "formal-text"
	provider, err := g.models.CreateProvider(t.Context(), modelservice.ProviderInput{
		Name: "本地正式流程模型", BaseURL: f.metadata.ModelBaseURL, VisionModel: "formal-vision", TextModel: &text, APIKey: &key, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := g.models.TestProvider(t.Context(), provider.Provider.ID)
	if err != nil || !report.Passed || report.Text.State != modelservice.TrialPassed || report.Vision.State != modelservice.TrialPassed {
		t.Fatalf("正式提供商测试没有通过：%+v，%v", report, err)
	}
	draft, err := g.models.GenerateProfile(t.Context(), modelservice.GenerateProfileInput{Keyword: "MacBook Pro", Requirement: "自用，核对成色与功能"})
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Profile.Specs) != 1 || draft.Profile.Specs[0].Name != "型号" || len(draft.Profile.Specs[0].Values) != 1 || draft.Profile.Specs[0].Values[0] != "MacBook Pro" {
		t.Fatal("正式画像生成没有保留约定的型号", draft.Profile.Specs)
	}
	state := f.state()
	counts := state["model_counts"].(map[string]int64)
	if counts["title_screen"] != 1 || counts["analysis"] != 1 || counts["profile"] != 1 {
		t.Fatal("正式模型请求用途不完整", counts)
	}
	encoded, err := json.Marshal(state)
	if err != nil || bytes.Contains(encoded, []byte(formalFixtureKey)) {
		t.Fatal("夹具状态包含密钥或无法序列化")
	}
}

func TestFormalFixtureCompatibilityAndAnalysisOnlyTimeout(t *testing.T) {
	f := newFormalFixtures(t.Context())
	t.Cleanup(f.close)
	g := graphFixture(t)
	snapshot, err := g.models.PromptSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	profile := analysis.DefaultProfile()
	profile.Musts = []analysis.Requirement{{ID: "m1", Text: "功能正常", Source: analysis.SourceUser, Rev: 1}}
	title, err := prompts.BuildTitle(snapshot, nil, profile, []prompts.TitleSource{{Title: "MacBook Pro 功能正常"}})
	if err != nil {
		t.Fatal(err)
	}
	description := "功能正常"
	vision, _, err := prompts.BuildAnalysis(snapshot, nil, profile, prompts.ProductSource{Title: "MacBook Pro", Description: &description}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct {
		purpose string
		user    string
		check   func(string) error
	}{{"title_screen", title.User, title.Validate}, {"analysis", vision.User, vision.Validate}} {
		purpose, _, reply, err := formalFixtureModelReply("", request.user)
		if err != nil || purpose != request.purpose {
			t.Fatal("缺省结构化输出模式没有识别正式用途", purpose, err)
		}
		encoded, err := json.Marshal(reply)
		if err != nil || request.check(string(encoded)) != nil {
			t.Fatal("缺省结构化输出模式返回无效事实", err)
		}
	}
	setMode := func(mode string) {
		r := httptest.NewRequest(http.MethodPost, "/control", strings.NewReader(`{"action":"model","mode":"`+mode+`"}`))
		w := httptest.NewRecorder()
		f.handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatal("模型控制失败", w.Code)
		}
	}
	call := func(ctx context.Context, user string) (int, error) {
		body, err := json.Marshal(map[string]any{"model": "formal-test", "messages": []map[string]string{{"role": "user", "content": user}}})
		if err != nil {
			return 0, err
		}
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, f.metadata.ModelBaseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return 0, err
		}
		r.Header.Set("Authorization", "Bearer "+formalFixtureKey)
		response, err := (&http.Client{Timeout: 3 * time.Second}).Do(r)
		if err != nil {
			return 0, err
		}
		defer response.Body.Close()
		_, err = io.Copy(io.Discard, response.Body)
		return response.StatusCode, err
	}
	setMode("timeout")
	if status, err := call(t.Context(), title.User); status != http.StatusOK || err != nil {
		t.Fatal("超时模式影响了标题请求", status, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := call(ctx, vision.User); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("详情模型请求没有等待客户端取消", err)
	}
	setMode("ok")
	if status, err := call(t.Context(), vision.User); status != http.StatusOK || err != nil {
		t.Fatal("恢复后的详情模型请求失败", status, err)
	}
}
