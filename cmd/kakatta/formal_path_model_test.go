package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
)

func (f *formalFixtures) serveModel(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Model    string `json:"model"`
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Format struct {
			Schema struct {
				Name string `json:"name"`
			} `json:"json_schema"`
		} `json:"response_format"`
	}
	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&request) != nil || len(request.Messages) == 0 {
		http.Error(w, "本地模型请求无效", http.StatusBadRequest)
		return
	}
	var user string
	if json.Unmarshal(request.Messages[len(request.Messages)-1].Content, &user) != nil {
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(request.Messages[len(request.Messages)-1].Content, &parts) != nil {
			http.Error(w, "本地模型消息无效", http.StatusBadRequest)
			return
		}
		for _, part := range parts {
			if part.Text != "" {
				user = part.Text
			}
		}
	}
	purpose, product, reply, err := formalFixtureModelReply(request.Format.Schema.Name, user)
	f.mu.Lock()
	index := len(f.modelRequests)
	f.modelRequests = append(f.modelRequests, formalModelReceipt{Purpose: purpose, Model: request.Model, StartedAt: time.Now().UnixMilli()})
	if product != nil {
		f.products = append(f.products, product)
	}
	timeout := f.modelTimeout && purpose == "analysis"
	f.mu.Unlock()
	status := 0
	defer func() {
		f.mu.Lock()
		f.modelRequests[index].Status = status
		f.modelRequests[index].EndedAt = time.Now().UnixMilli()
		f.mu.Unlock()
	}()
	if r.Header.Get("Authorization") != "Bearer "+formalFixtureKey {
		status = http.StatusUnauthorized
		formalFixtureJSON(w, status, map[string]any{"error": map[string]string{"message": "incorrect API key", "type": "invalid_api_key"}})
		return
	}
	if request.Model == "" || strings.Contains(request.Model, "invalid") || strings.Contains(request.Model, "missing") {
		status = http.StatusNotFound
		formalFixtureJSON(w, status, map[string]any{"error": map[string]string{"message": "model not found"}})
		return
	}
	if err != nil {
		status = http.StatusBadRequest
		http.Error(w, err.Error(), status)
		return
	}
	if timeout {
		select {
		case <-r.Context().Done():
		case <-f.ctx.Done():
		}
		return
	}
	content := "OK"
	if purpose != "probe" && purpose != "market" {
		encoded, encodeErr := json.Marshal(reply)
		if encodeErr != nil {
			status = http.StatusInternalServerError
			http.Error(w, "本地模型回应生成失败", status)
			return
		}
		content = string(encoded)
	} else if purpose == "market" {
		content = "样本来自挂牌价，仍需核对实物与交易条件。"
	}
	status = http.StatusOK
	formalFixtureJSON(w, status, map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 10}})
}

