package server

import (
	"encoding/json"
	"net/http"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/service"
)

func (b *businessHandler) registerResultRoutes() {
	for pattern, handler := range map[string]http.HandlerFunc{
		"GET /api/v1/findings":                              b.resultFindings,
		"GET /api/v1/findings/{finding_id}":                 b.resultFinding,
		"PATCH /api/v1/findings/{finding_id}":               b.resultSetRead,
		"DELETE /api/v1/findings/{finding_id}":              b.resultDeleteFinding,
		"DELETE /api/v1/tasks/{task_id}/findings":           b.resultDeleteTaskFindings,
		"POST /api/v1/findings/reanalysis-preview":          b.resultReanalysisPreview,
		"POST /api/v1/findings/batch":                       b.resultBatch,
		"POST /api/v1/findings/{finding_id}/reanalyze":      b.resultReanalyzeOne,
		"PUT /api/v1/findings/{finding_id}/overrides":       b.resultOverrides,
		"PUT /api/v1/tasks/{task_id}/items/{item_id}/specs": b.resultItemSpecs,
		"POST /api/v1/findings/{finding_id}/extra-check":    b.resultExtraCheck,
		"GET /api/v1/listings":                              b.resultListings,
		"GET /api/v1/listings/{item_id}":                    b.resultListing,
		"PATCH /api/v1/listings/{item_id}":                  b.resultUpdateListing,
		"GET /api/v1/listings/{item_id}/prices":             b.resultPrices,
		"GET /api/v1/blocked-sellers":                       b.resultBlockedSellers,
		"PUT /api/v1/blocked-sellers/{seller_id}":           b.resultBlockSeller,
		"DELETE /api/v1/blocked-sellers/{seller_id}":        b.resultUnblockSeller,
		"GET /api/v1/overview":                              b.resultOverview,
		"GET /api/v1/runs":                                  b.resultRuns,
		"GET /api/v1/runs/{run_id}":                         b.resultRun,
		"GET /api/v1/runs/{run_id}/steps":                   b.resultRunSteps,
		"GET /api/v1/runs/overview":                         b.resultRunOverview,
		"GET /api/v1/system/events":                         b.resultSystemEvents,
		"GET /api/v1/system/status":                         b.resultSystemStatus,
		"POST /api/v1/maintenance/clear-image-cache":        b.resultClearImages,
		"POST /api/v1/maintenance/clear-data":               b.resultClearData,
	} {
		b.register(pattern, handler)
	}
}

func resultFindingQuery(r *http.Request, detail bool) (service.FindingQuery, error) {
	allowed := []string{"task_id", "view", "decision", "price_drop", "over_budget", "q", "sort"}
	if !detail {
		allowed = append(allowed, "limit", "cursor")
	}
	values, err := queryValues(r, allowed...)
	if err != nil {
		return service.FindingQuery{}, err
	}
	query := service.FindingQuery{View: values.Get("view"), Q: values.Get("q"), Sort: values.Get("sort")}
	for _, key := range []string{"view", "sort"} {
		if values.Has(key) && values.Get(key) == "" {
			return service.FindingQuery{}, invalidParameter(key)
		}
	}
	if !detail {
		if query.ListQuery, err = queryList(values); err != nil {
			return service.FindingQuery{}, err
		}
	}
	if query.TaskID, err = queryID(values, "task_id"); err != nil {
		return service.FindingQuery{}, err
	}
	if query.PriceDrop, err = queryBool(values, "price_drop"); err != nil {
		return service.FindingQuery{}, err
	}
	if query.OverBudget, err = queryBool(values, "over_budget"); err != nil {
		return service.FindingQuery{}, err
	}
	if _, exists := values["decision"]; exists {
		decision := analysis.Decision(values.Get("decision"))
		query.Decision = &decision
	}
	return query, nil
}

func (b *businessHandler) resultFindings(w http.ResponseWriter, r *http.Request) {
	query, err := resultFindingQuery(r, false)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.ListFindings(r.Context(), query)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) resultFinding(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "finding_id")
	if err != nil {
		b.failure(w, r, err)
		return
	}
	query, err := resultFindingQuery(r, true)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.GetFinding(r.Context(), id, query)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) resultSetRead(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "finding_id")
	if err == nil {
		_, err = queryValues(r)
	}
	var body struct {
		IsRead *bool `json:"is_read"`
	}
	if err == nil {
		err = decodeJSON(w, r, &body)
	}
	if err == nil && body.IsRead == nil {
		err = invalidParameter("is_read")
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	if _, err := b.service.SetRead(r.Context(), []int64{id}, *body.IsRead); err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.GetFinding(r.Context(), id, service.FindingQuery{})
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) resultDeleteFinding(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "finding_id")
	if err == nil {
		_, err = queryValues(r)
	}
	if err == nil {
		err = noBody(r)
	}
	if err == nil {
		_, err = b.service.DeleteFindings(r.Context(), []int64{id})
	}
	b.reply(w, r, http.StatusNoContent, nil, err)
}

