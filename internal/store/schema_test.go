package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type schemaColumn struct {
	name, kind string
	notNull    bool
	primaryKey int
	defaultSQL sql.NullString
}

// 每列独立列出类型、非空和主键顺序，避免迁移自己证明自己正确。
// n 表示非空，p 后的数字表示复合主键顺序。
var schemaRequiredColumns = map[string]string{
	"admin": `
		id:INTEGER:p1 username:TEXT:n password_hash:TEXT:n totp_secret_enc:BLOB
		totp_enabled_at:INTEGER totp_last_step:INTEGER created_at:INTEGER:n password_changed_at:INTEGER:n`,
	"auth_tokens": `
		id:INTEGER:p1 kind:TEXT:n token_hash:BLOB:n name:TEXT:n created_at:INTEGER
		last_used_at:INTEGER expires_at:INTEGER revoked_at:INTEGER`,
	"settings": `key:TEXT:np1 value_json:TEXT:n updated_at:INTEGER`,
	"prompts":  `key:TEXT:np1 content:TEXT:n updated_at:INTEGER`,
	"proxies":  `id:INTEGER:p1 uuid:TEXT:n name:TEXT:n url_enc:BLOB:n created_at:INTEGER`,
	"accounts": `
		id:INTEGER:p1 name:TEXT:n proxy_id:INTEGER nickname:TEXT avatar_url:TEXT state:TEXT:n
		state_reason:TEXT state_changed_at:INTEGER cooldown_until:INTEGER verify_level:INTEGER:n
		verify_url:TEXT last_cooldown_end_at:INTEGER cooldown_manual_used:INTEGER:n
		last_run_ended_at:INTEGER next_gap_seconds:INTEGER last_used_at:INTEGER created_at:INTEGER`,
	"quota_events": `id:INTEGER:p1 account_id:INTEGER:n kind:TEXT:n run_id:INTEGER at:INTEGER:n`,
	"tasks": `
		id:INTEGER:p1 name:TEXT:n keyword:TEXT:n enabled:INTEGER:n account_id:INTEGER
		account_pinned:INTEGER:n min_price:INTEGER max_price:INTEGER personal_only:INTEGER:n
		free_shipping_only:INTEGER:n publish_window:TEXT:n region_province:TEXT region_city:TEXT
		region_district:TEXT max_pages:INTEGER:n interval_minutes:INTEGER:n crawl_overrides_json:TEXT:n
		judge_mode:TEXT:n requirement:TEXT:n profile_json:TEXT:n budget_min:INTEGER budget_max:INTEGER
		match_keywords_json:TEXT:n exclude_keywords_json:TEXT:n analyze_on_first_run:INTEGER:n
		notify_policy:TEXT:n notify_price_drop:INTEGER:n search_generation:INTEGER:n
		baseline_pending:INTEGER:n sort_index:INTEGER:n created_at:INTEGER updated_at:INTEGER
		last_run_at:INTEGER last_success_at:INTEGER next_run_at:INTEGER last_run_outcome:TEXT
		last_error:TEXT consecutive_failures:INTEGER:n failure_paused_until:INTEGER
		no_good_hint_dismissed_at:INTEGER`,
	"channels": `
		id:INTEGER:p1 uuid:TEXT:n type:TEXT:n name:TEXT:n enabled:INTEGER:n config_json:TEXT:n
		secret_enc:BLOB last_test_at:INTEGER last_test_ok:INTEGER last_error:TEXT`,
	"task_channels": `task_id:INTEGER:np1 channel_id:INTEGER:np2`,
	"listings": `
		item_id:TEXT:np1 title:TEXT:n price:INTEGER price_text:TEXT original_price_text:TEXT
		thumbnail_url:TEXT image_urls_json:TEXT desktop_url:TEXT:n area:TEXT seller_id:TEXT
		seller_nick:TEXT published_at:INTEGER want_count:INTEGER browse_count:INTEGER tags_json:TEXT
		description:TEXT seller_registration_days:INTEGER zhima_level:TEXT first_seen_at:INTEGER:n
		last_seen_at:INTEGER:n detail_fetched_at:INTEGER is_favorite:INTEGER:n favorited_at:INTEGER
		is_dismissed:INTEGER:n last_drop_amount:INTEGER last_drop_at:INTEGER sale_status:TEXT`,
	"price_points":   `id:INTEGER:p1 item_id:TEXT price:INTEGER:n observed_at:INTEGER:n`,
	"task_sightings": `task_id:INTEGER:np1 item_id:TEXT:np3 day:TEXT:np2 price:INTEGER last_seen_at:INTEGER:n`,
	"task_item_specs": `
		task_id:INTEGER:np1 item_id:TEXT:np2 kind:TEXT specs_json:TEXT brand_new:INTEGER
		config_key:TEXT excluded:INTEGER:n source:TEXT:n updated_at:INTEGER:n`,
	"seller_profiles": `
		seller_id:TEXT:np1 nick:TEXT avatar_url:TEXT introduction:TEXT seller_credit:TEXT buyer_credit:TEXT
		seller_credit_level:TEXT buyer_credit_level:TEXT item_count:INTEGER rate_count:INTEGER
		on_sale_count:INTEGER sold_count:INTEGER seller_good_rate:REAL buyer_good_rate:REAL
		recent_items_json:TEXT recent_rates_json:TEXT fetched_at:INTEGER:n`,
	"blocked_sellers": `seller_id:TEXT:np1 nick:TEXT created_at:INTEGER`,
	"seen_items":      `task_id:INTEGER:np1 item_id:TEXT:np2 first_seen_at:INTEGER`,
	"findings": `
		id:INTEGER:p1 task_id:INTEGER:n item_id:TEXT:n search_generation:INTEGER:n found_at:INTEGER:n
		is_baseline:INTEGER:n decision:TEXT:n reject_kind:TEXT stage:TEXT:n decided_layer:INTEGER
		reason:TEXT analysis_json:TEXT user_overrides_json:TEXT score:INTEGER grade:TEXT score_breakdown_json:TEXT
		fair_price:INTEGER fair_sample:INTEGER fair_basis:TEXT scored_price:INTEGER scored_at:INTEGER
		summary:TEXT seller_type:TEXT title_screen_reason:TEXT matched_keywords_json:TEXT
		analysis_model:TEXT analysis_provider_id:INTEGER analyzed_at:INTEGER attempts:INTEGER:n
		retryable:INTEGER:n reanalyze_requested_at:INTEGER is_read:INTEGER:n notify_kind:TEXT
		notify_skip_reason:TEXT notified_at:INTEGER notify_failures:INTEGER:n recommended_price:INTEGER
		last_drop_notified_price:INTEGER drop_notified_at:INTEGER`,
	"ai_providers": `
		id:INTEGER:p1 uuid:TEXT:n name:TEXT:n preset:TEXT base_url:TEXT:n api_key_enc:BLOB
		vision_model:TEXT:n text_model:TEXT enabled:INTEGER:n sort_index:INTEGER:n error_state:TEXT error_at:INTEGER`,
	"ai_compat": `
		provider_id:INTEGER:np1 model:TEXT:np2 json_mode:TEXT:n send_temperature:INTEGER:n
		max_tokens_param:TEXT:n max_tokens_floor:INTEGER updated_at:INTEGER`,
	"ai_calls": `
		id:INTEGER:p1 at:INTEGER provider_id:INTEGER model:TEXT purpose:TEXT task_id:INTEGER ok:INTEGER
		http_status:INTEGER error_kind:TEXT json_mode:TEXT input_tokens:INTEGER output_tokens:INTEGER
		reasoning_tokens:INTEGER finish_reason:TEXT duration_ms:INTEGER`,
	"deliveries": `
		id:INTEGER:p1 at:INTEGER channel_id:INTEGER task_id:INTEGER kind:TEXT item_count:INTEGER ok:INTEGER error:TEXT`,
	"delivery_items": `delivery_id:INTEGER:np1 finding_id:INTEGER:np2`,
	"run_records": `
		id:INTEGER:p1 task_id:INTEGER task_name:TEXT:n trigger:TEXT:n account_id:INTEGER account_name:TEXT
		proxy_name:TEXT started_at:INTEGER ended_at:INTEGER outcome:TEXT:n failure_kind:TEXT message:TEXT
		repeat_count:INTEGER:n page_count:INTEGER scanned_count:INTEGER new_count:INTEGER layer1_rejected:INTEGER
		layer2_count:INTEGER layer2_rejected:INTEGER detail_count:INTEGER seller_page_count:INTEGER
		analyzed_count:INTEGER recommended_count:INTEGER notified_count:INTEGER warnings_json:TEXT`,
	"run_steps":       `run_id:INTEGER:np1 seq:INTEGER:np2 at:INTEGER status:TEXT message:TEXT detail:TEXT`,
	"system_events":   `id:INTEGER:p1 kind:TEXT subject_id:INTEGER message:TEXT created_at:INTEGER resolved_at:INTEGER notified_at:INTEGER`,
	"market_insights": `id:INTEGER:p1 task_id:INTEGER generated_at:INTEGER stats_json:TEXT text:TEXT provider_id:INTEGER model:TEXT`,
	"meta":            `key:TEXT:np1 value:TEXT:n`,
}

