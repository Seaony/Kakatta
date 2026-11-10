package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/service"
)

func TestBusinessTaskDefaultsCreateAndPatchPresence(t *testing.T) {
	f := newBusinessFixture(t)
	account, err := f.store.DB().ExecContext(t.Context(), `INSERT INTO accounts(name,state,created_at) VALUES('本地假账号','active',?)`, f.clock.Load())
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ := account.LastInsertId()
	channel, err := f.store.DB().ExecContext(t.Context(), `INSERT INTO channels(uuid,type,name,enabled,config_json) VALUES('task-channel','webhook','本地假渠道',1,'{}')`)
	if err != nil {
		t.Fatal(err)
	}
	channelID, _ := channel.LastInsertId()
	if _, err := f.store.DB().ExecContext(t.Context(), `INSERT INTO channels(uuid,type,name,enabled,config_json) VALUES('disabled-task-channel','webhook','停用的本地渠道',0,'{}')`); err != nil {
		t.Fatal(err)
	}
	defaults := f.request(http.MethodGet, "/api/v1/tasks/defaults", "", true)
	requireStatus(t, defaults, http.StatusOK)
	fields := decodeBody[service.TaskFields](t, defaults)
	if fields.Keyword != "" || fields.MaxPages != 1 || fields.IntervalMinutes != 30 || !fields.PersonalOnly || !reflect.DeepEqual(fields.ChannelIDs, []int64{channelID}) {
		t.Fatal("草稿默认值应来自当前设置和启用渠道")
	}
	keyword := strings.Repeat("相机", 20)
	body, _ := json.Marshal(map[string]any{"keyword": keyword, "enabled": false, "min_price": 10000})
	created := f.request(http.MethodPost, "/api/v1/tasks", string(body), true)
	requireStatus(t, created, http.StatusCreated)
	saved := decodeBody[service.TaskSaveResult](t, created)
	if saved.Task.ID < 1 || saved.Task.Name != strings.Repeat("相机", 15) || saved.Task.Keyword != keyword || saved.Task.AccountID == nil || *saved.Task.AccountID != accountID || saved.Task.AccountPinned || saved.Task.Enabled {
		t.Fatal("新任务的缺省字段、自动绑定或名称截取不正确")
	}
	if saved.Task.SearchGeneration != 1 || !saved.Task.BaselinePending || !reflect.DeepEqual(saved.Task.ChannelIDs, []int64{channelID}) || saved.Task.MinPrice == nil || *saved.Task.MinPrice != 10000 {
		t.Fatal("初始代次、基线、渠道或价格没有落入真实任务")
	}
	path := "/api/v1/tasks/" + strconv.FormatInt(saved.Task.ID, 10)
	get := f.request(http.MethodGet, path, "", true)
	requireStatus(t, get, http.StatusOK)
	if got := decodeBody[service.TaskView](t, get); !reflect.DeepEqual(got.Task, saved.Task) {
		t.Fatal("HTTP读取必须返回已保存的同一任务对象")
	}
	patch := f.request(http.MethodPatch, path, `{"name":"改名","channel_ids":[]}`, true)
	requireStatus(t, patch, http.StatusOK)
	updated := decodeBody[service.TaskSaveResult](t, patch)
	if updated.Task.MinPrice == nil || *updated.Task.MinPrice != 10000 || updated.Task.Name != "改名" || len(updated.Task.ChannelIDs) != 0 || updated.Task.SearchGeneration != 1 || updated.Effects.SearchReset {
		t.Fatal("省略应保留，显式空列表应清空；改名不能换搜索代次")
	}
	risky := f.request(http.MethodPatch, path, `{"max_pages":4}`, true)
	requireStatus(t, risky, http.StatusOK)
	if value := decodeBody[service.TaskSaveResult](t, risky); value.Task.MaxPages != 4 || value.Task.SearchGeneration != 1 || len(value.Warnings) == 0 {
		t.Fatal("超过推荐翻页数应保留用户设置并返回风险，不能截回推荐值或切搜索代次")
	}
	clear := f.request(http.MethodPatch, path, `{"min_price":null}`, true)
	requireStatus(t, clear, http.StatusOK)
	cleared := decodeBody[service.TaskSaveResult](t, clear)
	if cleared.Task.MinPrice != nil || cleared.Task.SearchGeneration != 2 || !cleared.Effects.SearchReset || cleared.Effects.Generation != 2 {
		t.Fatal("显式清空价格必须保存并切换搜索代次")
	}
	invalid := f.request(http.MethodPatch, path, `{"name":"不应保存","enabled":null}`, true)
	requireStatus(t, invalid, http.StatusUnprocessableEntity)
	current := decodeBody[service.TaskView](t, f.request(http.MethodGet, path, "", true))
	if current.Name != "改名" || current.Enabled || current.SearchGeneration != 2 {
		t.Fatal("无效patch不能部分覆盖任务")
	}
	for _, table := range []string{"run_records", "quota_events"} {
		var count int64
		if err := f.store.DB().QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatal("任务CRUD和默认值读取不能启动抓取")
		}
	}
}

