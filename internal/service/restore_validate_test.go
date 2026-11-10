package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/settings"
)

func storedValidationFixture(t *testing.T) (findingsHarness, *security.Cipher, WorkRef) {
	t.Helper()
	h := newFindingsHarness(t)
	cipher, err := security.NewCipher(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ref := h.task(analysis.ModeAI, false)
	work := h.finding(ref, "item", StageDone, analysis.DecisionScored, h.now)
	h.exec(`UPDATE tasks SET profile_json=? WHERE id=?`, `{"musts":[{"id":"m1","text":"需求","source":"user","rev":2}],"risks":[{"id":"c2","name":"风险","attitude":"mind","source":"user","rev":2}],"next_no":3}`, ref.TaskID)
	h.exec(`UPDATE listings SET image_urls_json='["https://example.invalid/a"]',tags_json='[]',description='已改过的描述',seller_id='expired-cache' WHERE item_id='item'`)
	h.exec(`UPDATE findings SET analysis_json=?,user_overrides_json=?,reason=?,score_breakdown_json=? WHERE id=?`,
		`{"specs":[{"name":"已删除规格","value":"旧值"}],"musts":[{"id":"m99","rev":1,"status":"met","source":"text","evidence":"原描述已不存在"}],"checks":[{"key":"c99","rev":0,"status":"risk","source":"image","evidence":"原图片"}],"extra_findings":[{"name":"旧发现","evidence":"旧证据","source":"text"}]}`,
		`{"risks":{"c99":{"status":"ok","rev":1}}}`, `{"code":"historical.code","args":{"large":9007199254740993123456789,"nested":{"external":[1e1000,null]}}}`,
		`{"items":[],"caps":[],"gates":[{"code":"old.gate","args":{"raw":1e1000}}],"historical_extension":{"raw":1e1000}}`, work.FindingID)
	h.exec(`INSERT INTO seen_items(task_id,item_id,first_seen_at) VALUES(?,'already-cleaned',?)`, ref.TaskID, h.now)
	h.exec(`INSERT INTO task_sightings(task_id,item_id,day,price,last_seen_at) VALUES(?,'item','2026-11-11',1000,?)`, ref.TaskID, h.now)
	h.exec(`INSERT INTO task_item_specs(task_id,item_id,specs_json,source,updated_at) VALUES(?,'item','[{"name":"旧规格","value":"旧值"}]','user',?)`, ref.TaskID, h.now)
	h.exec(`INSERT INTO task_item_specs(task_id,item_id,specs_json,source,updated_at) VALUES(?,'already-cleaned','[]','title',?)`, ref.TaskID, h.now)
	h.exec(`INSERT INTO accounts(id,name,state,verify_level,next_gap_seconds,state_reason) VALUES(1,'账号','signed_out',0,0,?)`, `{"code":"old.account","args":{"raw":1e1000}}`)
	h.exec(`UPDATE tasks SET account_id=1,last_error=? WHERE id=?`, `{"code":"old.task","args":{"raw":1e1000}}`, ref.TaskID)
	sealed, err := cipher.Seal([]byte("http://private-user:private-password@localhost:8123"), "proxies", "url_enc", "proxy-one")
	if err != nil {
		t.Fatal(err)
	}
	h.exec(`INSERT INTO proxies(id,uuid,name,url_enc) VALUES(1,'proxy-one','代理',?)`, sealed)
	h.exec(`UPDATE accounts SET proxy_id=1 WHERE id=1`)
	h.exec(`INSERT INTO seller_profiles(seller_id,recent_items_json,recent_rates_json,fetched_at) VALUES('null-cache',NULL,'[]',?)`, h.now)
	h.exec(`INSERT INTO run_records(id,task_name,trigger,account_id,started_at,ended_at,outcome,message,warnings_json) VALUES(1,'已删任务','auto',1,?,?, 'success',?,'[]')`, h.now-1, h.now, `{"code":"old.run","args":{"raw":1e1000}}`)
	h.exec(`INSERT INTO run_records(id,task_name,trigger,account_id,started_at,outcome,repeat_count,warnings_json) VALUES(2,'已删账号','auto',999,?,'skipped',12,'[]')`, h.now)
	h.exec(`INSERT INTO run_steps(run_id,seq,at,status,message) VALUES(1,101,?,'fail',?)`, h.now, `{"code":"old.step","args":{"raw":1e1000}}`)
	h.exec(`INSERT INTO system_events(kind,subject_id,message,created_at,resolved_at) VALUES('account_login',999,?,?,?)`, `{"code":"old.event","args":{"raw":1e1000}}`, h.now, h.now)
	h.exec(`INSERT INTO market_insights(task_id,generated_at,stats_json,provider_id) VALUES(?,?,?,999)`, ref.TaskID, h.now, `{"groups":[],"warnings":[{"code":"old.market","args":{"raw":1e1000}}]}`)
	for key, value := range map[string]string{
		"task.latest_search." + fmt.Sprint(ref.TaskID):   `{"run_id":999,"search_generation":1,"item_ids":["already-cleaned"]}`,
		"task.profile_next_no." + fmt.Sprint(ref.TaskID): "10",
		"runtime.session.1":                              fmt.Sprintf(`{"version":1,"session_id":"old-hold","started_at":%d,"run_id":1}`, h.now),
		"account.cleanup.2":                              "1", "account.logged_out.1": "1", "stats.coverage.run_records": "1",
		"unknown.external": `{"raw":1e1000,"private":"keep exactly"}`,
	} {
		h.exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	}
	return h, cipher, work
}

func storedSnapshot(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, table := range []string{"settings", "prompts", "meta", "tasks", "accounts", "proxies", "findings", "listings", "price_points", "seller_profiles", "task_sightings", "task_item_specs", "seen_items", "market_insights", "run_records", "run_steps", "system_events"} {
		rows, err := db.QueryContext(t.Context(), "SELECT * FROM "+table+" ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var values []any
		for rows.Next() {
			row, targets := make([]any, len(columns)), make([]any, len(columns))
			for i := range row {
				targets[i] = &row[i]
			}
			if err := rows.Scan(targets...); err != nil {
				t.Fatal(err)
			}
			values = append(values, row)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		raw, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		result[table] = string(raw)
	}
	return result
}

func TestValidateStoredKeepsHistoricalDataAndIsReadOnly(t *testing.T) {
	h, cipher, _ := storedValidationFixture(t)
	before := storedSnapshot(t, h.s.db)
	h.exec(`PRAGMA query_only=ON`)
	if err := ValidateStored(t.Context(), h.s.db, cipher, settings.Environment{TZ: "UTC"}); err != nil {
		t.Fatal(err)
	}
	if after := storedSnapshot(t, h.s.db); !reflect.DeepEqual(before, after) {
		t.Fatal("只读校验改变了数据")
	}
}

func TestValidateStoredAccountPauseDeadline(t *testing.T) {
	for _, test := range []struct {
		name        string
		state       AccountState
		offset      time.Duration
		wantInvalid bool
	}{
		{name: "cooling_missing", state: AccountCooling, wantInvalid: true},
		{name: "denied_missing", state: AccountDenied, wantInvalid: true},
		{name: "cooling_expired", state: AccountCooling, offset: -time.Minute},
		{name: "cooling_future", state: AccountCooling, offset: time.Minute},
		{name: "denied_expired", state: AccountDenied, offset: -time.Minute},
		{name: "denied_future", state: AccountDenied, offset: time.Minute},
		{name: "active_future", state: AccountActive, offset: time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, cipher, _ := storedValidationFixture(t)
			var deadline *int64
			if test.offset != 0 {
				value := h.now + test.offset.Milliseconds()
				deadline = &value
			}
			h.exec(`UPDATE accounts SET state=?,cooldown_until=? WHERE id=1`, test.state, deadline)
			before := storedSnapshot(t, h.s.db)
			h.exec(`PRAGMA query_only=ON`)
			err := ValidateStored(t.Context(), h.s.db, cipher, settings.Environment{TZ: "UTC"})
			if test.wantInvalid {
				if err == nil || !strings.Contains(err.Error(), "cooldown_until") {
					t.Fatalf("缺少暂停截止时间的账号未被正确拒绝：%v", err)
				}
			} else if err != nil {
				t.Fatalf("兼容的账号暂停截止时间被拒绝：%v", err)
			}
			if after := storedSnapshot(t, h.s.db); !reflect.DeepEqual(before, after) {
				t.Fatal("校验改写了账号暂停状态")
			}
		})
	}
}

func TestValidateStoredRejectsBrokenCoreJSONAndReferences(t *testing.T) {
	for name, query := range map[string]string{
		"task_profile":         `UPDATE tasks SET profile_json='{"musts":[{"id":"m1","text":"需求","source":"wrong"}]}'`,
		"task_overrides":       `UPDATE tasks SET crawl_overrides_json='{"not_supported":1}'`,
		"task_message":         `UPDATE tasks SET last_error='{"args":{}}'`,
		"task_region":          `UPDATE tasks SET region_province='不存在的省份'`,
		"task_null_word":       `UPDATE tasks SET match_keywords_json='[null]'`,
		"facts_syntax":         `UPDATE findings SET analysis_json='{'`,
		"facts_status":         `UPDATE findings SET analysis_json='{"checks":[{"key":"c99","status":"wrong"}]}'`,
		"facts_source":         `UPDATE findings SET analysis_json='{"checks":[{"key":"c99","status":"risk","source":"program"}]}'`,
		"facts_warning":        `UPDATE findings SET analysis_json='{"parse_warnings":[{"code":"not.allowed","args":{}}]}'`,
		"overrides":            `UPDATE findings SET user_overrides_json='{"risks":{"c99":{"status":"wrong","rev":1}}}'`,
		"breakdown":            `UPDATE findings SET score_breakdown_json='[]'`,
		"reason":               `UPDATE findings SET reason='{"code":""}'`,
		"keywords":             `UPDATE findings SET matched_keywords_json='[{}]'`,
		"seller_assessment":    `UPDATE findings SET seller_checks_json='null'`,
		"listing_images":       `UPDATE listings SET image_urls_json='{}'`,
		"listing_price":        `UPDATE listings SET price=-1`,
		"older_price":          `INSERT INTO price_points(item_id,price,observed_at) VALUES('item',-1,1)`,
		"listing_null_image":   `UPDATE listings SET image_urls_json='[null]'`,
		"seller_cache":         `UPDATE seller_profiles SET recent_rates_json='["invalid"]'`,
		"seller_enum":          `UPDATE seller_profiles SET recent_rates_json='[{"kind":"wrong"}]'`,
		"item_specs":           `UPDATE task_item_specs SET specs_json='[{"name":9}]'`,
		"sighting_reference":   `UPDATE task_sightings SET item_id='missing-current-listing'`,
		"market_older":         `INSERT INTO market_insights(task_id,generated_at,stats_json) SELECT id,1,'[]' FROM tasks`,
		"run_warning":          `UPDATE run_records SET warnings_json='[{}]' WHERE id=1`,
		"late_step":            `UPDATE run_steps SET detail='{"code":""}' WHERE seq=101`,
		"system_event":         `UPDATE system_events SET message='null'`,
		"latest_search":        `UPDATE meta SET value='{"run_id":1,"search_generation":1,"item_ids":["a","a"]}' WHERE key LIKE 'task.latest_search.%'`,
		"watermark":            `UPDATE meta SET value='01' WHERE key LIKE 'task.profile_next_no.%'`,
		"clock":                `UPDATE meta SET value='{}' WHERE key='quota.clock'`,
		"session":              `UPDATE meta SET value='{"version":1,"session_id":"hold","started_at":1,"run_id":2}' WHERE key='runtime.session.1'`,
		"cleanup":              `UPDATE meta SET value='0' WHERE key='account.cleanup.2'`,
		"coverage":             `UPDATE meta SET value='0' WHERE key='stats.coverage.run_records'`,
		"meta_task_reference":  `INSERT INTO meta(key,value) VALUES('task.profile_next_no.999','1')`,
		"logged_out_reference": `INSERT INTO meta(key,value) VALUES('account.logged_out.999','1')`,
		"cleanup_key":          `INSERT INTO meta(key,value) VALUES('account.cleanup.01','1')`,
	} {
		t.Run(name, func(t *testing.T) {
			h, cipher, _ := storedValidationFixture(t)
			h.exec(query)
			before := storedSnapshot(t, h.s.db)
			if err := ValidateStored(t.Context(), h.s.db, cipher, settings.Environment{TZ: "UTC"}); err == nil {
				t.Fatal("损坏记录被接受")
			}
			if after := storedSnapshot(t, h.s.db); !reflect.DeepEqual(before, after) {
				t.Fatal("失败校验改变了数据")
			}
		})
	}
}

func TestValidateStoredKeepsModeChangesAndExpiredClockWindows(t *testing.T) {
	h, cipher, _ := storedValidationFixture(t)
	h.exec(`UPDATE tasks SET judge_mode='keyword',notify_policy='recommended',profile_json='{}'`)
	state := quota.ClockState{Version: 1, HighestUTC: h.now, Timezone: "Asia/Shanghai", Windows: []quota.DayWindow{{Start: 1, End: 2}}}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	h.exec(`UPDATE meta SET value=? WHERE key='quota.clock'`, string(raw))
	before := storedSnapshot(t, h.s.db)
	if err := ValidateStored(t.Context(), h.s.db, cipher, settings.Environment{TZ: "UTC"}); err != nil {
		t.Fatal(err)
	}
	if after := storedSnapshot(t, h.s.db); !reflect.DeepEqual(before, after) {
		t.Fatal("校验改了旧模式结论或额度窗口")
	}
}

func TestValidateStoredRejectsImpossibleTaskConfigurations(t *testing.T) {
	for _, test := range []struct {
		name        string
		query       string
		bypassCheck bool
	}{
		{"inverted_prices", `UPDATE tasks SET min_price=200,max_price=100`, true},
		{"empty_ai_profile", `UPDATE tasks SET profile_json='{}'`, false},
		{"only_bonuses_and_risks", `UPDATE tasks SET profile_json='{"bonuses":[{"id":"b1","text":"加分项","source":"user","rev":1}],"risks":[{"id":"fake","attitude":"confirm","source":"user","rev":1}],"next_no":2}'`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, cipher, _ := storedValidationFixture(t)
			if test.bypassCheck {
				h.exec(`PRAGMA ignore_check_constraints=ON`)
			}
			h.exec(test.query)
			if test.bypassCheck {
				h.exec(`PRAGMA ignore_check_constraints=OFF`)
			}
			before := storedSnapshot(t, h.s.db)
			if err := ValidateStored(t.Context(), h.s.db, cipher, settings.Environment{TZ: "UTC"}); err == nil {
				t.Fatal("正式入口不允许的任务配置被接受")
			}
			if after := storedSnapshot(t, h.s.db); !reflect.DeepEqual(before, after) {
				t.Fatal("校验改写了坏任务配置")
			}
		})
	}
}

func TestValidateStoredKeepsValidTaskConfigurationBoundaries(t *testing.T) {
	for name, query := range map[string]string{
		"equal_price":         `UPDATE tasks SET min_price=100,max_price=100`,
		"only_minimum":        `UPDATE tasks SET min_price=100,max_price=NULL`,
		"only_maximum":        `UPDATE tasks SET min_price=NULL,max_price=100`,
		"specs_without_musts": `UPDATE tasks SET profile_json='{"specs":[{"name":"容量","options":["256G"],"values":["256G"],"source":"user"}],"next_no":1}'`,
	} {
		t.Run(name, func(t *testing.T) {
			h, cipher, _ := storedValidationFixture(t)
			h.exec(query)
			before := storedSnapshot(t, h.s.db)
			if err := ValidateStored(t.Context(), h.s.db, cipher, settings.Environment{TZ: "UTC"}); err != nil {
				t.Fatal("合法任务配置被拒绝", err)
			}
			if after := storedSnapshot(t, h.s.db); !reflect.DeepEqual(before, after) {
				t.Fatal("校验改写了合法任务配置")
			}
		})
	}
}

func TestValidateStoredChecksEveryProxyCipherAndCancels(t *testing.T) {
	for _, corruption := range []string{"wrong_key", "swap_aad", "broken", "bad_url"} {
		t.Run(corruption, func(t *testing.T) {
			h, cipher, _ := storedValidationFixture(t)
			switch corruption {
			case "wrong_key":
				cipher, _ = security.NewCipher(bytes.Repeat([]byte{9}, 32))
			case "swap_aad":
				h.exec(`UPDATE proxies SET uuid='other-row'`)
			case "broken":
				h.exec(`UPDATE proxies SET url_enc=X'00'`)
			case "bad_url":
				sealed, err := cipher.Seal([]byte("https://private-user:private-password@localhost:8123"), "proxies", "url_enc", "proxy-one")
				if err != nil {
					t.Fatal(err)
				}
				h.exec(`UPDATE proxies SET url_enc=?`, sealed)
			}
			err := ValidateStored(t.Context(), h.s.db, cipher, settings.Environment{TZ: "UTC"})
			if err == nil || strings.Contains(err.Error(), "private-password") || strings.Contains(err.Error(), "private-user") {
				t.Fatal("密文校验未拒绝或泄露凭据", err)
			}
		})
	}
	h, cipher, _ := storedValidationFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := ValidateStored(ctx, h.s.db, cipher, settings.Environment{TZ: "UTC"}); err == nil {
		t.Fatal("取消仍通过")
	}
	ctx, cancel = context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := ValidateStored(ctx, h.s.db, cipher, settings.Environment{TZ: "UTC"}); err != nil {
		t.Fatal("单连接校验卡住或错误", err)
	}
}