var schemaRequiredDefaults = map[string]map[string]string{
	"accounts": {"verify_level": "0", "cooldown_manual_used": "0"},
	"tasks": {
		"enabled": "1", "account_pinned": "0", "personal_only": "1", "free_shipping_only": "0",
		"publish_window": "'any'", "crawl_overrides_json": "'{}'", "requirement": "''", "profile_json": "'{}'",
		"match_keywords_json": "'[]'", "exclude_keywords_json": "'[]'", "analyze_on_first_run": "1",
		"notify_price_drop": "1", "search_generation": "1", "baseline_pending": "1", "consecutive_failures": "0",
	},
	"channels":        {"enabled": "1"},
	"listings":        {"is_favorite": "0", "is_dismissed": "0"},
	"task_item_specs": {"excluded": "0"},
	"findings":        {"attempts": "0", "retryable": "0", "notify_failures": "0"},
	"ai_providers":    {"enabled": "1"},
	"ai_compat":       {"json_mode": "'schema'", "send_temperature": "1", "max_tokens_param": "'max_tokens'"},
	"run_records":     {"repeat_count": "1"},
}

func schemaOpenDB(t *testing.T) *sql.DB {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "schema.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s.DB()
}

func TestInitialSchemaColumns(t *testing.T) {
	db := schemaOpenDB(t)
	rows, err := db.Query(`SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	var gotTables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		gotTables = append(gotTables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	wantTables := make([]string, 0, len(schemaRequiredColumns))
	for name := range schemaRequiredColumns {
		wantTables = append(wantTables, name)
	}
	sort.Strings(gotTables)
	sort.Strings(wantTables)
	if !reflect.DeepEqual(gotTables, wantTables) {
		t.Fatalf("业务表不一致：got %v, want %v", gotTables, wantTables)
	}
	for table, spec := range schemaRequiredColumns {
		t.Run(table, func(t *testing.T) {
			want := make([]schemaColumn, 0)
			for _, token := range strings.Fields(spec) {
				parts := strings.Split(token, ":")
				column := schemaColumn{name: parts[0], kind: parts[1]}
				if len(parts) == 3 {
					column.notNull = strings.Contains(parts[2], "n")
					if _, number, ok := strings.Cut(parts[2], "p"); ok {
						column.primaryKey, err = strconv.Atoi(number)
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				if value, ok := schemaRequiredDefaults[table][column.name]; ok {
					column.defaultSQL = sql.NullString{String: value, Valid: true}
				}
				want = append(want, column)
			}
			rows, err := db.Query(`SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info(?) ORDER BY cid`, table)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var got []schemaColumn
			for rows.Next() {
				var column schemaColumn
				if err := rows.Scan(&column.name, &column.kind, &column.notNull, &column.defaultSQL, &column.primaryKey); err != nil {
					t.Fatal(err)
				}
				got = append(got, column)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("字段不一致：\ngot  %+v\nwant %+v", got, want)
			}
		})
	}
}

func TestInitialSchemaForeignKeys(t *testing.T) {
	db := schemaOpenDB(t)
	want := map[string][]string{
		"accounts":        {"proxy_id>proxies.id:NO ACTION"},
		"quota_events":    {"account_id>accounts.id:CASCADE"},
		"tasks":           {"account_id>accounts.id:SET NULL"},
		"task_channels":   {"task_id>tasks.id:CASCADE", "channel_id>channels.id:CASCADE"},
		"price_points":    {"item_id>listings.item_id:CASCADE"},
		"task_sightings":  {"task_id>tasks.id:CASCADE"},
		"task_item_specs": {"task_id>tasks.id:CASCADE"},
		"seen_items":      {"task_id>tasks.id:CASCADE"},
		"findings":        {"task_id>tasks.id:CASCADE", "item_id>listings.item_id:NO ACTION"},
		"ai_compat":       {"provider_id>ai_providers.id:CASCADE"},
		"delivery_items":  {"delivery_id>deliveries.id:CASCADE"},
		"run_records":     {"task_id>tasks.id:SET NULL"},
		"run_steps":       {"run_id>run_records.id:CASCADE"},
		"market_insights": {"task_id>tasks.id:CASCADE"},
	}
	for table := range schemaRequiredColumns {
		t.Run(table, func(t *testing.T) {
			rows, err := db.Query(`SELECT "from", "table", "to", on_delete, on_update FROM pragma_foreign_key_list(?)`, table)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var got []string
			for rows.Next() {
				var from, target, to, onDelete, onUpdate string
				if err := rows.Scan(&from, &target, &to, &onDelete, &onUpdate); err != nil {
					t.Fatal(err)
				}
				got = append(got, from+">"+target+"."+to+":"+onDelete)
				if onUpdate != "NO ACTION" {
					t.Errorf("意外的更新联动：%s.%s %s", table, from, onUpdate)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			sort.Strings(got)
			sort.Strings(want[table])
			if !reflect.DeepEqual(got, want[table]) {
				t.Errorf("外键不一致：got %v, want %v", got, want[table])
			}
		})
	}
}

func TestInitialSchemaIndexes(t *testing.T) {
	db := schemaOpenDB(t)
	// 列顺序、是否唯一、是否为部分索引；包含 SQLite 为主键和唯一约束建的索引。
	want := map[string][]string{
		"auth_tokens":     {"token_hash|1|0", "kind,revoked_at|0|0"},
		"settings":        {"key|1|0"},
		"prompts":         {"key|1|0"},
		"proxies":         {"uuid|1|0", "name|1|0"},
		"quota_events":    {"account_id,at|0|0"},
		"tasks":           {"enabled,next_run_at|0|0", "account_id|0|0"},
		"channels":        {"uuid|1|0", "type,name|1|0"},
		"task_channels":   {"task_id,channel_id|1|0"},
		"listings":        {"item_id|1|0", "last_seen_at|0|0", "seller_id|0|0", "is_favorite|0|0"},
		"price_points":    {"item_id,observed_at|0|0"},
		"task_sightings":  {"task_id,day,item_id|1|0"},
		"task_item_specs": {"task_id,item_id|1|0"},
		"seller_profiles": {"seller_id|1|0"},
		"blocked_sellers": {"seller_id|1|0"},
		"seen_items":      {"task_id,item_id|1|0"},
		"findings": {"task_id,item_id|1|0", "task_id,found_at|0|0", "task_id,decision|0|0", "task_id,score|0|0",
			"is_read|0|0", "item_id|0|0"},
		"ai_providers":    {"uuid|1|0"},
		"ai_compat":       {"provider_id,model|1|0"},
		"ai_calls":        {"at|0|0"},
		"deliveries":      {"at|0|0", "channel_id,at|0|0"},
		"delivery_items":  {"delivery_id,finding_id|1|0", "finding_id|0|0"},
		"run_records":     {"started_at|0|0", "task_id,started_at|0|0"},
		"run_steps":       {"run_id,seq|1|0"},
		"system_events":   {"kind,subject_id|1|1"},
		"market_insights": {"task_id,generated_at|0|0"},
		"meta":            {"key|1|0"},
	}
	for table := range schemaRequiredColumns {
		t.Run(table, func(t *testing.T) {
			rows, err := db.Query(`SELECT name, "unique", partial FROM pragma_index_list(?)`, table)
			if err != nil {
				t.Fatal(err)
			}
			type index struct {
				name            string
				unique, partial int
			}
			var indexes []index
			for rows.Next() {
				var index index
				if err := rows.Scan(&index.name, &index.unique, &index.partial); err != nil {
					t.Fatal(err)
				}
				indexes = append(indexes, index)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			var got []string
			for _, index := range indexes {
				rows, err := db.Query(`SELECT name FROM pragma_index_info(?) ORDER BY seqno`, index.name)
				if err != nil {
					t.Fatal(err)
				}
				var columns []string
				for rows.Next() {
					var column string
					if err := rows.Scan(&column); err != nil {
						t.Fatal(err)
					}
					columns = append(columns, column)
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				rows.Close()
				got = append(got, fmt.Sprintf("%s|%d|%d", strings.Join(columns, ","), index.unique, index.partial))
			}
			sort.Strings(got)
			sort.Strings(want[table])
			if !reflect.DeepEqual(got, want[table]) {
				t.Errorf("索引不一致：got %v, want %v", got, want[table])
			}
		})
	}
}

func schemaSeed(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, query := range []string{
		`INSERT INTO admin(id,username,password_hash,created_at,password_changed_at) VALUES(1,'admin','hash',1,1)`,
		`INSERT INTO auth_tokens(id,kind,token_hash,name) VALUES(1,'web_session',x'01','browser')`,
		`INSERT INTO settings(key,value_json) VALUES('general.language','"zh-CN"')`,
		`INSERT INTO prompts(key,content) VALUES('analysis','提示词')`,
		`INSERT INTO proxies(id,uuid,name,url_enc) VALUES(1,'proxy-1','代理',x'01')`,
		`INSERT INTO accounts(id,name,proxy_id,state) VALUES(1,'账号',1,'active')`,
		`INSERT INTO quota_events(id,account_id,kind,at) VALUES(1,1,'search',1)`,
		`INSERT INTO tasks(id,name,keyword,account_id,account_pinned,max_pages,interval_minutes,judge_mode,notify_policy,sort_index) VALUES(1,'任务','手机',1,1,1,30,'keyword','recommended',0)`,
		`INSERT INTO channels(id,uuid,type,name,config_json) VALUES(1,'channel-1','bark','推送','{}')`,
		`INSERT INTO task_channels(task_id,channel_id) VALUES(1,1)`,
		`INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at) VALUES('item-1','商品','商品链接',1,1)`,
		`INSERT INTO price_points(id,item_id,price,observed_at) VALUES(1,'item-1',12345,1)`,
		`INSERT INTO task_sightings(task_id,item_id,day,last_seen_at) VALUES(1,'item-1','2026-11-10',1)`,
		`INSERT INTO task_item_specs(task_id,item_id,source,updated_at) VALUES(1,'item-1','title',1)`,
		`INSERT INTO seller_profiles(seller_id,fetched_at) VALUES('seller-1',1)`,
		`INSERT INTO blocked_sellers(seller_id) VALUES('seller-1')`,
		`INSERT INTO seen_items(task_id,item_id,first_seen_at) VALUES(1,'item-1',1)`,
		`INSERT INTO findings(id,task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read) VALUES(1,1,'item-1',1,1,0,'pending','title',0)`,
		`INSERT INTO ai_providers(id,uuid,name,base_url,vision_model,sort_index) VALUES(1,'provider-1','服务商','模型地址','model-1',0)`,
		`INSERT INTO ai_compat(provider_id,model) VALUES(1,'model-1')`,
		`INSERT INTO ai_calls(id,provider_id,task_id,purpose) VALUES(1,1,1,'analysis')`,
		`INSERT INTO deliveries(id,channel_id,task_id,kind) VALUES(1,1,1,'recommended')`,
		`INSERT INTO delivery_items(delivery_id,finding_id) VALUES(1,1)`,
		`INSERT INTO run_records(id,task_id,task_name,trigger,account_id,outcome) VALUES(1,1,'任务','auto',1,'success')`,
		`INSERT INTO run_steps(run_id,seq,status) VALUES(1,1,'ok')`,
		`INSERT INTO system_events(id,kind,subject_id) VALUES(1,'account_verify',1)`,
		`INSERT INTO market_insights(id,task_id,provider_id) VALUES(1,1,1)`,
		`INSERT INTO meta(key,value) VALUES('clock_high_water','1')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("初始化测试数据：%s: %v", query, err)
		}
	}
}