func TestBusinessTaskQueriesAndTransportValidation(t *testing.T) {
	f := newBusinessFixture(t)
	for index := 0; index < 3; index++ {
		result := f.request(http.MethodPost, "/api/v1/tasks", fmt.Sprintf(`{"keyword":"相机%d","enabled":false}`, index), true)
		requireStatus(t, result, http.StatusCreated)
	}
	pageOne := f.request(http.MethodGet, "/api/v1/tasks?limit=2&enabled=false", "", true)
	requireStatus(t, pageOne, http.StatusOK)
	first := decodeBody[service.Page[service.TaskView]](t, pageOne)
	if len(first.Items) != 2 || first.Total != 3 || first.NextCursor == nil {
		t.Fatal("分页需要真实总数和后续游标")
	}
	pageTwo := f.request(http.MethodGet, "/api/v1/tasks?limit=2&enabled=false&cursor="+url.QueryEscape(*first.NextCursor), "", true)
	requireStatus(t, pageTwo, http.StatusOK)
	second := decodeBody[service.Page[service.TaskView]](t, pageTwo)
	if len(second.Items) != 1 || second.Total != 3 || second.Items[0].ID == first.Items[0].ID || second.Items[0].ID == first.Items[1].ID || second.NextCursor != nil {
		t.Fatal("任务分页不能重复或静默截断")
	}
	for _, path := range []string{
		"/api/v1/tasks?limit=0", "/api/v1/tasks?limit=201", "/api/v1/tasks?limit=2&limit=3",
		"/api/v1/tasks?enabled=1", "/api/v1/tasks?enabled=", "/api/v1/tasks?other=1", "/api/v1/tasks?cursor=broken",
		"/api/v1/tasks/0", "/api/v1/tasks/-1", "/api/v1/tasks/9223372036854775808", "/api/v1/tasks/defaults?limit=1",
		"/api/v1/tasks/1/runtime?arbitrary=true", "/api/v1/tasks/1/market/samples?brand_new=1",
	} {
		t.Run(path, func(t *testing.T) {
			requireStatus(t, f.request(http.MethodGet, path, "", true), http.StatusBadRequest)
		})
	}
	for _, body := range []string{
		`{"keyword":"一","keyword":"二"}`, `{"keyword":"一"}{}`, `[]`, `null`, `{"keyword":"\ud800` + string([]byte{0xff}) + `"}`,
	} {
		requireStatus(t, f.request(http.MethodPost, "/api/v1/tasks", body, true), http.StatusBadRequest)
	}
	invalid := f.request(http.MethodPost, "/api/v1/tasks", `{"keyword":"相机","search_generation":99}`, true)
	requireStatus(t, invalid, http.StatusUnprocessableEntity)
	if value := decodeBody[ErrorEnvelope](t, invalid); value.Error.Code != "tasks.invalid" || len(value.Fields) == 0 || value.Fields[0].Key != "search_generation" {
		t.Fatal("只读字段应由唯一服务校验器明确拒绝")
	}
	requireStatus(t, f.request(http.MethodPost, "/api/v1/tasks", `{"keyword":"相机"}`, true, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }), http.StatusUnsupportedMediaType)
	requireStatus(t, f.request(http.MethodPost, "/api/v1/tasks", `{"keyword":"`+strings.Repeat("x", maxBody)+`"}`, true), http.StatusRequestEntityTooLarge)
}

