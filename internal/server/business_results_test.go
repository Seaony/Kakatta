package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
)

func resultExec(t *testing.T, f *businessFixture, statement string, args ...any) sql.Result {
	t.Helper()
	result, err := f.store.DB().ExecContext(t.Context(), statement, args...)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func resultTask(t *testing.T, f *businessFixture) int64 {
	t.Helper()
	result := resultExec(t, f, `INSERT INTO tasks(name,keyword,enabled,max_pages,interval_minutes,judge_mode,notify_policy,sort_index,
		baseline_pending,created_at,updated_at) VALUES('相机任务','相机',0,1,30,'keyword','recommended',0,0,?,?)`, f.clock.Load(), f.clock.Load())
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func resultFinding(t *testing.T, f *businessFixture, taskID int64, itemID string, order int64, fresh bool) int64 {
	t.Helper()
	now := f.clock.Load()
	var fetchedAt any
	if fresh {
		fetchedAt = now - time.Hour.Milliseconds()
	}
	resultExec(t, f, `INSERT INTO listings(item_id,title,desktop_url,price,seller_id,description,detail_fetched_at,first_seen_at,last_seen_at)
		VALUES(?,?,?,20000,?,'个人相机，可正常使用',?,?,?)`, itemID, "相机 "+itemID, "https://example.invalid/"+itemID, "seller-"+itemID, fetchedAt, now-1000, now-1000)
	result := resultExec(t, f, `INSERT INTO findings(task_id,item_id,search_generation,found_at,is_baseline,decision,stage,decided_layer,
		is_read,user_overrides_json,matched_keywords_json) VALUES(?,?,1,?,0,'matched','done',2,0,'{"risks":{}}','["相机"]')`, taskID, itemID, now-1000+order)
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	resultExec(t, f, `INSERT INTO seen_items(task_id,item_id,first_seen_at) VALUES(?,?,?)`, taskID, itemID, now-1000)
	return id
}

func resultCount(t *testing.T, f *businessFixture, statement string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := f.store.DB().QueryRowContext(t.Context(), statement, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func resultPath(id int64) string { return "/api/v1/findings/" + strconv.FormatInt(id, 10) }

func TestBusinessResultReadFlagsAndPagination(t *testing.T) {
	f := newBusinessFixture(t)
	taskID := resultTask(t, f)
	firstID := resultFinding(t, f, taskID, "first", 1, false)
	secondID := resultFinding(t, f, taskID, "second", 2, false)
	thirdID := resultFinding(t, f, taskID, "third", 3, false)
	page := f.request(http.MethodGet, "/api/v1/findings?view=all&sort=latest&limit=1", "", true)
	requireStatus(t, page, http.StatusOK)
	value := decodeBody[service.Page[service.FindingView]](t, page)
	if value.Total != 3 || len(value.Items) != 1 || value.Items[0].ID != thirdID || value.NextCursor == nil {
		t.Fatal("结果分页没有真实读取全部匹配数量和最新一条")
	}
	next := f.request(http.MethodGet, "/api/v1/findings?view=all&sort=latest&limit=1&cursor="+url.QueryEscape(*value.NextCursor), "", true)
	requireStatus(t, next, http.StatusOK)
	if decodeBody[service.Page[service.FindingView]](t, next).Items[0].ID != secondID {
		t.Fatal("结果游标没有接着读取下一条")
	}
	detail := f.request(http.MethodGet, resultPath(secondID)+"?view=all&sort=latest", "", true)
	requireStatus(t, detail, http.StatusOK)
	finding := decodeBody[service.FindingView](t, detail)
	if finding.IsRead || finding.PreviousID == nil || *finding.PreviousID != thirdID || finding.NextID == nil || *finding.NextID != firstID {
		t.Fatal("只读详情改变已读状态或上下条没有使用当前上下文")
	}
	if resultCount(t, f, "SELECT is_read FROM findings WHERE id=?", secondID) != 0 {
		t.Fatal("GET详情不能隐式写已读")
	}
	for _, read := range []bool{true, false} {
		patch := f.request(http.MethodPatch, resultPath(secondID), fmt.Sprintf(`{"is_read":%t}`, read), true)
		requireStatus(t, patch, http.StatusOK)
		if decodeBody[service.FindingView](t, patch).IsRead != read {
			t.Fatal("显式false被当成未提供")
		}
	}
	for _, body := range []string{`{}`, `{"is_read":null}`, `{"is_read":false,"score":100}`} {
		requireStatus(t, f.request(http.MethodPatch, resultPath(secondID), body, true), http.StatusBadRequest)
	}
	marked := f.request(http.MethodPatch, "/api/v1/listings/second", `{"is_favorite":true,"is_dismissed":true}`, true)
	requireStatus(t, marked, http.StatusOK)
	if item := decodeBody[service.ListingView](t, marked); !item.IsFavorite || !item.IsDismissed {
		t.Fatal("本地商品标记没有保存")
	}
	visible := f.request(http.MethodGet, "/api/v1/findings", "", true)
	requireStatus(t, visible, http.StatusOK)
	if decodeBody[service.Page[service.FindingView]](t, visible).Total != 2 {
		t.Fatal("不感兴趣没有从普通结果全局隐藏")
	}
	hidden := f.request(http.MethodGet, "/api/v1/findings?view=hidden", "", true)
	requireStatus(t, hidden, http.StatusOK)
	if page := decodeBody[service.Page[service.FindingView]](t, hidden); page.Total != 1 || page.Items[0].ID != secondID {
		t.Fatal("隐藏结果不能读取原发现")
	}
	favorites := f.request(http.MethodGet, "/api/v1/listings?favorite=true&dismissed=true", "", true)
	requireStatus(t, favorites, http.StatusOK)
	if page := decodeBody[service.Page[service.ListingView]](t, favorites); page.Total != 1 || page.Items[0].ItemID != "second" {
		t.Fatal("全局商品标记没有用于列表筛选")
	}
	requireStatus(t, f.request(http.MethodPatch, "/api/v1/listings/second", `{"is_dismissed":false}`, true), http.StatusOK)
	requireStatus(t, f.request(http.MethodPatch, "/api/v1/listings/second", `{"is_favorite":null}`, true), http.StatusUnprocessableEntity)
	resultExec(t, f, `INSERT INTO price_points(item_id,price,observed_at) VALUES('second',22000,?),('second',20000,?)`, f.clock.Load()-2000, f.clock.Load()-1000)
	prices := f.request(http.MethodGet, "/api/v1/listings/second/prices?limit=1", "", true)
	requireStatus(t, prices, http.StatusOK)
	pricePage := decodeBody[service.Page[service.PricePoint]](t, prices)
	if pricePage.Total != 2 || pricePage.Items[0].Price != 22000 || pricePage.NextCursor == nil {
		t.Fatal("价格历史没有按完整数量和观察时间分页")
	}
	prices = f.request(http.MethodGet, "/api/v1/listings/second/prices?limit=1&cursor="+url.QueryEscape(*pricePage.NextCursor), "", true)
	requireStatus(t, prices, http.StatusOK)
	if decodeBody[service.Page[service.PricePoint]](t, prices).Items[0].Price != 20000 {
		t.Fatal("价格游标丢失第二个点")
	}
}

func TestBusinessResultDeleteKeepsSeenAndFavoriteListing(t *testing.T) {
	f := newBusinessFixture(t)
	taskID := resultTask(t, f)
	keepID := resultFinding(t, f, taskID, "favorite", 1, false)
	removeID := resultFinding(t, f, taskID, "ordinary", 2, false)
	requireStatus(t, f.request(http.MethodPatch, "/api/v1/listings/favorite", `{"is_favorite":true}`, true), http.StatusOK)
	deleted := f.request(http.MethodDelete, "/api/v1/tasks/"+strconv.FormatInt(taskID, 10)+"/findings", "", true)
	requireStatus(t, deleted, http.StatusOK)
	if decodeBody[service.MutationResult](t, deleted).Applied != 1 || resultCount(t, f, "SELECT count(*) FROM findings WHERE id=?", keepID) != 1 || resultCount(t, f, "SELECT count(*) FROM findings WHERE id=?", removeID) != 0 {
		t.Fatal("清空任务结果应该保留收藏发现")
	}
	if resultCount(t, f, "SELECT count(*) FROM seen_items WHERE task_id=?", taskID) != 2 {
		t.Fatal("清空结果不能清掉见过")
	}
	requireStatus(t, f.request(http.MethodDelete, resultPath(keepID), "", true), http.StatusNoContent)
	if resultCount(t, f, "SELECT count(*) FROM seen_items WHERE task_id=?", taskID) != 2 {
		t.Fatal("单个删除不能清掉见过")
	}
	requireStatus(t, f.request(http.MethodDelete, "/api/v1/tasks/"+strconv.FormatInt(taskID, 10), "", true), http.StatusNoContent)
	listing := f.request(http.MethodGet, "/api/v1/listings/favorite", "", true)
	requireStatus(t, listing, http.StatusOK)
	if value := decodeBody[service.ListingView](t, listing); !value.IsFavorite || len(value.Findings) != 0 {
		t.Fatal("删任务后收藏商品应可独立读取")
	}
}

func TestBusinessResultBatchAndSingleReanalysisStayDistinct(t *testing.T) {
	f := newBusinessFixture(t)
	taskID := resultTask(t, f)
	firstID := resultFinding(t, f, taskID, "cached", 1, true)
	secondID := resultFinding(t, f, taskID, "stale", 2, false)
	previewBody := fmt.Sprintf(`{"ids":[%d,%d]}`, firstID, secondID)
	preview := f.request(http.MethodPost, "/api/v1/findings/reanalysis-preview", previewBody, true)
	requireStatus(t, preview, http.StatusOK)
	if value := decodeBody[service.ReanalysisPreview](t, preview); value.RequestedCount != 2 || value.CachedDetailCount != 1 || value.NeedsDetailCount != 1 {
		t.Fatal("预览没有统计真实缓存")
	}
	batch := f.request(http.MethodPost, "/api/v1/findings/batch", fmt.Sprintf(`{"action":"reanalyze","ids":[%d]}`, firstID), true)
	requireStatus(t, batch, http.StatusAccepted)
	if value := decodeBody[service.ReanalysisResult](t, batch); len(value.Items) != 1 || value.Items[0].Status != service.ReanalysisQueued {
		t.Fatal("批量接口只选一件也必须进入普通队列")
	}
	if resultCount(t, f, "SELECT count(*) FROM findings WHERE id=? AND decision='pending' AND stage='detail' AND processing_revision=2 AND reanalysis_origin=1", firstID) != 1 {
		t.Fatal("批量重分析没有真实持久化新的工作版本")
	}
	one := f.request(http.MethodPost, resultPath(firstID)+"/reanalyze", "", true)
	requireStatus(t, one, http.StatusAccepted)
	if value := decodeBody[service.ReanalysisResult](t, one); len(value.Items) != 1 || value.Items[0].Status != service.ReanalysisCached {
		t.Fatal("单件缓存例外没有调用独立入口")
	}
	duplicate := f.request(http.MethodPost, "/api/v1/findings/batch", fmt.Sprintf(`{"action":"dismiss","ids":[%d,%d]}`, firstID, firstID), true)
	requireStatus(t, duplicate, http.StatusOK)
	if value := decodeBody[service.BatchResult](t, duplicate); value.Requested != 1 || value.Applied+value.Skipped != 1 || len(value.Items) != 1 {
		t.Fatal("批量应按去重后的实际发现计算数量")
	}
	batch = f.request(http.MethodPost, "/api/v1/findings/batch", fmt.Sprintf(`{"action":"dismiss","ids":[%d,%d]}`, firstID, secondID), true)
	requireStatus(t, batch, http.StatusOK)
	if value := decodeBody[service.BatchResult](t, batch); value.Requested != 2 || value.Applied+value.Skipped != 2 || len(value.Items) != 2 {
		t.Fatal("批量结果数量不完整")
	}
	if resultCount(t, f, "SELECT count(*) FROM listings WHERE is_dismissed=1") != 2 {
		t.Fatal("批量不感兴趣没有更新全局商品")
	}
	deleted := f.request(http.MethodPost, "/api/v1/findings/batch", fmt.Sprintf(`{"action":"delete","ids":[%d]}`, firstID), true)
	requireStatus(t, deleted, http.StatusOK)
	if value := decodeBody[service.BatchResult](t, deleted); value.Requested != 1 || value.Applied != 1 || resultCount(t, f, "SELECT count(*) FROM seen_items WHERE task_id=?", taskID) != 2 {
		t.Fatal("批量删除应该删除选择的发现但保留见过")
	}
}

func TestBusinessResultQueriesAndWritesRejectInvalidInput(t *testing.T) {
	f := newBusinessFixture(t)
	taskID := resultTask(t, f)
	id := resultFinding(t, f, taskID, "camera", 1, false)
	for _, path := range []string{
		"/api/v1/findings?limit=1&limit=2", "/api/v1/findings?view=", "/api/v1/findings?sort=", "/api/v1/findings?view=invalid",
		"/api/v1/findings?price_drop=1", "/api/v1/findings?task_id=0", "/api/v1/findings?cursor=invalid", "/api/v1/findings?unknown=1",
		resultPath(id) + "?limit=1", "/api/v1/listings?favorite=1", "/api/v1/runs?outcome=invalid", "/api/v1/runs?from_at=2&to_at=1",
		"/api/v1/runs?limit=201", "/api/v1/system/events?resolved=1", "/api/v1/overview?period=", "/api/v1/overview?period=all",
	} {
		t.Run(path, func(t *testing.T) { requireStatus(t, f.request(http.MethodGet, path, "", true), http.StatusBadRequest) })
	}
	for _, body := range []string{`{"is_read":true,"is_read":false}`, `{"IsRead":true}`, `{"is_read":true} {}`} {
		requireStatus(t, f.request(http.MethodPatch, resultPath(id), body, true), http.StatusBadRequest)
	}
	requireStatus(t, f.request(http.MethodPatch, "/api/v1/listings/camera", `{"is_favorite":true,"score":100}`, true), http.StatusUnprocessableEntity)
	requireStatus(t, f.request(http.MethodPost, "/api/v1/findings/batch", `{"action":"unknown","ids":[1]}`, true), http.StatusBadRequest)
	requireStatus(t, f.request(http.MethodPost, resultPath(id)+"/reanalyze", `{}`, true), http.StatusBadRequest)
	requireStatus(t, f.request(http.MethodGet, resultPath(id)+"/reanalyze", "", true), http.StatusMethodNotAllowed)
	tooLarge := `{"is_read":true,"padding":"` + strings.Repeat("x", maxBody) + `"}`
	requireStatus(t, f.request(http.MethodPatch, resultPath(id), tooLarge, true), http.StatusRequestEntityTooLarge)
	for _, pattern := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/findings", ""}, {http.MethodGet, "/api/v1/overview", ""}, {http.MethodGet, "/api/v1/runs", ""},
		{http.MethodPatch, resultPath(id), `{"is_read":true}`}, {http.MethodPost, "/api/v1/findings/batch", `{"action":"delete","ids":[1]}`},
	} {
		requireStatus(t, f.request(pattern.method, pattern.path, pattern.body, false), http.StatusUnauthorized)
	}
	before := resultCount(t, f, "SELECT count(*) FROM findings")
	cross := f.request(http.MethodPost, "/api/v1/findings/batch", fmt.Sprintf(`{"action":"delete","ids":[%d]}`, id), true, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
	requireStatus(t, cross, http.StatusForbidden)
	if resultCount(t, f, "SELECT count(*) FROM findings") != before {
		t.Fatal("跨站请求不应发生写入")
	}
}

func TestBusinessResultSpecsOverridesAndExtraCheckUseStoredFacts(t *testing.T) {
	f := newBusinessFixture(t)
	taskID := resultTask(t, f)
	id := resultFinding(t, f, taskID, "camera", 1, true)
	profile := analysis.DefaultProfile()
	profile.Specs = []analysis.ProfileSpec{{Name: "型号", Options: []string{"A", "B"}, Values: []string{"A"}, Source: analysis.SourceUser, Edited: true}}
	profileRaw, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	model := "A"
	extra := analysis.ExtraFinding{Name: "快门失灵", Evidence: "快门有时失灵", Source: "text"}
	facts := analysis.Facts{Specs: []analysis.SpecValue{{Name: "型号", Value: &model}}, Musts: []analysis.RequirementFact{}, Bonuses: []analysis.RequirementFact{},
		Checks: []analysis.CheckFact{}, SellerSignals: []analysis.SellerSignal{}, ExtraFindings: []analysis.ExtraFinding{extra}, Summary: "本地保存事实"}
	factsRaw, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	resultExec(t, f, "UPDATE tasks SET judge_mode='ai',notify_policy='good',profile_json=? WHERE id=?", string(profileRaw), taskID)
	resultExec(t, f, "UPDATE listings SET description='快门有时失灵' WHERE item_id='camera'")
	resultExec(t, f, "UPDATE findings SET decision='scored',decided_layer=3,analysis_json=?,score=80,grade='good' WHERE id=?", string(factsRaw), id)
	path := fmt.Sprintf("/api/v1/tasks/%d/items/camera/specs", taskID)
	updated := f.request(http.MethodPut, path, `{"specs":[{"name":"型号","value":"A"}],"brand_new":null}`, true)
	requireStatus(t, updated, http.StatusOK)
	if value := decodeBody[service.ItemSpecsUpdateResult](t, updated); value.ItemSpecs.Source != analysis.SpecUser || value.ItemSpecs.ConfigKey == nil || *value.ItemSpecs.ConfigKey != "A" || value.ItemSpecs.BrandNew != nil {
		t.Fatal("手动规格没有以用户来源保存，或null丢失")
	}
	for _, body := range []string{`{"specs":[]}`, `{"specs":null,"brand_new":null}`, `{"specs":[],"brand_new":4}`} {
		requireStatus(t, f.request(http.MethodPut, path, body, true), http.StatusBadRequest)
	}
	override := f.request(http.MethodPut, resultPath(id)+"/overrides", `{"risks":{"photo_mismatch":{"status":"ok","rev":1}}}`, true)
	requireStatus(t, override, http.StatusOK)
	if value := decodeBody[service.FindingView](t, override); value.UserOverrides.Risks["photo_mismatch"].Status != analysis.CheckOK {
		t.Fatal("风险覆盖没有保存")
	}
	requireStatus(t, f.request(http.MethodPut, resultPath(id)+"/overrides", `{}`, true), http.StatusBadRequest)
	requireStatus(t, f.request(http.MethodPut, resultPath(id)+"/overrides", `{"risks":null}`, true), http.StatusBadRequest)
	cleared := f.request(http.MethodPut, resultPath(id)+"/overrides", `{"risks":{}}`, true)
	requireStatus(t, cleared, http.StatusOK)
	if len(decodeBody[service.FindingView](t, cleared).UserOverrides.Risks) != 0 {
		t.Fatal("显式空map不能清除覆盖")
	}
	body, _ := json.Marshal(map[string]analysis.ExtraFinding{"extra_finding": extra})
	added := f.request(http.MethodPost, resultPath(id)+"/extra-check", string(body), true)
	requireStatus(t, added, http.StatusOK)
	if len(decodeBody[service.TaskSaveResult](t, added).Task.Profile.Risks) != len(profile.Risks)+1 {
		t.Fatal("清单外发现未加入当前任务清单")
	}
	requireStatus(t, f.request(http.MethodPost, resultPath(id)+"/extra-check", string(body), true), http.StatusConflict)
}

func TestBusinessResultBlockedSellerReturnsActualSavedRow(t *testing.T) {
	f := newBusinessFixture(t)
	taskID := resultTask(t, f)
	resultFinding(t, f, taskID, "camera", 1, false)
	blocked := f.request(http.MethodPut, "/api/v1/blocked-sellers/seller-camera", `{"nick":"原昵称"}`, true)
	requireStatus(t, blocked, http.StatusOK)
	row := decodeBody[service.BlockedSeller](t, blocked)
	if row.SellerID != "seller-camera" || row.Nick == nil || *row.Nick != "原昵称" || row.CreatedAt == nil || *row.CreatedAt != f.clock.Load() {
		t.Fatal("屏蔽接口必须返回真实保存的行")
	}
	f.advance(time.Second)
	again := f.request(http.MethodPut, "/api/v1/blocked-sellers/seller-camera", `{"nick":null}`, true)
	requireStatus(t, again, http.StatusOK)
	if saved := decodeBody[service.BlockedSeller](t, again); saved.Nick == nil || *saved.Nick != "原昵称" || saved.CreatedAt == nil || *saved.CreatedAt != *row.CreatedAt {
		t.Fatal("重复屏蔽不能伪造创建时间或丢已有昵称")
	}
	list := f.request(http.MethodGet, "/api/v1/blocked-sellers?limit=1", "", true)
	requireStatus(t, list, http.StatusOK)
	if decodeBody[service.Page[service.BlockedSeller]](t, list).Total != 1 {
		t.Fatal("屏蔽记录不应重复")
	}
	visible := f.request(http.MethodGet, "/api/v1/findings", "", true)
	requireStatus(t, visible, http.StatusOK)
	if decodeBody[service.Page[service.FindingView]](t, visible).Total != 0 {
		t.Fatal("屏蔽卖家应全局隐藏")
	}
	requireStatus(t, f.request(http.MethodDelete, "/api/v1/blocked-sellers/seller-camera", "", true), http.StatusNoContent)
	requireStatus(t, f.request(http.MethodPut, "/api/v1/blocked-sellers/seller-camera", `{}`, true), http.StatusBadRequest)
}

func TestBusinessResultRunsOverviewAndSystemReadRealFacts(t *testing.T) {
	f := newBusinessFixture(t)
	taskID := resultTask(t, f)
	resultFinding(t, f, taskID, "camera", 1, false)
	now := f.clock.Load()
	result := resultExec(t, f, `INSERT INTO run_records(task_id,task_name,trigger,started_at,ended_at,outcome,scanned_count,new_count)
		VALUES(?,'已保存任务名','manual',?,?,'success',7,3)`, taskID, now-30000, now-20000)
	runID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	resultExec(t, f, `INSERT INTO run_records(task_id,task_name,trigger,started_at,ended_at,outcome,failure_kind)
		VALUES(?,'失败快照','manual',?,?,'failed','network')`, taskID, now-15000, now-14000)
	skipped := resultExec(t, f, `INSERT INTO run_records(task_id,task_name,trigger,started_at,ended_at,outcome,repeat_count)
		VALUES(?,'折叠跳过','auto',?,?,'skipped',3)`, taskID, now-12000, now-9000)
	skipID, err := skipped.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	resultExec(t, f, `INSERT INTO run_skip_events(run_id,seq,at) VALUES(?,1,?),(?,2,?),(?,3,?)`, skipID, now-11000, skipID, now-10000, skipID, now-9000)
	unfinished := resultExec(t, f, `INSERT INTO run_records(task_id,task_name,trigger,started_at,outcome,failure_kind,message)
		VALUES(?,'正在运行','manual',?,'failed','restart','{"code":"run.restart","args":{}}')`, taskID, now-5000)
	unfinishedID, err := unfinished.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	for seq := 1; seq <= 150; seq++ {
		resultExec(t, f, `INSERT INTO run_steps(run_id,seq,at,status,message) VALUES(?,?,?,'fail','{"code":"run.test","args":{}}')`, runID, seq, now-25000)
	}
	for _, path := range []string{
		"/api/v1/runs?task_id=" + strconv.FormatInt(taskID, 10) + "&outcome=success&from_at=" + strconv.FormatInt(now-40000, 10) + "&to_at=" + strconv.FormatInt(now, 10),
		"/api/v1/runs?failure_kind=network&q=" + url.QueryEscape("失败快照"),
	} {
		page := f.request(http.MethodGet, path, "", true)
		requireStatus(t, page, http.StatusOK)
		if decodeBody[service.Page[service.RunView]](t, page).Total != 1 {
			t.Fatal("日志筛选没有读取实际快照")
		}
	}
	detail := f.request(http.MethodGet, "/api/v1/runs/"+strconv.FormatInt(runID, 10), "", true)
	requireStatus(t, detail, http.StatusOK)
	if value := decodeBody[service.RunDetail](t, detail); value.Run.TaskName != "已保存任务名" || value.Steps.Total != 150 || len(value.Steps.Items) != 50 || value.Steps.NextCursor == nil {
		t.Fatal("运行详情没有复用完整步骤分页")
	}
	steps := f.request(http.MethodGet, "/api/v1/runs/"+strconv.FormatInt(runID, 10)+"/steps?limit=200", "", true)
	requireStatus(t, steps, http.StatusOK)
	if value := decodeBody[service.Page[service.RunStep]](t, steps); value.Total != 150 || len(value.Items) != 150 || value.Items[149].Seq != 150 {
		t.Fatal("日志不能截掉100步之后的记录")
	}
	current := f.request(http.MethodGet, "/api/v1/runs/"+strconv.FormatInt(unfinishedID, 10), "", true)
	requireStatus(t, current, http.StatusOK)
	if run := decodeBody[service.RunDetail](t, current).Run; run.Outcome != nil || run.FailureKind != nil || run.Message != nil || run.EndedAt != nil {
		t.Fatal("未结束运行不能冒充重启失败")
	}
	summary := f.request(http.MethodGet, "/api/v1/runs/overview", "", true)
	requireStatus(t, summary, http.StatusOK)
	if value := decodeBody[service.RunLogOverview](t, summary); value.TotalCount != 5 || value.SuccessCount != 1 || value.FailedCount != 1 || value.SkippedCount != 3 || len(value.Buckets) != 24 || value.SuccessRate == nil || *value.SuccessRate != 0.5 {
		t.Fatal("运行概览没有用逐次跳过事实或排除未结束运行")
	}
	overview := f.request(http.MethodGet, "/api/v1/overview", "", true)
	requireStatus(t, overview, http.StatusOK)
	if value := decodeBody[service.OverviewView](t, overview); value.Period != "today" || value.Cards.ScannedCount != 7 || value.Cards.NewCount != 3 || value.Cards.RecommendedKeywordCount != 1 {
		t.Fatal("概览没有返回服务端统计")
	}
	resultExec(t, f, `INSERT INTO system_events(kind,subject_id,message,created_at,resolved_at) VALUES
		('login_failures',0,'{"code":"security.login_failures","args":{"count":20,"ip":"127.0.0.1"}}',?,NULL),
		('xianyu_layout',0,'{"code":"xianyu.layout","args":{}}',?,?)`, now-1000, now-2000, now-1000)
	for _, resolved := range []string{"false", "true"} {
		events := f.request(http.MethodGet, "/api/v1/system/events?resolved="+resolved, "", true)
		requireStatus(t, events, http.StatusOK)
		if decodeBody[service.Page[service.SystemEvent]](t, events).Total != 1 {
			t.Fatal("事件筛选没有区分已解决与未解决")
		}
	}
	status := f.request(http.MethodGet, "/api/v1/system/status", "", true)
	requireStatus(t, status, http.StatusOK)
	if value := decodeBody[service.SystemStatus](t, status); value.AsOf != now || value.Configuration == nil || value.Quotas == nil || value.Resources.DiskTotalBytes == nil {
		t.Fatal("系统状态没有读取真实配置和磁盘")
	}
}

func TestBusinessResultMaintenanceClearsOnlyAuthorizedData(t *testing.T) {
	f := newBusinessFixture(t)
	taskID := resultTask(t, f)
	resultFinding(t, f, taskID, "camera", 1, false)
	if err := f.imageCache.Put(t.Context(), "camera", []byte("thumbnail"), 1024); err != nil {
		t.Fatal(err)
	}
	images := f.request(http.MethodPost, "/api/v1/maintenance/clear-image-cache", "", true)
	requireStatus(t, images, http.StatusOK)
	if decodeBody[service.CleanupResult](t, images).ImagesDeleted != 1 || resultCount(t, f, "SELECT count(*) FROM tasks") != 1 {
		t.Fatal("清图片缓存不能清业务")
	}
	if _, err := f.imageCache.Get(t.Context(), "camera"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("清理没有删除真实缓存")
	}
	if _, err := f.settings.Apply(t.Context(), settings.Values{"general.language": json.RawMessage(`"en"`)}); err != nil {
		t.Fatal(err)
	}
	resultExec(t, f, `INSERT INTO accounts(id,name,state) VALUES(1,'保留账号','signed_out')`)
	resultExec(t, f, `INSERT INTO quota_events(account_id,kind,at) VALUES(1,'search',?)`, f.clock.Load())
	resultExec(t, f, `INSERT INTO ai_calls(at,input_tokens,output_tokens) VALUES(?,3,2)`, f.clock.Load())
	profile := filepath.Join(f.dataDir, "browser", "1", "profile-marker")
	if err := os.MkdirAll(filepath.Dir(profile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profile, []byte("profile"), 0600); err != nil {
		t.Fatal(err)
	}
	invalid := f.request(http.MethodPost, "/api/v1/maintenance/clear-data", `{"confirmation":"不清除"}`, true)
	requireStatus(t, invalid, http.StatusUnprocessableEntity)
	if resultCount(t, f, "SELECT count(*) FROM tasks") != 1 {
		t.Fatal("确认不符时不能修改数据")
	}
	cleared := f.request(http.MethodPost, "/api/v1/maintenance/clear-data", `{"confirmation":"清除"}`, true)
	requireStatus(t, cleared, http.StatusOK)
	if value := decodeBody[service.CleanupResult](t, cleared); value.Deleted["tasks"] != 1 || value.Deleted["findings"] != 1 || resultCount(t, f, "SELECT count(*) FROM listings") != 0 {
		t.Fatal("清除业务没有返回真实删除数量")
	}
	for _, table := range []string{"admin", "auth_tokens", "accounts", "quota_events", "ai_calls", "settings"} {
		if resultCount(t, f, "SELECT count(*) FROM "+table) == 0 {
			t.Fatal("保护数据被清掉", table)
		}
	}
	if _, err := os.ReadFile(profile); err != nil {
		t.Fatal("清业务不能清浏览器登录目录")
	}
	identity := f.request(http.MethodGet, "/api/v1/me", "", true)
	requireStatus(t, identity, http.StatusOK)
	if decodeBody[meResponse](t, identity).Language != "en" {
		t.Fatal("清除业务不能重置设置或会话")
	}
}

func TestBusinessResultWritesThroughRealCookieAndBearerHTTP(t *testing.T) {
	f := newBusinessFixture(t)
	taskID := resultTask(t, f)
	resultFinding(t, f, taskID, "camera", 1, false)
	token, err := f.auth.CreateToken(t.Context(), f.session.Identity, "本地HTTP脚本")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(f.server)
	defer server.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPatch, server.URL+"/api/v1/listings/camera", strings.NewReader(`{"is_favorite":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.Token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var saved service.ListingView
	decodeErr := json.NewDecoder(response.Body).Decode(&saved)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || !saved.IsFavorite || resultCount(t, f, `SELECT is_favorite FROM listings WHERE item_id='camera'`) != 1 {
		t.Fatal("真实HTTP令牌写入没有保存商品标记")
	}
	request, err = http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/api/v1/listings?favorite=true", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: cookieName, Value: f.session.Token})
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var page service.Page[service.ListingView]
	decodeErr = json.NewDecoder(response.Body).Decode(&page)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ItemID != "camera" {
		t.Fatal("真实HTTP会话读取未取得同一保存对象")
	}
	request, err = http.NewRequestWithContext(t.Context(), http.MethodPatch, server.URL+"/api/v1/listings/camera", strings.NewReader(`{"is_favorite":false}`))
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: cookieName, Value: f.session.Token})
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://cross.invalid")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden || resultCount(t, f, `SELECT is_favorite FROM listings WHERE item_id='camera'`) != 1 {
		t.Fatal("跨站真实HTTP写入绕过保护")
	}
}