func formalFixtureModelReply(schema, user string) (string, map[string]any, any, error) {
	purpose := map[string]string{"title_screen": "title_screen", "listing_facts": "analysis", "task_profile": "profile"}[schema]
	if purpose == "" {
		switch {
		case user == "Reply with OK":
			purpose = "probe"
		case strings.Contains(user, "我想买："):
			purpose = "profile"
		case strings.Contains(user, "## 商品（搜索列表）"):
			purpose = "title_screen"
		case strings.Contains(user, "## 商品信息"):
			purpose = "analysis"
		default:
			purpose = "market"
		}
	}
	if purpose == "profile" {
		return purpose, nil, map[string]any{
			"summary": "会帮你找：自用 MacBook Pro，核对描述与实际成色",
			"specs":   []any{map[string]any{"name": "型号", "options": []string{"MacBook Pro"}, "values": []string{"MacBook Pro"}, "source": "user"}},
			"musts":   []any{}, "bonuses": []any{}, "risks": []any{}, "allowed_kinds": []string{"sale"}, "budget_min": nil, "budget_max": nil, "keyword": "MacBook Pro", "exclude_words": []string{},
		}, nil
	}
	if purpose == "probe" || purpose == "market" {
		return purpose, nil, nil, nil
	}
	parts := strings.Split(user, "```json\n")
	if len(parts) != 3 {
		return purpose, nil, nil, fmt.Errorf("本地模型缺少清单或商品原文")
	}
	var profile struct {
		Specs []struct {
			Name            string `json:"name"`
			Options, Values []string
		} `json:"specs"`
		Checks []struct {
			Key string `json:"key"`
		}
	}
	if json.Unmarshal([]byte(strings.SplitN(parts[1], "\n```", 2)[0]), &profile) != nil {
		return purpose, nil, nil, fmt.Errorf("本地模型清单无效")
	}
	specsFor := func(title string) []map[string]any {
		specs := []map[string]any{}
		for _, spec := range profile.Specs {
			var value any
			for _, option := range spec.Options {
				if strings.Contains(title, option) {
					value = option
					break
				}
			}
			specs = append(specs, map[string]any{"name": spec.Name, "value": value})
		}
		return specs
	}
	source := strings.SplitN(parts[2], "\n```", 2)[0]
	if purpose == "title_screen" {
		var batch []struct {
			No    int    `json:"no"`
			Title string `json:"title"`
		}
		if json.Unmarshal([]byte(source), &batch) != nil || len(batch) == 0 {
			return purpose, nil, nil, fmt.Errorf("本地模型标题列表无效")
		}
		items := []map[string]any{}
		for _, item := range batch {
			kind := analysis.KeywordKind(item.Title)
			if kind == "" {
				kind = analysis.KindSale
			}
			items = append(items, map[string]any{"no": item.No, "kind": kind, "specs": specsFor(item.Title), "brand_new": false, "verdict": "pass", "reason": ""})
		}
		return purpose, nil, map[string]any{"items": items}, nil
	}
	var product map[string]any
	if json.Unmarshal([]byte(source), &product) != nil {
		return purpose, nil, nil, fmt.Errorf("本地模型商品原文无效")
	}
	var requirements struct{ Musts, Bonuses []struct{ ID, Text string } }
	if json.Unmarshal([]byte(strings.SplitN(parts[1], "\n```", 2)[0]), &requirements) != nil {
		return purpose, nil, nil, fmt.Errorf("本地模型必需与加分项无效")
	}
	title, _ := product["title"].(string)
	description, _ := product["description"].(string)
	withEvidence := func(id, text string) map[string]any {
		fact := map[string]any{"id": id, "status": "unknown", "source": nil, "evidence": nil}
		if strings.Contains(title+"\n"+description, text) {
			fact["status"], fact["source"], fact["evidence"] = "met", "text", text
		}
		return fact
	}
	musts, bonuses, checks := []map[string]any{}, []map[string]any{}, []map[string]any{}
	for _, item := range requirements.Musts {
		musts = append(musts, withEvidence(item.ID, item.Text))
	}
	for _, item := range requirements.Bonuses {
		bonuses = append(bonuses, withEvidence(item.ID, item.Text))
	}
	for _, check := range profile.Checks {
		fact := map[string]any{"key": check.Key, "status": "unknown", "source": nil, "evidence": nil}
		if check.Key == "cosmetic_damage" && strings.Contains(description, "外壳有细微划痕") {
			fact["status"], fact["source"], fact["evidence"] = "risk", "text", "外壳有细微划痕"
		}
		checks = append(checks, fact)
	}
	return purpose, product, map[string]any{"specs": specsFor(title), "brand_new": false, "musts": musts, "bonuses": bonuses, "checks": checks, "seller_signals": []any{}, "price_note": nil, "extra_findings": []any{}, "adjust": map[string]any{"value": 0, "reason": ""}, "summary": "已核对描述，建议确认实物成色、功能与配件。"}, nil
}