func TestBusinessTaskProfileUsesServerIDsAndRevisions(t *testing.T) {
	f := newBusinessFixture(t)
	profile := analysis.Profile{
		Musts:  []analysis.Requirement{{ID: "m999", Text: "国行", Source: analysis.SourceUser, Edited: true, Rev: 999}},
		NextNo: 1,
	}
	body, _ := json.Marshal(map[string]any{"keyword": "相机", "requirement": "国行相机", "judge_mode": "ai", "enabled": false, "profile": profile})
	result := f.request(http.MethodPost, "/api/v1/tasks", string(body), true)
	requireStatus(t, result, http.StatusCreated)
	created := decodeBody[service.TaskSaveResult](t, result)
	if len(created.Task.Profile.Musts) != 1 || created.Task.Profile.Musts[0].ID != "m1" || created.Task.Profile.Musts[0].Rev != 1 || created.Task.Profile.NextNo != 2 {
		t.Fatal("新清单编号和版本必须由服务分配")
	}
	profile = created.Task.Profile
	profile.Musts[0].Text = "日版"
	profile.Musts[0].Rev = 0
	profile.Bonuses = []analysis.Requirement{{ID: "b1", Text: "原盒", Source: analysis.SourceUser, Edited: true, Rev: 999}}
	profile.NextNo = 1
	patch, _ := json.Marshal(map[string]any{"profile": profile})
	path := "/api/v1/tasks/" + strconv.FormatInt(created.Task.ID, 10)
	reused := f.request(http.MethodPatch, path, string(patch), true)
	requireStatus(t, reused, http.StatusUnprocessableEntity)
	profile.Bonuses[0].ID = ""
	patch, _ = json.Marshal(map[string]any{"profile": profile})
	updated := f.request(http.MethodPatch, path, string(patch), true)
	requireStatus(t, updated, http.StatusOK)
	saved := decodeBody[service.TaskSaveResult](t, updated)
	if saved.Task.Profile.Musts[0].ID != "m1" || saved.Task.Profile.Musts[0].Rev != 2 || len(saved.Task.Profile.Bonuses) != 1 || saved.Task.Profile.Bonuses[0].ID != "b2" || saved.Task.Profile.Bonuses[0].Rev != 1 || saved.Task.Profile.NextNo != 3 {
		t.Fatal("编辑不能回拨水位或伪造事实版本")
	}
	if saved.Task.SearchGeneration != 1 || saved.Effects.SearchReset {
		t.Fatal("改清单不能重置搜索基线")
	}
	switched := f.request(http.MethodPatch, path, `{"judge_mode":"keyword"}`, true)
	requireStatus(t, switched, http.StatusOK)
	keyword := decodeBody[service.TaskSaveResult](t, switched)
	rawProfile, _ := json.Marshal(keyword.Task.Profile)
	if string(rawProfile) != "{}" || keyword.Task.NotifyPolicy != "recommended" || keyword.Task.SearchGeneration != 1 {
		t.Fatal("切到关键词应清空清单、沿用服务默认通知策略且不切搜索代次")
	}
}

