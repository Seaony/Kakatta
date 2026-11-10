package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"slices"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	modelservice "github.com/Seaony/Kakatta/internal/analysis/service"
	"github.com/Seaony/Kakatta/internal/service"
)

type modelHandler struct {
	*businessHandler
	models modelservice.API
}

func modelRoutes(server *Server, models modelservice.API) []businessRoute {
	b := &modelHandler{businessHandler: &businessHandler{server: server}, models: models}
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"GET /api/v1/ai/providers", b.listProviders},
		{"POST /api/v1/ai/providers", b.createProvider},
		{"GET /api/v1/ai/providers/{provider_id}", b.getProvider},
		{"PUT /api/v1/ai/providers/{provider_id}", b.updateProvider},
		{"DELETE /api/v1/ai/providers/{provider_id}", b.deleteProvider},
		{"PUT /api/v1/ai/providers/{provider_id}/enabled", b.setProviderEnabled},
		{"PUT /api/v1/ai/providers/order", b.reorderProviders},
		{"POST /api/v1/ai/providers/{provider_id}/test", b.testProvider},
		{"GET /api/v1/prompts", b.listPrompts},
		{"PUT /api/v1/prompts/{kind}", b.savePrompt},
		{"DELETE /api/v1/prompts/{kind}", b.restorePrompt},
		{"POST /api/v1/ai/profile", b.generateProfile},
		{"GET /api/v1/ai/usage", b.modelUsage},
		{"GET /api/v1/ai/budget", b.modelBudget},
	} {
		b.register(route.pattern, route.handler)
	}
	return b.routes
}

func (s *Server) RegisterModels(models modelservice.API) error {
	if models == nil || reflect.ValueOf(models).Kind() == reflect.Pointer && reflect.ValueOf(models).IsNil() {
		return errors.New("模型服务不能为空")
	}
	probe := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/api/v1/ai/providers"}}
	if _, pattern := s.mux.Handler(probe); pattern != "" && pattern != "/" {
		return errors.New("模型接口已经注册")
	}
	for _, route := range modelRoutes(s, models) {
		s.RegisterProtected(route.pattern, route.handler)
	}
	return nil
}

