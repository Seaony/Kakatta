package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func TestKeywordConclusionPersistsCurrentMatchedWordsAndReason(t *testing.T) {
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeKeyword, false)
	work := h.finding(ref, "matched", StageDetail, DecisionPending, h.now)
	h.exec(`UPDATE tasks SET match_keywords_json='["original","q1","ORIGINAL"]' WHERE id=?`, ref.TaskID)
	h.exec(`UPDATE listings SET title='Ｑ１ q1r5',description='ORIGINAL 配件齐全' WHERE item_id='matched'`)
	h.exec(`UPDATE findings SET matched_keywords_json='["过期词"]' WHERE id=?`, work.FindingID)
	tx := h.tx()
	first, err := h.s.saveConclusionTx(t.Context(), tx, work, Conclusion{Layer: &analysis.LayerResult{MatchedKeywords: []string{"旧请求的词"}}}, h.now)
	if err != nil || !first {
		t.Fatalf("当前关键词结论未保存：first=%t err=%v", first, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got := h.read(work.FindingID)
	if got.Decision != analysis.DecisionMatched || got.Stage != StageDone || !reflect.DeepEqual(got.MatchedKeywords, []string{"original", "q1"}) {
		t.Fatalf("落库命中词没有按当前规则规整、去重并保留顺序：%+v", got)
	}
	if got.Reason == nil || got.Reason.Code != "analysis.keyword_matched" || len(got.Reason.Args) != 2 || got.Reason.Args["count"] != json.Number("2") || got.Reason.Args["words"] != "original、q1" {
		t.Fatalf("命中原因与落库的完整词表不一致：%+v", got.Reason)
	}
}

func TestRunInsightRequiresSuccessfulRunAndUsableProvider(t *testing.T) {
	for _, test := range []struct {
		name          string
		cause         error
		enabled       bool
		providerError bool
		wantCalls     int
		outcome       string
	}{
		{name: "成功且服务商可用", enabled: true, wantCalls: 1, outcome: "success"},
		{name: "页面失败但其他条件满足", cause: errors.New("本地页面失败"), enabled: true, outcome: "failed"},
		{name: "额度跳过但其他条件满足", cause: web.ErrQuota, enabled: true, outcome: "skipped"},
		{name: "取消但其他条件满足", cause: errUserCancelled, enabled: true, outcome: "failed"},
		{name: "成功但服务商停用", outcome: "success"},
		{name: "成功但服务商有错误", enabled: true, providerError: true, outcome: "success"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, run, _, now := finishFixture(t)
			seedFinishMarket(t, s, now, 10, true)
			finishExec(t, s.db, `UPDATE ai_providers SET enabled=? WHERE id=1`, test.enabled)
			if test.providerError {
				finishExec(t, s.db, `UPDATE ai_providers SET error_state='{"code":"analysis.client.auth","args":{}}' WHERE id=1`)
			}
			calls := 0
			s.models = finishModels(func(_ context.Context, request client.Request) (client.Result, error) {
				calls++
				if request.Kind != client.Market {
					t.Errorf("只应生成行情解读：%+v", request)
				}
				return client.Result{Text: "本地行情解读", ProviderID: 1, Model: "fixture"}, nil
			})
			if err := s.finishRun(t.Context(), run, test.cause); err != nil {
				t.Fatal(err)
			}
			s.jobs.Wait()
			if calls != test.wantCalls || finishScalar[int](t, s.db, `SELECT count(*) FROM market_insights`) != test.wantCalls {
				t.Fatalf("解读越过运行结果或服务商门禁：calls=%d want=%d", calls, test.wantCalls)
			}
			if got := finishScalar[string](t, s.db, `SELECT outcome FROM run_records WHERE id=1`); got != test.outcome {
				t.Fatalf("解读资格改变了运行结果：got=%s want=%s", got, test.outcome)
			}
		})
	}
}

func TestRunInsightSixHourBoundary(t *testing.T) {
	for _, age := range []time.Duration{6*time.Hour - time.Millisecond, 6 * time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			s, run, _, now := finishFixture(t)
			seedFinishMarket(t, s, now, 10, true)
			finishExec(t, s.db, `INSERT INTO market_insights(task_id,generated_at,text) VALUES(1,?,'旧解读')`, now.Add(-age).UnixMilli())
			calls := 0
			s.models = finishModels(func(context.Context, client.Request) (client.Result, error) {
				calls++
				return client.Result{Text: "新解读", ProviderID: 1, Model: "fixture"}, nil
			})
			if err := s.generateRunInsight(t.Context(), run); err != nil {
				t.Fatal(err)
			}
			want := 0
			if age == 6*time.Hour {
				want = 1
			}
			if calls != want || finishScalar[int](t, s.db, `SELECT count(*) FROM market_insights`) != 1+want {
				t.Fatalf("六小时边界错误：age=%v calls=%d want=%d", age, calls, want)
			}
		})
	}
}

