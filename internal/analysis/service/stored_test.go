package modelservice

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/analysis/prompts"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/store"
)

type storedFixture struct {
	db     *sql.DB
	cipher *security.Cipher
}

func newStoredFixture(t *testing.T) storedFixture {
	t.Helper()
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "candidate.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	cipher, err := security.NewCipher(security.NewMasterKey())
	if err != nil {
		t.Fatal(err)
	}
	return storedFixture{database.DB(), cipher}
}

func (f storedFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func (f storedFixture) provider(t *testing.T, uuid string, enabled bool, key *string) int64 {
	t.Helper()
	var encrypted []byte
	var err error
	if key != nil {
		encrypted, err = f.cipher.Seal([]byte(*key), "ai_providers", "api_key_enc", uuid)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert, err := f.db.ExecContext(t.Context(), `INSERT INTO ai_providers(uuid,name,base_url,api_key_enc,vision_model,enabled,sort_index) VALUES(?,?,?,?,?,?,?)`,
		uuid, "本地配置", "http://model.invalid/v1", encrypted, "old-model", enabled, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := insert.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func storedSnapshot(t *testing.T, f storedFixture) string {
	t.Helper()
	result := map[string][][]any{}
	for _, table := range []string{"ai_providers", "ai_compat", "ai_calls", "prompts", "meta", "settings", "quota_events", "system_events", "sqlite_sequence"} {
		rows, err := f.db.QueryContext(t.Context(), `SELECT * FROM `+table+` ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			result[table] = append(result[table], values)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func storedFailure(t *testing.T, err error) {
	t.Helper()
	var problem *service.ServiceError
	if !errors.As(err, &problem) || problem.Kind != service.ErrorUnavailable || problem.Reason.Code != "service.storage_failure" || err.Error() != "service.storage_failure" {
		t.Fatalf("unsafe candidate error: %v", err)
	}
	encoded, _ := json.Marshal(problem)
	if strings.Contains(string(encoded), "private") || len(problem.Reason.Args) != 0 || len(problem.Fields) != 0 {
		t.Fatalf("candidate error leaked stored content: %s", encoded)
	}
}

func TestValidateStoredAcceptsLegacyAndExternalHistoryWithoutWrites(t *testing.T) {
	f := newStoredFixture(t)
	secret := "private key"
	legacy := f.provider(t, "legacy-row", false, &secret)
	current := f.provider(t, "current-row", true, nil)
	empty := ""
	f.provider(t, "historical row without UUID formatting", false, &empty)
	f.exec(t, `INSERT INTO meta(key,value) VALUES(?,?)`, providerTokenKey(current), strings.Repeat("af", 32))
	state := `{"code":"old.unrecognized.reason","args":{"large":9007199254740993,"huge":1e309,"nested":[null,{"raw":"private old detail"}]}}`
	f.exec(t, `UPDATE ai_providers SET error_state=?,error_at=? WHERE id=?`, state, int64(math.MaxInt64), legacy)
	f.exec(t, `INSERT INTO ai_compat(provider_id,model,json_mode,send_temperature,max_tokens_param,max_tokens_floor,updated_at) VALUES(?,?,'text',0,'max_completion_tokens',?,?)`,
		legacy, "retired model not in current configuration", int64(math.MaxInt64), int64(-1))
	for _, at := range []int64{-1, math.MaxInt64} {
		f.exec(t, `INSERT INTO ai_calls(at,provider_id,model,purpose,task_id,ok,input_tokens,output_tokens,reasoning_tokens,duration_ms,error_kind,finish_reason)
			VALUES(?,999,'retired model','test',999,NULL,?,0,?,?,?,?)`, at, int64(math.MaxInt64), int64(math.MaxInt64), int64(math.MaxInt64), "old.error.code", "old-finish")
	}
	f.exec(t, `INSERT INTO ai_calls(at,purpose) VALUES(1,'profile')`)
	definitions, err := prompts.Definitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		content := definition.Default
		if definition.Kind == client.Market {
			content = " \n\t"
		}
		f.exec(t, `INSERT INTO prompts(key,content) VALUES(?,?)`, definition.Kind, content)
	}
	before := storedSnapshot(t, f)
	var changes int64
	if err := f.db.QueryRowContext(t.Context(), `SELECT total_changes()`).Scan(&changes); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `PRAGMA query_only=ON`)
	if err := ValidateStored(t.Context(), f.db, f.cipher); err != nil {
		t.Fatal("valid history rejected", err)
	}
	var afterChanges int64
	if err := f.db.QueryRowContext(t.Context(), `SELECT total_changes()`).Scan(&afterChanges); err != nil {
		t.Fatal(err)
	}
	if afterChanges != changes || storedSnapshot(t, f) != before {
		t.Fatal("validation wrote defaults, metadata, quota, compat or history")
	}
}

func TestValidateStoredDecryptsEveryDisabledUnreferencedProvider(t *testing.T) {
	for _, change := range []string{"wrong key", "wrong row", "corrupt blob", "empty blob", "invalid plaintext"} {
		t.Run(change, func(t *testing.T) {
			f := newStoredFixture(t)
			secret := "private key"
			first := f.provider(t, "first", true, &secret)
			second := f.provider(t, "second", false, &secret)
			cipher := f.cipher
			switch change {
			case "wrong key":
				var err error
				cipher, err = security.NewCipher(security.NewMasterKey())
				if err != nil {
					t.Fatal(err)
				}
			case "wrong row":
				f.exec(t, `UPDATE ai_providers SET api_key_enc=(SELECT api_key_enc FROM ai_providers WHERE id=?) WHERE id=?`, first, second)
			case "corrupt blob":
				f.exec(t, `UPDATE ai_providers SET api_key_enc=x'010203' WHERE id=?`, second)
			case "empty blob":
				f.exec(t, `UPDATE ai_providers SET api_key_enc=zeroblob(0) WHERE id=?`, second)
			case "invalid plaintext":
				blob, err := f.cipher.Seal([]byte("private\r\nkey"), "ai_providers", "api_key_enc", "second")
				if err != nil {
					t.Fatal(err)
				}
				f.exec(t, `UPDATE ai_providers SET api_key_enc=? WHERE id=?`, blob, second)
			}
			before := storedSnapshot(t, f)
			storedFailure(t, ValidateStored(t.Context(), f.db, cipher))
			if storedSnapshot(t, f) != before {
				t.Fatal("bad provider validation changed the candidate")
			}
		})
	}
}

func TestValidateStoredRejectsBadPresentProviderStateWithoutRequiringKnownReasons(t *testing.T) {
	for _, change := range []string{
		`UPDATE ai_providers SET name=' private name '`,
		`UPDATE ai_providers SET vision_model=''`,
		`UPDATE ai_providers SET base_url='https://model.invalid/v1/chat/completions'`,
		`UPDATE ai_providers SET uuid=''`,
		`UPDATE ai_providers SET error_state='{"code":"private"'`,
		`UPDATE ai_providers SET error_state='{"code":""}'`,
		`UPDATE ai_providers SET error_state='{"code":"old","args":[]}'`,
		`UPDATE ai_providers SET error_state='{"code":"old"}{}'`,
		`UPDATE ai_providers SET error_state='{"code":"old","extra":1}'`,
		`INSERT INTO meta(key,value) SELECT 'provider.config.'||id,'private bad token' FROM ai_providers`,
	} {
		t.Run(change, func(t *testing.T) {
			f := newStoredFixture(t)
			f.provider(t, "legacy-row", false, nil)
			f.exec(t, change)
			before := storedSnapshot(t, f)
			storedFailure(t, ValidateStored(t.Context(), f.db, f.cipher))
			if storedSnapshot(t, f) != before {
				t.Fatal("invalid provider state was repaired")
			}
		})
	}
	for _, state := range []string{`{"code":"old"}`, `{"code":"old","args":null}`, `{"code":"old","args":{"old-field":18446744073709551616}}`} {
		f := newStoredFixture(t)
		f.provider(t, "legacy-row", false, nil)
		f.exec(t, `UPDATE ai_providers SET error_state=?`, state)
		if err := ValidateStored(t.Context(), f.db, f.cipher); err != nil {
			t.Fatal("valid opaque Message rejected", state, err)
		}
	}
}

func TestValidateStoredRejectsInvalidUTF8ReasonWithoutReplacingIt(t *testing.T) {
	f := newStoredFixture(t)
	f.provider(t, "legacy-row", false, nil)
	valid := `{"code":"old.reason","args":{"detail":"合法的替代字符：�"}}`
	f.exec(t, `UPDATE ai_providers SET error_state=?`, valid)
	if err := ValidateStored(t.Context(), f.db, f.cipher); err != nil {
		t.Fatal("valid UTF-8 reason rejected", err)
	}
	invalid := `{"code":"old.reason","args":{"detail":"private ` + string([]byte{0xff}) + `"}}`
	f.exec(t, `UPDATE ai_providers SET error_state=?`, invalid)
	storedFailure(t, ValidateStored(t.Context(), f.db, f.cipher))
	var after string
	if err := f.db.QueryRowContext(t.Context(), `SELECT error_state FROM ai_providers`).Scan(&after); err != nil || after != invalid {
		t.Fatal("invalid reason was rewritten during validation", err)
	}
}

func TestValidateStoredChecksAllCompatibilityFormats(t *testing.T) {
	for _, change := range []string{
		`UPDATE ai_compat SET json_mode='private'`,
		`UPDATE ai_compat SET max_tokens_param='private'`,
		`UPDATE ai_compat SET max_tokens_floor=-1`,
		`UPDATE ai_compat SET max_tokens_floor=1.5`,
		`UPDATE ai_compat SET send_temperature=2`,
	} {
		t.Run(change, func(t *testing.T) {
			f := newStoredFixture(t)
			id := f.provider(t, "legacy-row", false, nil)
			f.exec(t, `INSERT INTO ai_compat(provider_id,model) VALUES(?,?)`, id, "old model")
			f.exec(t, `PRAGMA ignore_check_constraints=ON`)
			f.exec(t, change)
			f.exec(t, `PRAGMA ignore_check_constraints=OFF`)
			storedFailure(t, ValidateStored(t.Context(), f.db, f.cipher))
		})
	}
}

func TestValidateStoredChecksEveryHistoricalCallWithoutAggregating(t *testing.T) {
	for _, change := range []string{
		`UPDATE ai_calls SET at=NULL`, `UPDATE ai_calls SET purpose=NULL`,
		`UPDATE ai_calls SET purpose='private'`, `UPDATE ai_calls SET provider_id=-1`, `UPDATE ai_calls SET task_id=-1`,
		`UPDATE ai_calls SET ok=2`, `UPDATE ai_calls SET http_status=99`, `UPDATE ai_calls SET http_status=600`,
		`UPDATE ai_calls SET json_mode='private'`, `UPDATE ai_calls SET input_tokens=-1`,
		`UPDATE ai_calls SET output_tokens=-1`, `UPDATE ai_calls SET reasoning_tokens=-1`,
		`UPDATE ai_calls SET duration_ms=-1`, `UPDATE ai_calls SET input_tokens=1.5`,
		`UPDATE ai_calls SET input_tokens=9223372036854775807,output_tokens=1`,
	} {
		t.Run(change, func(t *testing.T) {
			f := newStoredFixture(t)
			f.exec(t, `INSERT INTO ai_calls(at,purpose,ok,input_tokens,output_tokens) VALUES(-1000000000000,'analysis',1,1,1)`)
			f.exec(t, `PRAGMA ignore_check_constraints=ON`)
			f.exec(t, change)
			f.exec(t, `PRAGMA ignore_check_constraints=OFF`)
			before := storedSnapshot(t, f)
			storedFailure(t, ValidateStored(t.Context(), f.db, f.cipher))
			if storedSnapshot(t, f) != before {
				t.Fatal("invalid history was overwritten")
			}
		})
	}
}

func TestValidateStoredResolvesAllPromptOverrides(t *testing.T) {
	for _, value := range []struct{ kind, content string }{
		{"analysis", `private {"checks":`},
		{"title_screen", `private "items" "kind"`},
		{"profile", `private "specs"`},
		{"unknown", "private text"},
		{"market", string([]byte{0xff})},
	} {
		t.Run(value.kind, func(t *testing.T) {
			f := newStoredFixture(t)
			f.exec(t, `PRAGMA ignore_check_constraints=ON`)
			f.exec(t, `INSERT INTO prompts(key,content) VALUES(?,?)`, value.kind, value.content)
			f.exec(t, `PRAGMA ignore_check_constraints=OFF`)
			before := storedSnapshot(t, f)
			storedFailure(t, ValidateStored(t.Context(), f.db, f.cipher))
			if storedSnapshot(t, f) != before {
				t.Fatal("invalid prompt was replaced by its default")
			}
		})
	}
}

func TestValidateStoredEmptyCandidateDependenciesAndDatabaseErrors(t *testing.T) {
	f := newStoredFixture(t)
	if err := ValidateStored(t.Context(), f.db, f.cipher); err != nil {
		t.Fatal("empty candidate rejected", err)
	}
	storedFailure(t, ValidateStored(t.Context(), nil, f.cipher))
	storedFailure(t, ValidateStored(t.Context(), f.db, nil))
	f.exec(t, `DROP TABLE ai_compat`)
	storedFailure(t, ValidateStored(t.Context(), f.db, f.cipher))
}

func TestValidateStoredPreservesCancellationWithoutStartingModelService(t *testing.T) {
	f := newStoredFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := ValidateStored(ctx, f.db, f.cipher); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	if err := ValidateStored(ctx, f.db, f.cipher); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	connection, err := f.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	waits := f.db.Stats().WaitCount
	go func() { result <- ValidateStored(ctx, f.db, f.cipher) }()
	deadline := time.Now().Add(time.Second)
	for f.db.Stats().WaitCount == waits {
		if time.Now().After(deadline) {
			t.Fatal("validator did not wait for the occupied connection")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("lost cancellation chain", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled validation did not exit")
	}
	connection.Close()
	if err := ValidateStored(t.Context(), f.db, f.cipher); err != nil {
		t.Fatal("validator required service initialization or quota metadata", err)
	}
}
