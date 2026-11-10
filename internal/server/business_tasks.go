package server

import (
	"net/http"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/service"
)

func (b *businessHandler) registerTaskRoutes() {
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"GET /api/v1/tasks", b.listTasks},
		{"GET /api/v1/tasks/defaults", b.taskDefaults},
		{"POST /api/v1/tasks", b.createTask},
		{"GET /api/v1/tasks/{task_id}", b.getTask},
		{"PATCH /api/v1/tasks/{task_id}", b.updateTask},
		{"DELETE /api/v1/tasks/{task_id}", b.deleteTask},
		{"GET /api/v1/tasks/{task_id}/runtime", b.taskRuntime},
		{"POST /api/v1/tasks/{task_id}/run", b.runTask},
		{"POST /api/v1/tasks/{task_id}/cancel", b.cancelTask},
		{"POST /api/v1/tasks/{task_id}/no-good-hint/dismiss", b.dismissNoGoodHint},
		{"POST /api/v1/tasks/{task_id}/apply-exclusions", b.applyTaskExclusions},
		{"GET /api/v1/tasks/{task_id}/market", b.taskMarket},
		{"GET /api/v1/tasks/{task_id}/market/samples", b.taskMarketSamples},
		{"GET /api/v1/risk-dictionary", b.riskDictionary},
		{"GET /api/v1/regions", b.regions},
	} {
		b.register(route.pattern, route.handler)
	}
}

func taskRequestID(r *http.Request) (int64, error) {
	if _, err := queryValues(r); err != nil {
		return 0, err
	}
	return pathID(r, "task_id")
}

func taskReadID(r *http.Request) (int64, error) {
	id, err := taskRequestID(r)
	if err == nil {
		err = noBody(r)
	}
	return id, err
}

func (b *businessHandler) listTasks(w http.ResponseWriter, r *http.Request) {
	values, err := queryValues(r, "q", "enabled", "limit", "cursor")
	if err == nil {
		err = noBody(r)
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	list, err := queryList(values)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	enabled, err := queryBool(values, "enabled")
	if err != nil {
		b.failure(w, r, err)
		return
	}
	result, err := b.service.ListTasks(r.Context(), service.TaskQuery{ListQuery: list, Q: values.Get("q"), Enabled: enabled})
	b.reply(w, r, http.StatusOK, result, err)
}

func (b *businessHandler) taskDefaults(w http.ResponseWriter, r *http.Request) {
	_, err := queryValues(r)
	if err == nil {
		err = noBody(r)
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	result, err := b.service.TaskDefaults(r.Context())
	b.reply(w, r, http.StatusOK, result, err)
}

func (b *businessHandler) createTask(w http.ResponseWriter, r *http.Request) {
	if _, err := queryValues(r); err != nil {
		b.failure(w, r, err)
		return
	}
	patch, err := readPatch(w, r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	result, err := b.service.CreateTask(r.Context(), service.TaskPatch(patch))
	b.reply(w, r, http.StatusCreated, result, err)
}

func (b *businessHandler) getTask(w http.ResponseWriter, r *http.Request) {
	id, err := taskReadID(r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	result, err := b.service.GetTask(r.Context(), id)
	b.reply(w, r, http.StatusOK, result, err)
}

func (b *businessHandler) updateTask(w http.ResponseWriter, r *http.Request) {
	id, err := taskRequestID(r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	patch, err := readPatch(w, r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	result, err := b.service.UpdateTask(r.Context(), id, service.TaskPatch(patch))
	b.reply(w, r, http.StatusOK, result, err)
}

func (b *businessHandler) deleteTask(w http.ResponseWriter, r *http.Request) {
	id, err := taskReadID(r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	err = b.service.DeleteTask(r.Context(), id)
	b.reply(w, r, http.StatusNoContent, nil, err)
}

func (b *businessHandler) taskRuntime(w http.ResponseWriter, r *http.Request) {
	id, err := taskReadID(r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	result, err := b.service.GetTaskRuntime(r.Context(), id)
	b.reply(w, r, http.StatusOK, result, err)
}

func (b *businessHandler) runTask(w http.ResponseWriter, r *http.Request) {
	id, err := taskReadID(r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	result, err := b.service.RunNow(r.Context(), id)
	b.reply(w, r, http.StatusAccepted, result, err)
}

func (b *businessHandler) cancelTask(w http.ResponseWriter, r *http.Request) {
	id, err := taskReadID(r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	err = b.service.CancelRun(r.Context(), id)
	b.reply(w, r, http.StatusNoContent, nil, err)
}

func (b *businessHandler) dismissNoGoodHint(w http.ResponseWriter, r *http.Request) {
	id, err := taskReadID(r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	err = b.service.DismissNoGoodHint(r.Context(), id)
	b.reply(w, r, http.StatusNoContent, nil, err)
}

func (b *businessHandler) applyTaskExclusions(w http.ResponseWriter, r *http.Request) {
	id, err := taskRequestID(r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	var body struct {
		Words *[]string `json:"added_exclude_words"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		b.failure(w, r, err)
		return
	}
	if body.Words == nil {
		b.failure(w, r, invalidParameter("added_exclude_words"))
		return
	}
	count, err := b.service.HideAddedExcludes(r.Context(), id, *body.Words)
	b.reply(w, r, http.StatusOK, service.MutationResult{Applied: count}, err)
}

func (b *businessHandler) taskMarket(w http.ResponseWriter, r *http.Request) {
	id, err := taskReadID(r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	result, err := b.service.GetMarket(r.Context(), id)
	b.reply(w, r, http.StatusOK, result, err)
}

func (b *businessHandler) taskMarketSamples(w http.ResponseWriter, r *http.Request) {
	values, err := queryValues(r, "config_key", "brand_new", "limit", "cursor")
	if err == nil {
		err = noBody(r)
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	id, err := pathID(r, "task_id")
	if err != nil {
		b.failure(w, r, err)
		return
	}
	list, err := queryList(values)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	brandNew, err := queryBool(values, "brand_new")
	if err != nil {
		b.failure(w, r, err)
		return
	}
	result, err := b.service.ListMarketSamples(r.Context(), id, service.MarketSampleQuery{ListQuery: list, ConfigKey: values.Get("config_key"), BrandNew: brandNew})
	b.reply(w, r, http.StatusOK, result, err)
}

func (b *businessHandler) riskDictionary(w http.ResponseWriter, r *http.Request) {
	_, err := queryValues(r)
	if err == nil {
		err = noBody(r)
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	b.reply(w, r, http.StatusOK, analysis.RiskDictionary(), nil)
}

func (b *businessHandler) regions(w http.ResponseWriter, r *http.Request) {
	values, err := queryValues(r, "parent")
	if err == nil {
		err = noBody(r)
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	result, err := b.service.ListRegions(r.Context(), values.Get("parent"))
	b.reply(w, r, http.StatusOK, result, err)
}