func TestBusinessTaskRoutesAuthenticationAndCrossOrigin(t *testing.T) {
	f := newBusinessFixture(t)
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/tasks", ""}, {http.MethodGet, "/api/v1/tasks/defaults", ""},
		{http.MethodPost, "/api/v1/tasks", `{"keyword":"相机"}`}, {http.MethodGet, "/api/v1/tasks/1", ""},
		{http.MethodPatch, "/api/v1/tasks/1", `{}`}, {http.MethodDelete, "/api/v1/tasks/1", ""},
		{http.MethodGet, "/api/v1/tasks/1/runtime", ""}, {http.MethodPost, "/api/v1/tasks/1/run", ""},
		{http.MethodPost, "/api/v1/tasks/1/cancel", ""}, {http.MethodPost, "/api/v1/tasks/1/no-good-hint/dismiss", ""},
		{http.MethodPost, "/api/v1/tasks/1/apply-exclusions", `{"added_exclude_words":[]}`},
		{http.MethodGet, "/api/v1/tasks/1/market", ""}, {http.MethodGet, "/api/v1/tasks/1/market/samples", ""},
		{http.MethodGet, "/api/v1/risk-dictionary", ""}, {http.MethodGet, "/api/v1/regions", ""},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			requireStatus(t, f.request(route.method, route.path, route.body, false), http.StatusUnauthorized)
			if route.method != http.MethodGet {
				requireStatus(t, f.request(route.method, route.path, route.body, true, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }), http.StatusForbidden)
			}
		})
	}
	token, err := f.auth.CreateToken(t.Context(), f.session.Identity, "本地任务脚本")
	if err != nil {
		t.Fatal(err)
	}
	result := f.request(http.MethodPost, "/api/v1/tasks", `{"keyword":"脚本任务","enabled":false}`, false, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token.Token) })
	requireStatus(t, result, http.StatusCreated)
}