func TestCleanupUsesEachRetentionSettingAtItsExactBoundary(t *testing.T) {
	db, values, now := cleanupDB(t)
	for key, days := range map[string]string{
		"retention.findings_days": "7", "retention.stats_days": "11", "retention.run_steps_days": "3",
		"retention.seller_profile_days": "5", "retention.orphan_listing_days": "13",
	} {
		values[key] = json.RawMessage(days)
	}
	day := 24 * time.Hour.Milliseconds()
	cleanupTask(t, db, 1)
	cleanupExec(t, db, `INSERT INTO run_records(id,task_id,task_name,trigger,started_at,ended_at,outcome) VALUES(3,1,'保留运行','manual',?,?,'success')`, now, now)
	cleanupExec(t, db, `INSERT INTO market_insights(id,task_id,generated_at) VALUES(3,1,?)`, now)
	for i, suffix := range []string{"older", "boundary"} {
		id, offset := int64(i+1), int64(i-1)
		cleanupListing(t, db, "finding-"+suffix, now, false, false)
		cleanupFinding(t, db, id, 1, "finding-"+suffix, now-7*day+offset)
		cleanupListing(t, db, "orphan-"+suffix, now-13*day+offset, false, false)
		cleanupExec(t, db, `INSERT INTO price_points(item_id,price,observed_at) VALUES(?,100,?)`, "orphan-"+suffix, now)
		cleanupListing(t, db, "sighting-"+suffix, now, false, false)
		cleanupExec(t, db, `INSERT INTO task_sightings(task_id,item_id,day,price,last_seen_at) VALUES(1,?,'2026-10-31',100,?)`, "sighting-"+suffix, now-11*day+offset)
		cleanupExec(t, db, `INSERT INTO task_item_specs(task_id,item_id,source,updated_at) VALUES(1,?,'user',?)`, "sighting-"+suffix, now)
		cleanupExec(t, db, `INSERT INTO seller_profiles(seller_id,fetched_at) VALUES(?,?)`, suffix, now-5*day+offset)
		cleanupExec(t, db, `INSERT INTO market_insights(id,task_id,generated_at) VALUES(?,1,?)`, id, now-11*day+offset)
		cleanupExec(t, db, `INSERT INTO run_records(id,task_id,task_name,trigger,started_at,ended_at,outcome) VALUES(?,1,?,'manual',?,?,'success')`, id, suffix, now-11*day+offset, now-11*day+offset)
		cleanupExec(t, db, `INSERT INTO run_steps(run_id,seq,at,status) VALUES(3,?,?,'ok')`, id, now-3*day+offset)
		cleanupExec(t, db, `INSERT INTO ai_calls(id,at,task_id) VALUES(?,?,1)`, id, now-11*day+offset)
		cleanupExec(t, db, `INSERT INTO deliveries(id,at,task_id) VALUES(?,?,1)`, id, now-11*day+offset)
		cleanupExec(t, db, `INSERT INTO notification_intents(id,finding_id,task_id,kind,state,created_at,updated_at) VALUES(?,?,1,'recommended','sent',?,?)`, id, id, now-11*day+offset, now-11*day+offset)
		cleanupExec(t, db, `INSERT INTO system_events(id,kind,subject_id,created_at,resolved_at) VALUES(?,'xianyu_layout',?,?,?)`, id, id, now-11*day+offset, now-11*day+offset)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := cleanupSQLTx(t.Context(), tx, values, now, nil, nil, now-day); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"findings", "market_insights", "run_records", "ai_calls", "deliveries", "notification_intents", "system_events"} {
		for id, want := range map[int64]bool{1: false, 2: true} {
			if got := cleanupHas(t, db, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE id=?)", id); got != want {
				t.Errorf("%s id=%d保留=%t，预期%t；各类保留期不能互相替代", table, id, got, want)
			}
		}
	}
	for _, test := range []struct {
		query    string
		older    any
		boundary any
	}{
		{`SELECT EXISTS(SELECT 1 FROM listings WHERE item_id=?)`, "orphan-older", "orphan-boundary"},
		{`SELECT EXISTS(SELECT 1 FROM price_points WHERE item_id=?)`, "orphan-older", "orphan-boundary"},
		{`SELECT EXISTS(SELECT 1 FROM task_sightings WHERE item_id=?)`, "sighting-older", "sighting-boundary"},
		{`SELECT EXISTS(SELECT 1 FROM task_item_specs WHERE item_id=?)`, "sighting-older", "sighting-boundary"},
		{`SELECT EXISTS(SELECT 1 FROM seller_profiles WHERE seller_id=?)`, "older", "boundary"},
		{`SELECT EXISTS(SELECT 1 FROM run_steps WHERE run_id=3 AND seq=?)`, int64(1), int64(2)},
	} {
		if cleanupHas(t, db, test.query, test.older) || !cleanupHas(t, db, test.query, test.boundary) {
			t.Errorf("保留期边界错误：%s", test.query)
		}
	}
}