func schemaTransaction(t *testing.T, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

func schemaExec(t *testing.T, tx *sql.Tx, query string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(query, args...); err != nil {
		t.Fatalf("执行失败：%s: %v", query, err)
	}
}

func schemaReject(t *testing.T, tx *sql.Tx, query string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(query, args...); err == nil {
		t.Fatalf("无效数据被接受：%s %v", query, args)
	}
}

func TestInitialSchemaEnums(t *testing.T) {
	db := schemaOpenDB(t)
	schemaSeed(t, db)
	values := map[string]string{
		"auth_tokens.kind":           "web_session api_token",
		"prompts.key":                "analysis title_screen profile market",
		"accounts.state":             "signed_out active cooling needs_verification denied expired",
		"quota_events.kind":          "search detail",
		"tasks.publish_window":       "any day1 day3 day7 day14",
		"tasks.judge_mode":           "ai keyword",
		"tasks.notify_policy":        "steal good fair all_new none recommended",
		"tasks.last_run_outcome":     "success failed skipped",
		"channels.type":              "bark telegram ntfy gotify wecom dingtalk feishu pushplus serverchan webhook email",
		"listings.sale_status":       "on_sale sold offline",
		"task_item_specs.kind":       "sale wanted accessory recycle rental repair box multi broken other",
		"task_item_specs.source":     "title detail user",
		"findings.decision":          "pending scored matched rejected failed skipped",
		"findings.reject_kind":       "filter excluded junk unmatched keyword_miss",
		"findings.stage":             "title detail done",
		"findings.grade":             "steal good fair pricey avoid",
		"findings.seller_type":       "personal dealer_suspect dealer unknown",
		"findings.notify_kind":       "recommended new",
		"ai_providers.preset":        "openai deepseek modelscope siliconflow openrouter aliyun anthropic ollama custom",
		"ai_compat.json_mode":        "schema object text",
		"ai_compat.max_tokens_param": "max_tokens max_completion_tokens",
		"ai_calls.purpose":           "title_screen analysis profile market test probe",
		"ai_calls.json_mode":         "schema object text",
		"deliveries.kind":            "recommended all_new price_drop summary system test",
		"run_records.trigger":        "auto manual",
		"run_records.outcome":        "success failed skipped",
		"run_records.failure_kind":   "risk_verify risk_deny risk_env login network page timeout quota config cancelled restart",
		"run_steps.status":           "ok warn fail",
		"system_events.kind":         "account_verify account_login account_deny ai_error ai_budget task_failure_pause xianyu_layout login_failures",
	}
	for field, allowed := range values {
		t.Run(field, func(t *testing.T) {
			tx := schemaTransaction(t, db)
			table, column, _ := strings.Cut(field, ".")
			query := "UPDATE " + table + " SET " + column + " = ?"
			for _, value := range strings.Fields(allowed) {
				schemaExec(t, tx, query, value)
			}
			schemaReject(t, tx, query, "invalid")
			schemaReject(t, tx, query, "")
		})
	}
}

func TestInitialSchemaBooleans(t *testing.T) {
	db := schemaOpenDB(t)
	schemaSeed(t, db)
	fields := map[string]bool{
		"accounts.cooldown_manual_used": false,
		"tasks.enabled":                 false, "tasks.account_pinned": false, "tasks.personal_only": false,
		"tasks.free_shipping_only": false, "tasks.analyze_on_first_run": false,
		"tasks.notify_price_drop": false, "tasks.baseline_pending": false,
		"channels.enabled": false, "channels.last_test_ok": true,
		"listings.is_favorite": false, "listings.is_dismissed": false,
		"task_item_specs.brand_new": true, "task_item_specs.excluded": false,
		"findings.is_baseline": false, "findings.retryable": false, "findings.is_read": false,
		"ai_providers.enabled": false, "ai_compat.send_temperature": false,
		"ai_calls.ok": true, "deliveries.ok": true,
	}
	for field, nullable := range fields {
		t.Run(field, func(t *testing.T) {
			tx := schemaTransaction(t, db)
			table, column, _ := strings.Cut(field, ".")
			query := "UPDATE " + table + " SET " + column + " = ?"
			schemaExec(t, tx, query, 0)
			schemaExec(t, tx, query, 1)
			schemaReject(t, tx, query, -1)
			schemaReject(t, tx, query, 2)
			if nullable {
				schemaExec(t, tx, query, nil)
			} else {
				schemaReject(t, tx, query, nil)
			}
		})
	}
}

func TestInitialSchemaBusinessConstraints(t *testing.T) {
	db := schemaOpenDB(t)
	schemaSeed(t, db)
	cases := map[string]func(*testing.T, *sql.Tx){
		"单一管理员": func(t *testing.T, tx *sql.Tx) {
			schemaReject(t, tx, `UPDATE admin SET id=2`)
		},
		"任务名称与关键词长度": func(t *testing.T, tx *sql.Tx) {
			for column, limit := range map[string]int{"name": 30, "keyword": 50} {
				query := "UPDATE tasks SET " + column + "=?"
				schemaExec(t, tx, query, "字")
				schemaExec(t, tx, query, strings.Repeat("字", limit))
				schemaReject(t, tx, query, strings.Repeat("字", limit+1))
				schemaReject(t, tx, query, "")
			}
		},
		"价格范围": func(t *testing.T, tx *sql.Tx) {
			schemaExec(t, tx, `UPDATE tasks SET min_price=0,max_price=0`)
			schemaExec(t, tx, `UPDATE tasks SET min_price=NULL,max_price=100`)
			schemaExec(t, tx, `UPDATE tasks SET min_price=100,max_price=NULL`)
			schemaReject(t, tx, `UPDATE tasks SET min_price=-1`)
			schemaReject(t, tx, `UPDATE tasks SET max_price=-1`)
			schemaReject(t, tx, `UPDATE tasks SET min_price=101,max_price=100`)
		},
		"访问页数与间隔": func(t *testing.T, tx *sql.Tx) {
			for _, column := range []string{"max_pages", "interval_minutes"} {
				query := "UPDATE tasks SET " + column + "=?"
				schemaExec(t, tx, query, 1)
				schemaExec(t, tx, query, 100)
				schemaReject(t, tx, query, 0)
				schemaReject(t, tx, query, -1)
			}
		},
		"分数与判断层": func(t *testing.T, tx *sql.Tx) {
			for _, score := range []any{nil, 0, 100} {
				schemaExec(t, tx, `UPDATE findings SET score=?`, score)
			}
			for _, score := range []int{-1, 101} {
				schemaReject(t, tx, `UPDATE findings SET score=?`, score)
			}
			for _, layer := range []any{nil, 1, 2, 3} {
				schemaExec(t, tx, `UPDATE findings SET decided_layer=?`, layer)
			}
			schemaReject(t, tx, `UPDATE findings SET decided_layer=0`)
			schemaReject(t, tx, `UPDATE findings SET decided_layer=4`)
		},
		"文本和复合主键非空": func(t *testing.T, tx *sql.Tx) {
			schemaReject(t, tx, `INSERT INTO settings(key,value_json) VALUES(NULL,'{}')`)
			schemaReject(t, tx, `INSERT INTO seen_items(task_id,item_id) VALUES(1,NULL)`)
			schemaReject(t, tx, `INSERT INTO ai_compat(provider_id,model) VALUES(NULL,'another')`)
		},
		"不可重复的令牌与身份": func(t *testing.T, tx *sql.Tx) {
			schemaReject(t, tx, `INSERT INTO auth_tokens(kind,token_hash,name) VALUES('api_token',x'01','another')`)
			schemaReject(t, tx, `INSERT INTO proxies(uuid,name,url_enc) VALUES('proxy-1','another',x'02')`)
			schemaReject(t, tx, `INSERT INTO proxies(uuid,name,url_enc) VALUES('proxy-2','代理',x'02')`)
			schemaReject(t, tx, `INSERT INTO ai_providers(uuid,name,base_url,vision_model,sort_index) VALUES('provider-1','another','url','model',1)`)
			schemaReject(t, tx, `INSERT INTO channels(uuid,type,name,config_json) VALUES('channel-1','ntfy','another','{}')`)
			schemaReject(t, tx, `INSERT INTO channels(uuid,type,name,config_json) VALUES('channel-2','bark','推送','{}')`)
			schemaExec(t, tx, `INSERT INTO channels(uuid,type,name,config_json) VALUES('channel-2','ntfy','推送','{}')`)
			schemaReject(t, tx, `INSERT INTO findings(task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read) VALUES(1,'item-1',2,2,0,'pending','title',0)`)
		},
		"未解决事件去重": func(t *testing.T, tx *sql.Tx) {
			schemaReject(t, tx, `INSERT INTO system_events(kind,subject_id) VALUES('account_verify',1)`)
			schemaExec(t, tx, `UPDATE system_events SET resolved_at=10 WHERE id=1`)
			schemaExec(t, tx, `INSERT INTO system_events(kind,subject_id) VALUES('account_verify',1)`)
			schemaExec(t, tx, `INSERT INTO system_events(kind,subject_id,resolved_at) VALUES('account_verify',1,20)`)
			schemaExec(t, tx, `INSERT INTO system_events(kind,subject_id) VALUES('account_verify',2)`)
			schemaExec(t, tx, `INSERT INTO system_events(kind,subject_id) VALUES('account_login',1)`)
			schemaReject(t, tx, `INSERT INTO system_events(kind,subject_id) VALUES('account_verify',1)`)
		},
	}
	for name, check := range cases {
		t.Run(name, func(t *testing.T) { check(t, schemaTransaction(t, db)) })
	}
}

func schemaCount(t *testing.T, tx *sql.Tx, query string, want int) {
	t.Helper()
	var got int
	if err := tx.QueryRow(query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("行数不一致：%s: got %d, want %d", query, got, want)
	}
}

func TestInitialSchemaDeleteRules(t *testing.T) {
	db := schemaOpenDB(t)
	schemaSeed(t, db)
	t.Run("代理绑定阻止删除", func(t *testing.T) {
		tx := schemaTransaction(t, db)
		schemaReject(t, tx, `DELETE FROM proxies WHERE id=1`)
	})
	t.Run("账号额度清理与历史保留", func(t *testing.T) {
		tx := schemaTransaction(t, db)
		schemaExec(t, tx, `DELETE FROM accounts WHERE id=1`)
		schemaCount(t, tx, `SELECT count(*) FROM quota_events`, 0)
		schemaCount(t, tx, `SELECT count(*) FROM tasks WHERE account_id IS NULL`, 1)
		schemaCount(t, tx, `SELECT count(*) FROM run_records WHERE account_id=1`, 1)
	})
	t.Run("删除发现保留见过和商品", func(t *testing.T) {
		tx := schemaTransaction(t, db)
		schemaExec(t, tx, `DELETE FROM findings WHERE id=1`)
		for _, table := range []string{"seen_items", "listings", "task_item_specs", "delivery_items"} {
			schemaCount(t, tx, "SELECT count(*) FROM "+table, 1)
		}
	})
	t.Run("任务删除清理所属数据并保留历史", func(t *testing.T) {
		tx := schemaTransaction(t, db)
		schemaExec(t, tx, `DELETE FROM tasks WHERE id=1`)
		for _, table := range []string{"task_channels", "task_sightings", "task_item_specs", "seen_items", "findings", "market_insights"} {
			schemaCount(t, tx, "SELECT count(*) FROM "+table, 0)
		}
		for _, table := range []string{"ai_calls", "deliveries"} {
			schemaCount(t, tx, "SELECT count(*) FROM "+table+" WHERE task_id=1", 1)
		}
		schemaCount(t, tx, `SELECT count(*) FROM run_records WHERE task_id IS NULL AND task_name='任务'`, 1)
		schemaCount(t, tx, `SELECT count(*) FROM delivery_items WHERE finding_id=1`, 1)
		schemaCount(t, tx, `SELECT count(*) FROM listings`, 1)
	})
	t.Run("商品价格随商品删除", func(t *testing.T) {
		tx := schemaTransaction(t, db)
		schemaReject(t, tx, `DELETE FROM listings WHERE item_id='item-1'`)
		schemaExec(t, tx, `DELETE FROM findings WHERE id=1`)
		schemaExec(t, tx, `DELETE FROM listings WHERE item_id='item-1'`)
		schemaCount(t, tx, `SELECT count(*) FROM price_points`, 0)
		schemaCount(t, tx, `SELECT count(*) FROM seen_items`, 1)
	})
	t.Run("服务商兼容信息清理而用量保留", func(t *testing.T) {
		tx := schemaTransaction(t, db)
		schemaExec(t, tx, `DELETE FROM ai_providers WHERE id=1`)
		schemaCount(t, tx, `SELECT count(*) FROM ai_compat`, 0)
		schemaCount(t, tx, `SELECT count(*) FROM ai_calls WHERE provider_id=1`, 1)
		schemaCount(t, tx, `SELECT count(*) FROM market_insights WHERE provider_id=1`, 1)
	})
	t.Run("渠道绑定清理而发送历史保留", func(t *testing.T) {
		tx := schemaTransaction(t, db)
		schemaExec(t, tx, `DELETE FROM channels WHERE id=1`)
		schemaCount(t, tx, `SELECT count(*) FROM task_channels`, 0)
		schemaCount(t, tx, `SELECT count(*) FROM deliveries WHERE channel_id=1`, 1)
	})
	t.Run("发送明细和逐步记录随历史清理", func(t *testing.T) {
		tx := schemaTransaction(t, db)
		schemaExec(t, tx, `DELETE FROM deliveries WHERE id=1`)
		schemaExec(t, tx, `DELETE FROM run_records WHERE id=1`)
		schemaCount(t, tx, `SELECT count(*) FROM delivery_items`, 0)
		schemaCount(t, tx, `SELECT count(*) FROM run_steps`, 0)
	})
}