func TestBusinessTaskDeleteKeepsFavoriteAndDismissWritesNow(t *testing.T) {
	f := newBusinessFixture(t)
	result := f.request(http.MethodPost, "/api/v1/tasks", `{"keyword":"相机","enabled":false}`, true)
	requireStatus(t, result, http.StatusCreated)
	task := decodeBody[service.TaskSaveResult](t, result).Task
	path := "/api/v1/tasks/" + strconv.FormatInt(task.ID, 10)
	if _, err := f.store.DB().ExecContext(t.Context(), `INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at,is_favorite) VALUES('task-favorite','收藏相机','https://example.invalid/item',?,?,1)`, f.clock.Load(), f.clock.Load()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB().ExecContext(t.Context(), `INSERT INTO seen_items(task_id,item_id,first_seen_at) VALUES(?,'task-favorite',?)`, task.ID, f.clock.Load()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB().ExecContext(t.Context(), `INSERT INTO findings(task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read) VALUES(?,'task-favorite',1,?,0,'matched','done',0)`, task.ID, f.clock.Load()); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request(http.MethodPost, path+"/no-good-hint/dismiss", "", true), http.StatusNoContent)
	get := decodeBody[service.TaskView](t, f.request(http.MethodGet, path, "", true))
	if get.NoGoodHintDismissedAt == nil || *get.NoGoodHintDismissedAt != f.clock.Load() {
		t.Fatal("提示关闭时间应由服务写当前时间")
	}
	requireStatus(t, f.request(http.MethodDelete, path, "", true), http.StatusNoContent)
	requireStatus(t, f.request(http.MethodGet, path, "", true), http.StatusNotFound)
	var favorite, findings, seen int64
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT is_favorite FROM listings WHERE item_id='task-favorite'`).Scan(&favorite); err != nil || favorite != 1 {
		t.Fatal("删任务不能删除独立收藏")
	}
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM findings WHERE task_id=?`, task.ID).Scan(&findings); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM seen_items WHERE task_id=?`, task.ID).Scan(&seen); err != nil || seen != 0 || findings != 0 {
		t.Fatal("删任务应清自己的发现和见过关系")
	}
}

func TestBusinessTaskMarketDictionaryAndRegionsAreReadOnly(t *testing.T) {
	f := newBusinessFixture(t)
	created := f.request(http.MethodPost, "/api/v1/tasks", `{"keyword":"相机","enabled":false}`, true)
	requireStatus(t, created, http.StatusCreated)
	task := decodeBody[service.TaskSaveResult](t, created).Task
	path := "/api/v1/tasks/" + strconv.FormatInt(task.ID, 10)
	market := f.request(http.MethodGet, path+"/market", "", true)
	requireStatus(t, market, http.StatusOK)
	if value := decodeBody[service.MarketView](t, market); len(value.Market.Groups) != 0 || value.Insight != nil {
		t.Fatal("没有样本时应返回真实空行情，不伪造市场价")
	}
	samples := f.request(http.MethodGet, path+"/market/samples?limit=10&brand_new=false", "", true)
	requireStatus(t, samples, http.StatusOK)
	if value := decodeBody[service.Page[service.MarketSampleView]](t, samples); value.Total != 0 || value.Items == nil || value.NextCursor != nil {
		t.Fatal("空样本应保留空数组和可空游标")
	}
	snapshot, err := f.settings.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var timezone string
	if err := json.Unmarshal(snapshot.Values["general.timezone"], &timezone); err != nil {
		t.Fatal(err)
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		t.Fatal(err)
	}
	day := time.UnixMilli(f.clock.Load()).In(location).Format(time.DateOnly)
	for index := 0; index < 5; index++ {
		itemID, price := fmt.Sprintf("market-%d", index), 10000+index*100
		if _, err := f.store.DB().ExecContext(t.Context(), `INSERT INTO listings(item_id,title,price,desktop_url,first_seen_at,last_seen_at) VALUES(?,'相机',?,'https://example.invalid/item',?,?)`, itemID, price, f.clock.Load(), f.clock.Load()); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.DB().ExecContext(t.Context(), `INSERT INTO task_sightings(task_id,item_id,day,price,last_seen_at) VALUES(?,?,?,?,?)`, task.ID, itemID, day, price, f.clock.Load()); err != nil {
			t.Fatal(err)
		}
	}
	populated := f.request(http.MethodGet, path+"/market/samples?limit=2&brand_new=false", "", true)
	requireStatus(t, populated, http.StatusOK)
	page := decodeBody[service.Page[service.MarketSampleView]](t, populated)
	if page.Total != 5 || len(page.Items) != 2 || page.Items[0].Price != 10000 || page.Items[1].Price != 10100 || page.Items[0].ConfigKey != "*" || page.NextCursor == nil {
		t.Fatal("行情样本必须读取真实价格并按价格稳定分页")
	}
	dictionary := f.request(http.MethodGet, "/api/v1/risk-dictionary", "", true)
	requireStatus(t, dictionary, http.StatusOK)
	if value := decodeBody[[]analysis.RiskDefinition](t, dictionary); !reflect.DeepEqual(value, analysis.RiskDictionary()) {
		t.Fatal("公开字典不能维护另一套内容")
	}
	regions := f.request(http.MethodGet, "/api/v1/regions", "", true)
	requireStatus(t, regions, http.StatusOK)
	provinces := decodeBody[[]service.RegionNode](t, regions)
	if len(provinces) == 0 || provinces[0].ID == "" {
		t.Fatal("地区应来自随发布的数据")
	}
	children := f.request(http.MethodGet, "/api/v1/regions?parent="+url.QueryEscape(provinces[0].ID), "", true)
	requireStatus(t, children, http.StatusOK)
	nodes := decodeBody[[]service.RegionNode](t, children)
	if len(nodes) == 0 {
		t.Fatal("地区父节点应能读取下一层实际选项")
	}
	for _, node := range nodes {
		if node.ParentID == nil || *node.ParentID != provinces[0].ID {
			t.Fatal("地区层级没有沿用父标识")
		}
	}
	requireStatus(t, f.request(http.MethodGet, "/api/v1/regions?parent=unknown-parent", "", true), http.StatusBadRequest)
	for _, table := range []string{"run_records", "quota_events"} {
		var count int64
		if err := f.store.DB().QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatal("行情和地区GET不能触发浏览器访问")
		}
	}
}

func TestBusinessTaskRuntimeReadsDoNotRunAndBlockedRunHasNoInventedTime(t *testing.T) {
	f := newBusinessFixture(t)
	created := f.request(http.MethodPost, "/api/v1/tasks", `{"keyword":"相机","enabled":false}`, true)
	requireStatus(t, created, http.StatusCreated)
	task := decodeBody[service.TaskSaveResult](t, created).Task
	path := "/api/v1/tasks/" + strconv.FormatInt(task.ID, 10)
	read := f.request(http.MethodGet, path+"/runtime", "", true)
	requireStatus(t, read, http.StatusOK)
	if value := decodeBody[service.TaskRuntime](t, read); value.Phase != service.TaskIdle || value.RunID != nil {
		t.Fatal("没有请求运行时不能伪造运行态")
	}
	var count int64
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM run_records`).Scan(&count); err != nil || count != 0 {
		t.Fatal("读取运行态不能写运行记录")
	}
	blocked := f.request(http.MethodPost, path+"/run", "", true)
	requireStatus(t, blocked, http.StatusConflict)
	err := decodeBody[ErrorEnvelope](t, blocked)
	if err.Error.Code != "run.no_account" {
		t.Fatal("无账号应返回真实前置门禁原因")
	}
	if value, exists := err.Error.Args["next_allowed_at"]; !exists || value != nil {
		t.Fatal("无账号不能编一个未来时间假装会自动恢复")
	}
}