func TestCleanupPreservesEveryPermanentObjectAndItsValues(t *testing.T) {
	db, values, now := cleanupDB(t)
	old := now - 200*24*time.Hour.Milliseconds()
	cleanupExec(t, db, `INSERT INTO proxies(id,uuid,name,url_enc,created_at) VALUES(1,'local-proxy','保留代理',X'010203',?)`, old)
	cleanupExec(t, db, `INSERT INTO accounts(id,name,proxy_id,nickname,state,cooldown_until,verify_level,created_at) VALUES(1,'保留账号',1,'昵称','cooling',?,3,?)`, now+1000, old)
	cleanupTask(t, db, 1)
	cleanupExec(t, db, `UPDATE tasks SET account_id=1,requirement='保留需求',created_at=? WHERE id=1`, old)
	cleanupExec(t, db, `INSERT INTO seen_items(task_id,item_id,first_seen_at) VALUES(1,'旧商品',?)`, old)
	cleanupExec(t, db, `INSERT INTO blocked_sellers(seller_id,nick,created_at) VALUES('屏蔽卖家','卖家昵称',?)`, old)
	cleanupListing(t, db, "favorite", old, true, false)
	cleanupFinding(t, db, 1, 1, "favorite", old)
	cleanupExec(t, db, `INSERT INTO settings(key,value_json,updated_at) VALUES('general.timezone','"Asia/Shanghai"',?)`, old)
	cleanupExec(t, db, `INSERT INTO prompts(key,content,updated_at) VALUES('market','保留提示词',?)`, old)
	cleanupExec(t, db, `INSERT INTO channels(id,uuid,type,name,config_json,secret_enc) VALUES(1,'local-channel','webhook','保留渠道','{"method":"POST"}',X'040506')`)
	cleanupExec(t, db, `INSERT INTO task_channels(task_id,channel_id) VALUES(1,1)`)
	cleanupExec(t, db, `INSERT INTO ai_providers(id,uuid,name,base_url,api_key_enc,vision_model,text_model,sort_index) VALUES(1,'local-provider','保留服务商','http://models.invalid',X'070809','vision','text',1)`)
	tables := []string{"proxies", "accounts", "tasks", "seen_items", "blocked_sellers", "listings", "findings", "settings", "prompts", "channels", "task_channels", "ai_providers"}
	snapshot := func() map[string][][]any {
		t.Helper()
		result := map[string][][]any{}
		for _, table := range tables {
			rows, err := db.QueryContext(t.Context(), "SELECT * FROM "+table+" ORDER BY 1")
			if err != nil {
				t.Fatal(err)
			}
			columns, err := rows.Columns()
			if err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for rows.Next() {
				values, targets := make([]any, len(columns)), make([]any, len(columns))
				for i := range values {
					targets[i] = &values[i]
				}
				if err := rows.Scan(targets...); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				for i, value := range values {
					if data, ok := value.([]byte); ok {
						values[i] = string(data)
					}
				}
				result[table] = append(result[table], values)
			}
			readErr, closeErr := rows.Err(), rows.Close()
			if readErr != nil || closeErr != nil {
				t.Fatal(errors.Join(readErr, closeErr))
			}
		}
		return result
	}
	before := snapshot()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := cleanupSQLTx(t.Context(), tx, values, now, nil, nil, now-24*time.Hour.Milliseconds()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	after := snapshot()
	for _, table := range tables {
		if len(before[table]) == 0 || !reflect.DeepEqual(before[table], after[table]) {
			t.Errorf("自动清理改动了永久保留的%s：before=%v after=%v", table, before[table], after[table])
		}
	}
}