func readModelJSON(w http.ResponseWriter, r *http.Request, target any, required []string, nullable ...string) error {
	raw, err := readJSONObject(w, r)
	if err != nil {
		return err
	}
	if !exactFields(raw, target) {
		return &requestError{status: http.StatusBadRequest, code: "http.invalid_json"}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return &requestError{status: http.StatusBadRequest, code: "http.invalid_json"}
	}
	for _, key := range required {
		value, present := fields[key]
		if !present || bytes.Equal(bytes.TrimSpace(value), []byte("null")) && !slices.Contains(nullable, key) {
			return invalidParameter(key)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var invalid *json.UnmarshalTypeError
		if errors.As(err, &invalid) && invalid.Field != "" {
			return invalidParameter(invalid.Field)
		}
		return &requestError{status: http.StatusBadRequest, code: "http.invalid_json"}
	}
	if _, generate := target.(*modelservice.GenerateProfileInput); generate {
		if err := service.ValidateProfileDraftJSON(fields["current"]); err != nil {
			return invalidParameter("current")
		}
	}
	return nil
}

func readProviderInput(w http.ResponseWriter, r *http.Request) (modelservice.ProviderInput, error) {
	var input modelservice.ProviderInput
	err := readModelJSON(w, r, &input, []string{"name", "preset", "base_url", "vision_model", "text_model", "enabled"}, "preset", "text_model")
	return input, err
}

func modelRequest(r *http.Request, body bool) error {
	if _, err := queryValues(r); err != nil {
		return err
	}
	if !body {
		return noBody(r)
	}
	return nil
}

func modelRequestID(r *http.Request, body bool) (int64, error) {
	if err := modelRequest(r, body); err != nil {
		return 0, err
	}
	return pathID(r, "provider_id")
}

func (b *modelHandler) listProviders(w http.ResponseWriter, r *http.Request) {
	if err := modelRequest(r, false); err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.models.ListProviders(r.Context())
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *modelHandler) createProvider(w http.ResponseWriter, r *http.Request) {
	if err := modelRequest(r, true); err != nil {
		b.failure(w, r, err)
		return
	}
	input, err := readProviderInput(w, r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.models.CreateProvider(r.Context(), input)
	b.reply(w, r, http.StatusCreated, value, err)
}

func (b *modelHandler) getProvider(w http.ResponseWriter, r *http.Request) {
	id, err := modelRequestID(r, false)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.models.GetProvider(r.Context(), id)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *modelHandler) updateProvider(w http.ResponseWriter, r *http.Request) {
	id, err := modelRequestID(r, true)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	input, err := readProviderInput(w, r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.models.UpdateProvider(r.Context(), id, input)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *modelHandler) deleteProvider(w http.ResponseWriter, r *http.Request) {
	id, err := modelRequestID(r, false)
	if err == nil {
		err = b.models.DeleteProvider(r.Context(), id)
	}
	b.reply(w, r, http.StatusNoContent, nil, err)
}

func (b *modelHandler) setProviderEnabled(w http.ResponseWriter, r *http.Request) {
	id, err := modelRequestID(r, true)
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if err == nil {
		err = readModelJSON(w, r, &input, []string{"enabled"})
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.models.SetProviderEnabled(r.Context(), id, input.Enabled)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *modelHandler) reorderProviders(w http.ResponseWriter, r *http.Request) {
	err := modelRequest(r, true)
	var input struct {
		IDs []int64 `json:"ids"`
	}
	if err == nil {
		err = readModelJSON(w, r, &input, []string{"ids"})
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.models.ReorderProviders(r.Context(), input.IDs)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *modelHandler) testProvider(w http.ResponseWriter, r *http.Request) {
	id, err := modelRequestID(r, false)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.models.TestProvider(r.Context(), id)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *modelHandler) listPrompts(w http.ResponseWriter, r *http.Request) {
	if err := modelRequest(r, false); err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.models.ListPrompts(r.Context())
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *modelHandler) savePrompt(w http.ResponseWriter, r *http.Request) {
	err := modelRequest(r, true)
	kind, pathErr := pathText(r, "kind")
	if err == nil {
		err = pathErr
	}
	var input struct {
		Content string `json:"content"`
	}
	if err == nil {
		err = readModelJSON(w, r, &input, []string{"content"})
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.models.SavePrompt(r.Context(), client.Kind(kind), input.Content)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *modelHandler) restorePrompt(w http.ResponseWriter, r *http.Request) {
	err := modelRequest(r, false)
	kind, pathErr := pathText(r, "kind")
	if err == nil {
		err = pathErr
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.models.RestorePrompt(r.Context(), client.Kind(kind))
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *modelHandler) generateProfile(w http.ResponseWriter, r *http.Request) {
	err := modelRequest(r, true)
	var input modelservice.GenerateProfileInput
	if err == nil {
		err = readModelJSON(w, r, &input, []string{"keyword", "requirement", "current"})
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.models.GenerateProfile(r.Context(), input)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *modelHandler) modelUsage(w http.ResponseWriter, r *http.Request) {
	values, err := queryValues(r, "window", "task_id", "purpose")
	if err == nil {
		err = noBody(r)
	}
	query := modelservice.UsageQuery{Window: modelservice.UsageToday}
	if err == nil && values.Has("window") {
		query.Window = modelservice.UsageWindow(values.Get("window"))
		if !slices.Contains([]modelservice.UsageWindow{modelservice.UsageToday, modelservice.Usage7Days, modelservice.Usage30Days}, query.Window) {
			err = invalidParameter("window")
		}
	}
	if err == nil {
		query.TaskID, err = queryID(values, "task_id")
	}
	if err == nil && values.Has("purpose") {
		purpose := values.Get("purpose")
		if !slices.Contains([]string{"title_screen", "analysis", "profile", "market", "test", "probe"}, purpose) {
			err = invalidParameter("purpose")
		}
		query.Purpose = &purpose
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.models.Usage(r.Context(), query)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *modelHandler) modelBudget(w http.ResponseWriter, r *http.Request) {
	if err := modelRequest(r, false); err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.models.BudgetStatus(r.Context())
	b.reply(w, r, http.StatusOK, value, err)
}