func TestBusinessTaskExclusionsRequireExplicitApply(t *testing.T) {
	f := newBusinessFixture(t)
	created := f.request(http.MethodPost, "/api/v1/tasks", `{"keyword":"相机","enabled":false}`, true)
	requireStatus(t, created, http.StatusCreated)
	task := decodeBody[service.TaskSaveResult](t, created).Task
	path := "/api/v1/tasks/" + strconv.FormatInt(task.ID, 10)
	for index, favorite := range []int{0, 1} {
		itemID := fmt.Sprintf("exclusion-%d", index)
		if _, err := f.store.DB().ExecContext(t.Context(), `INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at,is_favorite) VALUES(?,'有划痕的相机','https://example.invalid/item',?,?,?)`, itemID, f.clock.Load(), f.clock.Load(), favorite); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.DB().ExecContext(t.Context(), `INSERT INTO seen_items(task_id,item_id,first_seen_at) VALUES(?,?,?)`, task.ID, itemID, f.clock.Load()); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.DB().ExecContext(t.Context(), `INSERT INTO findings(task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read) VALUES(?,?,1,?,0,'matched','done',0)`, task.ID, itemID, f.clock.Load()); err != nil {
			t.Fatal(err)
		}
	}
	patch := f.request(http.MethodPatch, path, `{"exclude_keywords":["划痕"]}`, true)
	requireStatus(t, patch, http.StatusOK)
	changed := decodeBody[service.TaskSaveResult](t, patch)
	if changed.Effects.ExistingExcludeMatchCount != 1 || !reflect.DeepEqual(changed.Effects.AddedExcludeWords, []string{"划痕"}) || changed.Effects.SearchReset {
		t.Fatal("保存排除词应只给数量和可确认的词，不重置基线")
	}
	var before int64
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM findings WHERE task_id=? AND decision='matched'`, task.ID).Scan(&before); err != nil || before != 2 {
		t.Fatal("保存条件不能悄悄隐藏已有结果")
	}
	apply := f.request(http.MethodPost, path+"/apply-exclusions", `{"added_exclude_words":["划痕"]}`, true)
	requireStatus(t, apply, http.StatusOK)
	if decodeBody[service.MutationResult](t, apply).Applied != 1 {
		t.Fatal("确认只应隐藏非收藏的匹配结果")
	}
	var decision, favoriteDecision string
	var seen int64
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT decision FROM findings WHERE task_id=? AND item_id='exclusion-0'`, task.ID).Scan(&decision); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT decision FROM findings WHERE task_id=? AND item_id='exclusion-1'`, task.ID).Scan(&favoriteDecision); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM seen_items WHERE task_id=?`, task.ID).Scan(&seen); err != nil || decision != "rejected" || favoriteDecision != "matched" || seen != 2 {
		t.Fatal("确认排除必须保留收藏和见过记录")
	}
	requireStatus(t, f.request(http.MethodPost, path+"/apply-exclusions", `{"added_exclude_words":["没有保存的词"]}`, true), http.StatusConflict)
	for _, body := range []string{`{}`, `{"added_exclude_words":null}`} {
		requireStatus(t, f.request(http.MethodPost, path+"/apply-exclusions", body, true), http.StatusBadRequest)
	}
}

