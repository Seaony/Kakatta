package appsettings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

func TestValidateStoredTxReadsAllSettingsAndReferencesWithoutWrites(t *testing.T) {
	for name, clock := range map[string]string{
		"missing":    "",
		"broken":     "broken-clock",
		"historical": `{"version":1,"highest_utc":1735689600000,"timezone":"UTC","windows":[{"start":1735603200000,"end":1735776000000}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			values := settings.Values{
				"general.language":          json.RawMessage(`"en"`),
				"general.timezone":          json.RawMessage(`"Asia/Tokyo"`),
				"security.trusted_proxies":  json.RawMessage(`["10.0.0.0/8"]`),
				"ai.image_count":            json.RawMessage(`8`),
				"ai.proxy_id":               json.RawMessage(`1`),
				"notify.system_channel_ids": json.RawMessage(`[1,2]`),
			}
			db := restoreSettingsDB(t, values)
			if clock != "" {
				if _, err := db.Exec(`INSERT INTO meta(key,value) VALUES(?,?),(?,?)`, ClockKey, clock, clockMarker, "1"); err != nil {
					t.Fatal(err)
				}
			}
			empty := ""
			env := settings.Environment{TZ: "America/New_York", TrustedProxies: &empty}
			want, err := settings.Resolve(values, env)
			if err != nil {
				t.Fatal(err)
			}
			tx := restoreReadOnlyTx(t, db)
			before, changes := restoreContents(t, tx), restoreChanges(t, tx)
			got, err := ValidateStoredTx(t.Context(), tx, env)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("没有复用完整设置解析：%+v %v", got, err)
			}
			if len(got.Warnings) != 1 || got.Warnings[0].Key != "ai.image_count" ||
				string(got.Values["security.trusted_proxies"]) != `[]` || got.Sources["security.trusted_proxies"] != settings.SourceEnvironment {
				t.Fatal("推荐外合法值或环境覆盖语义改变")
			}
			assertRestoreUnchanged(t, tx, before, changes)
			if err := tx.Commit(); err != nil {
				t.Fatalf("校验结束了调用方的事务：%v", err)
			}
		})
	}
}

func TestValidateStoredTxPreservesValidationErrorsAndReturnsNoPartialSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values settings.Values
		env    settings.Environment
	}{
		{"bad JSON", settings.Values{"ai.image_count": json.RawMessage(`{`)}, settings.Environment{TZ: "UTC"}},
		{"bad value", settings.Values{"ai.image_count": json.RawMessage(`0`)}, settings.Environment{TZ: "UTC"}},
		{"unknown key", settings.Values{"unknown.setting": json.RawMessage(`1`)}, settings.Environment{TZ: "UTC"}},
		{"bad environment", settings.Values{}, settings.Environment{TZ: "Not/AZone"}},
		{"missing proxy", settings.Values{"ai.proxy_id": json.RawMessage(`9`)}, settings.Environment{TZ: "UTC"}},
		{"later missing channel", settings.Values{"notify.system_channel_ids": json.RawMessage(`[1,9]`)}, settings.Environment{TZ: "UTC"}},
		{"both references", settings.Values{"ai.proxy_id": json.RawMessage(`9`), "notify.system_channel_ids": json.RawMessage(`[1,9]`)}, settings.Environment{TZ: "UTC"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := restoreSettingsDB(t, tc.values)
			tx := restoreReadOnlyTx(t, db)
			before, changes := restoreContents(t, tx), restoreChanges(t, tx)
			resolved, wantErr := settings.Resolve(tc.values, tc.env)
			if wantErr == nil {
				wantErr = validateReferences(t.Context(), tx, resolved.Values)
			}
			var expected *settings.ValidationError
			if !errors.As(wantErr, &expected) {
				t.Fatalf("对照没有产生既有校验错误：%v", wantErr)
			}
			got, err := ValidateStoredTx(t.Context(), tx, tc.env)
			var invalid *settings.ValidationError
			if !errors.As(err, &invalid) || !reflect.DeepEqual(invalid, expected) || !reflect.DeepEqual(got, settings.Snapshot{}) {
				t.Fatalf("改变了既有错误或返回了部分结果：%+v %v，期望%v", got, err, wantErr)
			}
			assertRestoreUnchanged(t, tx, before, changes)
		})
	}
}

func TestValidateStoredTxPreservesCancellationAndTransactionErrors(t *testing.T) {
	db := restoreSettingsDB(t, settings.Values{"ai.proxy_id": json.RawMessage(`1`), "notify.system_channel_ids": json.RawMessage(`[1,2]`)})
	tx := restoreReadOnlyTx(t, db)
	before, changes := restoreContents(t, tx), restoreChanges(t, tx)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	got, err := ValidateStoredTx(canceled, tx, settings.Environment{TZ: "UTC"})
	if err != context.Canceled || !reflect.DeepEqual(got, settings.Snapshot{}) {
		t.Fatalf("取消错误被替换或返回了部分结果：%+v %v", got, err)
	}
	assertRestoreUnchanged(t, tx, before, changes)
	if _, err := ValidateStoredTx(t.Context(), tx, settings.Environment{TZ: "UTC"}); err != nil {
		t.Fatalf("取消调用关闭了调用方事务：%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	got, err = ValidateStoredTx(t.Context(), tx, settings.Environment{TZ: "UTC"})
	if err != sql.ErrTxDone || !reflect.DeepEqual(got, settings.Snapshot{}) {
		t.Fatalf("没有保留已结束事务的原始错误：%+v %v", got, err)
	}
}

func TestValidateStoredTxPreservesSQLiteReadErrors(t *testing.T) {
	for _, table := range []string{"settings", "proxies", "channels"} {
		t.Run(table, func(t *testing.T) {
			values := settings.Values{"ai.proxy_id": json.RawMessage(`1`), "notify.system_channel_ids": json.RawMessage(`[1,2]`)}
			db := restoreSettingsDB(t, values)
			if _, err := db.Exec("DROP TABLE " + table); err != nil {
				t.Fatal(err)
			}
			tx := restoreReadOnlyTx(t, db)
			before := restoreChanges(t, tx)
			stored, expected := readValues(t.Context(), tx)
			if expected == nil {
				resolved, err := settings.Resolve(stored, settings.Environment{TZ: "UTC"})
				if err != nil {
					t.Fatal(err)
				}
				expected = validateReferences(t.Context(), tx, resolved.Values)
			}
			got, err := ValidateStoredTx(t.Context(), tx, settings.Environment{TZ: "UTC"})
			if expected == nil || err == nil || reflect.TypeOf(err) != reflect.TypeOf(expected) || err.Error() != expected.Error() || !reflect.DeepEqual(got, settings.Snapshot{}) {
				t.Fatalf("没有保留SQLite查询错误：%+v %v，期望%v", got, err, expected)
			}
			if restoreChanges(t, tx) != before {
				t.Fatal("查询失败后仍有写入")
			}
		})
	}
}

func restoreSettingsDB(t *testing.T, values settings.Values) *sql.DB {
	t.Helper()
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "restore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	db := database.DB()
	for _, statement := range []string{
		`INSERT INTO proxies(id,uuid,name,url_enc) VALUES(1,'restore-proxy','引用测试',X'00')`,
		`INSERT INTO channels(id,uuid,type,name,enabled,config_json) VALUES(1,'restore-channel-1','webhook','已停用引用',0,'{}'),(2,'restore-channel-2','webhook','第二个引用',1,'{}')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for key, raw := range values {
		if _, err := db.Exec(`INSERT INTO settings(key,value_json,updated_at) VALUES(?,?,123)`, key, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func restoreReadOnlyTx(t *testing.T, db *sql.DB) *sql.Tx {
	t.Helper()
	if _, err := db.Exec(`PRAGMA query_only=ON`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback() })
	if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('restore.readonly_probe','must not write')`); err == nil {
		t.Fatal("测试事务没有真正禁止写入")
	}
	return tx
}

func restoreContents(t *testing.T, tx *sql.Tx) []string {
	t.Helper()
	rows, err := tx.Query(`SELECT 'settings:'||key,value_json FROM settings UNION ALL SELECT 'meta:'||key,value FROM meta ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	contents := []string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			t.Fatal(err)
		}
		contents = append(contents, key+"\x00"+value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return contents
}

func restoreChanges(t *testing.T, tx *sql.Tx) int64 {
	t.Helper()
	var count int64
	if err := tx.QueryRow(`SELECT total_changes()`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertRestoreUnchanged(t *testing.T, tx *sql.Tx, before []string, changes int64) {
	t.Helper()
	if restoreChanges(t, tx) != changes || !reflect.DeepEqual(restoreContents(t, tx), before) {
		t.Fatal("校验改变了设置、历史或额度时钟")
	}
}