func (b *businessHandler) resultDeleteTaskFindings(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "task_id")
	if err == nil {
		_, err = queryValues(r)
	}
	if err == nil {
		err = noBody(r)
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	count, err := b.service.DeleteTaskFindings(r.Context(), id)
	b.reply(w, r, http.StatusOK, service.MutationResult{Applied: count}, err)
}

func (b *businessHandler) resultReanalysisPreview(w http.ResponseWriter, r *http.Request) {
	_, err := queryValues(r)
	var body struct {
		IDs []int64 `json:"ids"`
	}
	if err == nil {
		err = decodeJSON(w, r, &body)
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.PreviewReanalysis(r.Context(), body.IDs)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) resultBatch(w http.ResponseWriter, r *http.Request) {
	_, err := queryValues(r)
	var body struct {
		Action string  `json:"action"`
		IDs    []int64 `json:"ids"`
	}
	if err == nil {
		err = decodeJSON(w, r, &body)
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	switch body.Action {
	case "delete":
		value, err := b.service.DeleteFindings(r.Context(), body.IDs)
		b.reply(w, r, http.StatusOK, value, err)
	case "dismiss":
		value, err := b.service.SetDismissed(r.Context(), body.IDs, true)
		b.reply(w, r, http.StatusOK, value, err)
	case "reanalyze":
		value, err := b.service.Reanalyze(r.Context(), body.IDs)
		b.reply(w, r, http.StatusAccepted, value, err)
	default:
		b.failure(w, r, invalidParameter("action"))
	}
}

func (b *businessHandler) resultReanalyzeOne(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "finding_id")
	if err == nil {
		_, err = queryValues(r)
	}
	if err == nil {
		err = noBody(r)
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.ReanalyzeOne(r.Context(), id)
	b.reply(w, r, http.StatusAccepted, value, err)
}

func (b *businessHandler) resultOverrides(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "finding_id")
	if err == nil {
		_, err = queryValues(r)
	}
	var body analysis.UserOverrides
	if err == nil {
		err = decodeJSON(w, r, &body)
	}
	if err == nil && body.Risks == nil {
		err = invalidParameter("risks")
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.SetRiskOverrides(r.Context(), id, body)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) resultItemSpecs(w http.ResponseWriter, r *http.Request) {
	taskID, err := pathID(r, "task_id")
	var itemID string
	if err == nil {
		itemID, err = pathText(r, "item_id")
	}
	if err == nil {
		_, err = queryValues(r)
	}
	var body struct {
		Specs    []analysis.SpecValue `json:"specs"`
		BrandNew json.RawMessage      `json:"brand_new"`
	}
	if err == nil {
		err = decodeJSON(w, r, &body)
	}
	if err == nil && body.Specs == nil {
		err = invalidParameter("specs")
	}
	if err == nil && body.BrandNew == nil {
		err = invalidParameter("brand_new")
	}
	var brandNew *bool
	if err == nil && json.Unmarshal(body.BrandNew, &brandNew) != nil {
		err = invalidParameter("brand_new")
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.SetItemSpecs(r.Context(), taskID, itemID, body.Specs, brandNew)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) resultExtraCheck(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "finding_id")
	if err == nil {
		_, err = queryValues(r)
	}
	var body struct {
		ExtraFinding *analysis.ExtraFinding `json:"extra_finding"`
	}
	if err == nil {
		err = decodeJSON(w, r, &body)
	}
	if err == nil && body.ExtraFinding == nil {
		err = invalidParameter("extra_finding")
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.AddExtraFinding(r.Context(), id, *body.ExtraFinding)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) resultListings(w http.ResponseWriter, r *http.Request) {
	values, err := queryValues(r, "favorite", "dismissed", "q", "limit", "cursor")
	query := service.ListingQuery{Q: values.Get("q")}
	if err == nil {
		query.ListQuery, err = queryList(values)
	}
	if err == nil {
		query.Favorite, err = queryBool(values, "favorite")
	}
	if err == nil {
		query.Dismissed, err = queryBool(values, "dismissed")
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.ListListings(r.Context(), query)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) resultListing(w http.ResponseWriter, r *http.Request) {
	id, err := pathText(r, "item_id")
	if err == nil {
		_, err = queryValues(r)
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.GetListing(r.Context(), id)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) resultUpdateListing(w http.ResponseWriter, r *http.Request) {
	id, err := pathText(r, "item_id")
	if err == nil {
		_, err = queryValues(r)
	}
	var patch map[string]json.RawMessage
	if err == nil {
		patch, err = readPatch(w, r)
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.UpdateListing(r.Context(), id, service.ListingPatch(patch))
	b.reply(w, r, http.StatusOK, value, err)
}

func resultListQuery(r *http.Request) (service.ListQuery, error) {
	values, err := queryValues(r, "limit", "cursor")
	if err != nil {
		return service.ListQuery{}, err
	}
	return queryList(values)
}

func (b *businessHandler) resultPrices(w http.ResponseWriter, r *http.Request) {
	id, err := pathText(r, "item_id")
	var query service.ListQuery
	if err == nil {
		query, err = resultListQuery(r)
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.ListPrices(r.Context(), id, query)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) resultBlockedSellers(w http.ResponseWriter, r *http.Request) {
	query, err := resultListQuery(r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.ListBlockedSellers(r.Context(), query)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) resultUnblockSeller(w http.ResponseWriter, r *http.Request) {
	id, err := pathText(r, "seller_id")
	if err == nil {
		_, err = queryValues(r)
	}
	if err == nil {
		err = noBody(r)
	}
	if err == nil {
		err = b.service.UnblockSeller(r.Context(), id)
	}
	b.reply(w, r, http.StatusNoContent, nil, err)
}

func (b *businessHandler) resultBlockSeller(w http.ResponseWriter, r *http.Request) {
	id, err := pathText(r, "seller_id")
	if err == nil {
		_, err = queryValues(r)
	}
	var body struct {
		Nick json.RawMessage `json:"nick"`
	}
	if err == nil {
		err = decodeJSON(w, r, &body)
	}
	if err == nil && body.Nick == nil {
		err = invalidParameter("nick")
	}
	var nick *string
	if err == nil && json.Unmarshal(body.Nick, &nick) != nil {
		err = invalidParameter("nick")
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.BlockSeller(r.Context(), id, nick)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) resultOverview(w http.ResponseWriter, r *http.Request) {
	values, err := queryValues(r, "period")
	if err != nil {
		b.failure(w, r, err)
		return
	}
	period := values.Get("period")
	if !values.Has("period") {
		period = "today"
	}
	value, err := b.service.GetOverview(r.Context(), period)
	b.reply(w, r, http.StatusOK, value, err)
}

func resultRunQuery(r *http.Request) (service.RunQuery, error) {
	values, err := queryValues(r, "task_id", "outcome", "failure_kind", "q", "from_at", "to_at", "limit", "cursor")
	if err != nil {
		return service.RunQuery{}, err
	}
	query := service.RunQuery{Q: values.Get("q")}
	if query.ListQuery, err = queryList(values); err != nil {
		return service.RunQuery{}, err
	}
	if query.TaskID, err = queryID(values, "task_id"); err != nil {
		return service.RunQuery{}, err
	}
	if query.FromAt, err = queryTime(values, "from_at"); err != nil {
		return service.RunQuery{}, err
	}
	if query.ToAt, err = queryTime(values, "to_at"); err != nil {
		return service.RunQuery{}, err
	}
	if _, exists := values["outcome"]; exists {
		value := service.RunOutcome(values.Get("outcome"))
		query.Outcome = &value
	}
	if _, exists := values["failure_kind"]; exists {
		value := service.FailureKind(values.Get("failure_kind"))
		query.FailureKind = &value
	}
	return query, nil
}

func (b *businessHandler) resultRuns(w http.ResponseWriter, r *http.Request) {
	query, err := resultRunQuery(r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.ListRuns(r.Context(), query)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) resultRun(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "run_id")
	if err == nil {
		_, err = queryValues(r)
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.GetRun(r.Context(), id)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) resultRunSteps(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "run_id")
	var query service.ListQuery
	if err == nil {
		query, err = resultListQuery(r)
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.ListRunSteps(r.Context(), id, query)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) resultRunOverview(w http.ResponseWriter, r *http.Request) {
	if _, err := queryValues(r); err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.GetRunLogOverview(r.Context())
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) resultSystemEvents(w http.ResponseWriter, r *http.Request) {
	values, err := queryValues(r, "resolved", "limit", "cursor")
	var query service.SystemEventQuery
	if err == nil {
		query.ListQuery, err = queryList(values)
	}
	if err == nil {
		query.Resolved, err = queryBool(values, "resolved")
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.ListSystemEvents(r.Context(), query)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) resultSystemStatus(w http.ResponseWriter, r *http.Request) {
	if _, err := queryValues(r); err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.GetSystemStatus(r.Context())
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) resultClearImages(w http.ResponseWriter, r *http.Request) {
	_, err := queryValues(r)
	if err == nil {
		err = noBody(r)
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.ClearImageCache(r.Context())
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) resultClearData(w http.ResponseWriter, r *http.Request) {
	_, err := queryValues(r)
	var body struct {
		Confirmation string `json:"confirmation"`
	}
	if err == nil {
		err = decodeJSON(w, r, &body)
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.ClearBusinessData(r.Context(), body.Confirmation)
	b.reply(w, r, http.StatusOK, value, err)
}