type taskBlockingFactory struct {
	opened   chan context.Context
	released chan struct{}
	calls    atomic.Int64
	close    sync.Once
}

func (f *taskBlockingFactory) Open(ctx context.Context, _ service.ResourceSpec) (service.SessionResource, error) {
	f.calls.Add(1)
	select {
	case f.opened <- ctx:
	default:
	}
	<-ctx.Done()
	f.close.Do(func() { close(f.released) })
	return nil, ctx.Err()
}

func TestBusinessTaskRunIsAcceptedAndOutlivesHTTPRequest(t *testing.T) {
	factory := &taskBlockingFactory{opened: make(chan context.Context, 1), released: make(chan struct{})}
	f := newBusinessFixture(t, factory)
	if _, err := f.store.DB().ExecContext(t.Context(), `INSERT INTO accounts(name,state,created_at) VALUES('只用于假资源的账号','active',?)`, f.clock.Load()); err != nil {
		t.Fatal(err)
	}
	created := f.request(http.MethodPost, "/api/v1/tasks", `{"keyword":"相机","enabled":false}`, true)
	requireStatus(t, created, http.StatusCreated)
	task := decodeBody[service.TaskSaveResult](t, created).Task
	path := "/api/v1/tasks/" + strconv.FormatInt(task.ID, 10)
	requestContext, cancelRequest := context.WithCancel(t.Context())
	defer cancelRequest()
	accepted := f.request(http.MethodPost, path+"/run", "", true, func(r *http.Request) { *r = *r.WithContext(requestContext) })
	requireStatus(t, accepted, http.StatusAccepted)
	submission := decodeBody[service.RunSubmission](t, accepted)
	if submission.TaskID != task.ID || submission.Status != service.SubmissionQueued && submission.Status != service.SubmissionStarted {
		t.Fatal("202只应说明同一个任务已接纳或开始")
	}
	cancelRequest()
	var workContext context.Context
	select {
	case workContext = <-factory.opened:
	case <-time.After(3 * time.Second):
		t.Fatal("接纳的工作没有真正进入资源工厂")
	}
	if workContext.Err() != nil {
		t.Fatal("请求断开不能取消已经接纳的工作")
	}
	runtime := f.request(http.MethodGet, path+"/runtime", "", true)
	requireStatus(t, runtime, http.StatusOK)
	if value := decodeBody[service.TaskRuntime](t, runtime); value.TaskID != task.ID || value.Phase == service.TaskIdle {
		t.Fatal("运行态必须来自真实所有者")
	}
	requireStatus(t, f.request(http.MethodPost, path+"/run", "", true), http.StatusConflict)
	requireStatus(t, f.request(http.MethodGet, path+"/run", "", true), http.StatusMethodNotAllowed)
	if factory.calls.Load() != 1 {
		t.Fatal("重复提交或GET不能另开资源")
	}
	requireStatus(t, f.request(http.MethodPost, path+"/cancel", "", true), http.StatusNoContent)
	select {
	case <-factory.released:
	case <-time.After(3 * time.Second):
		t.Fatal("明确取消后资源没有收回")
	}
	if workContext.Err() == nil {
		t.Fatal("明确取消必须传到真实执行上下文")
	}
}
