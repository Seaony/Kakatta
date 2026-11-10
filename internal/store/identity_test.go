package store

import (
	"context"
	"database/sql"
	"reflect"
	"sort"
	"strconv"
	"testing"
)

func insertID(t *testing.T, db *sql.DB, query string) int64 {
	t.Helper()
	result, err := db.ExecContext(context.Background(), query)
	if err != nil {
		t.Fatal(query, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func reopenStore(t *testing.T, s *Store) *Store {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), s.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	return reopened
}

func TestAutoIDsSurviveDeletionAndReopen(t *testing.T) {
	cases := []struct{ table, setup, insert string }{
		{"auth_tokens", "", `INSERT INTO auth_tokens(kind,token_hash,name) VALUES('api_token',x'01','令牌')`},
		{"proxies", "", `INSERT INTO proxies(uuid,name,url_enc) VALUES('proxy','代理',x'01')`},
		{"accounts", "", `INSERT INTO accounts(name,state) VALUES('账号','signed_out')`},
		{"quota_events", `INSERT INTO accounts(id,name,state) VALUES(1,'账号','active')`, `INSERT INTO quota_events(account_id,kind,at) VALUES(1,'search',1)`},
		{"tasks", "", `INSERT INTO tasks(name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index) VALUES('任务','手机',1,30,'keyword','recommended',0)`},
		{"channels", "", `INSERT INTO channels(uuid,type,name,config_json) VALUES('channel','bark','渠道','{}')`},
		{"price_points", "", `INSERT INTO price_points(price,observed_at) VALUES(100,1)`},
		{"findings", `INSERT INTO tasks(id,name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index) VALUES(1,'任务','手机',1,30,'keyword','recommended',0);
			INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at) VALUES('item','商品','链接',1,1)`,
			`INSERT INTO findings(task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read) VALUES(1,'item',1,1,0,'pending','title',0)`},
		{"ai_providers", "", `INSERT INTO ai_providers(uuid,name,base_url,vision_model,sort_index) VALUES('provider','服务商','地址','model',0)`},
		{"ai_calls", "", `INSERT INTO ai_calls(purpose) VALUES('analysis')`},
		{"deliveries", "", `INSERT INTO deliveries(kind) VALUES('recommended')`},
		{"run_records", "", `INSERT INTO run_records(task_name,trigger,outcome) VALUES('任务','auto','success')`},
		{"system_events", "", `INSERT INTO system_events(kind,subject_id) VALUES('account_verify',1)`},
		{"market_insights", "", `INSERT INTO market_insights(generated_at) VALUES(1)`},
		{"notification_intents", "", `INSERT INTO notification_intents(finding_id,task_id,kind,state,created_at,updated_at) VALUES(1,1,'recommended','sent',1,1)`},
	}
	for _, test := range cases {
		t.Run(test.table, func(t *testing.T) {
			s := testStore(t)
			if test.setup != "" {
				execute(t, s.DB(), test.setup)
			}
			first := insertID(t, s.DB(), test.insert)
			execute(t, s.DB(), "DELETE FROM "+test.table+" WHERE id=?", first)
			second := insertID(t, s.DB(), test.insert)
			if second <= first {
				t.Errorf("删除后复用了旧编号：旧%d，新%d", first, second)
			}
			execute(t, s.DB(), "DELETE FROM "+test.table)
			s = reopenStore(t, s)
			third := insertID(t, s.DB(), test.insert)
			if third <= second {
				t.Errorf("清空并重开后复用了旧编号：旧%d，新%d", second, third)
			}
		})
	}
}

func TestDeletedEntitiesDoNotCaptureHistory(t *testing.T) {
	for _, reopen := range []bool{false, true} {
		t.Run("重开_"+strconv.FormatBool(reopen), func(t *testing.T) {
			cases := []struct {
				name    string
				deletes []string
				inserts []string
				kept    []string
				joins   []string
			}{
				{
					name:    "发现与发送明细",
					deletes: []string{`DELETE FROM findings WHERE id=1`},
					inserts: []string{
						`INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at) VALUES('new-item','新商品','新链接',2,2)`,
						`INSERT INTO findings(task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read) VALUES(1,'new-item',1,2,0,'pending','title',0)`,
					},
					kept:  []string{`SELECT count(*) FROM delivery_items WHERE delivery_id=1 AND finding_id=1`},
					joins: []string{`SELECT count(*) FROM delivery_items d JOIN findings f ON f.id=d.finding_id WHERE f.item_id='new-item'`},
				},
				{
					name:    "任务服务商与用量",
					deletes: []string{`DELETE FROM tasks WHERE id=1`, `DELETE FROM ai_providers WHERE id=1`},
					inserts: []string{
						`INSERT INTO tasks(name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index) VALUES('新任务','相机',1,30,'keyword','recommended',0)`,
						`INSERT INTO ai_providers(uuid,name,base_url,vision_model,sort_index) VALUES('new-provider','新服务商','新地址','model',0)`,
					},
					kept: []string{`SELECT count(*) FROM ai_calls WHERE id=1 AND task_id=1 AND provider_id=1`},
					joins: []string{
						`SELECT count(*) FROM ai_calls c JOIN tasks t ON t.id=c.task_id WHERE t.name='新任务'`,
						`SELECT count(*) FROM ai_calls c JOIN ai_providers p ON p.id=c.provider_id WHERE p.name='新服务商'`,
					},
				},
				{
					name:    "渠道与发送记录",
					deletes: []string{`DELETE FROM channels WHERE id=1`},
					inserts: []string{`INSERT INTO channels(uuid,type,name,config_json) VALUES('new-channel','bark','新渠道','{}')`},
					kept:    []string{`SELECT count(*) FROM deliveries WHERE id=1 AND channel_id=1`},
					joins:   []string{`SELECT count(*) FROM deliveries d JOIN channels c ON c.id=d.channel_id WHERE c.name='新渠道'`},
				},
			}
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					s := testStore(t)
					schemaSeed(t, s.DB())
					for _, query := range test.deletes {
						execute(t, s.DB(), query)
					}
					if reopen {
						s = reopenStore(t, s)
					}
					for _, query := range test.inserts {
						execute(t, s.DB(), query)
					}
					for _, query := range test.kept {
						if got := scalar[int](t, s.DB(), query); got != 1 {
							t.Errorf("旧引用没有保留：%s，行数%d", query, got)
						}
					}
					for _, query := range test.joins {
						if got := scalar[int](t, s.DB(), query); got != 0 {
							t.Errorf("旧历史串到了新实体：%s，行数%d", query, got)
						}
					}
				})
			}
		})
	}
}

func TestOnlyAutomaticIDsHaveSequences(t *testing.T) {
	s := testStore(t)
	schemaSeed(t, s.DB())
	rows, err := s.DB().Query(`SELECT name FROM sqlite_sequence ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"auth_tokens", "proxies", "accounts", "quota_events", "tasks", "channels", "price_points", "findings", "ai_providers", "ai_calls", "deliveries", "run_records", "system_events", "market_insights", "notification_intents"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("自动编号序列不一致：admin、版本号和自然主键不应建序列：got %v, want %v", got, want)
	}
}
