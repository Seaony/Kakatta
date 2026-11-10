package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/store"
)

type exportRuntime struct {
	source     SnapshotSource
	before     func(context.Context) error
	after      func() error
	leased     atomic.Bool
	calls      atomic.Int32
	closeCalls atomic.Int32
}

func (r *exportRuntime) WithSnapshot(ctx context.Context, fn func(SnapshotSource) error) error {
	r.calls.Add(1)
	r.leased.Store(true)
	defer r.leased.Store(false)
	if r.before != nil {
		if err := r.before(ctx); err != nil {
			return err
		}
	}
	if err := fn(r.source); err != nil {
		return err
	}
	if r.after != nil {
		return r.after()
	}
	return nil
}

func (*exportRuntime) Install(context.Context, RestorePlan) error {
	return errors.New("本测试不应安装恢复库")
}

func (r *exportRuntime) Close() error { r.closeCalls.Add(1); return nil }

func exportFixture(t *testing.T) (*Service, *exportRuntime, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	live, err := store.Open(t.Context(), filepath.Join(dir, "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	var version int
	if err := live.DB().QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte("private runtime export key"))
	runtime := &exportRuntime{source: SnapshotSource{Store: live, MasterKey: key, KeySource: KeyExternalFile, Generation: "runtime-generation"}}
	s, err := New(Dependencies{DataDir: dir, AppVersion: "test-export", SchemaVersion: version, Runtime: runtime,
		Now: func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, runtime, live
}

func exportExec(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), statement, args...); err != nil {
		t.Fatal("本地备份夹具写入失败")
	}
}

func assertNoExportFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".backup-export-") {
			t.Fatal("导出结束后留下明文临时目录")
		}
	}
}

func businessDigests(t *testing.T, db *sql.DB) map[string][32]byte {
	t.Helper()
	rows, err := db.Query("SELECT name FROM sqlite_schema WHERE type='table' AND name NOT IN ('auth_tokens','quota_events') ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	digests := map[string][32]byte{}
	for _, table := range tables {
		quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		data, err := db.Query("SELECT * FROM " + quoted + " LIMIT 0")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := data.Columns()
		data.Close()
		if err != nil {
			t.Fatal(err)
		}
		order := make([]string, len(columns))
		for i := range columns {
			order[i] = strconv.Itoa(i + 1)
		}
		data, err = db.Query("SELECT * FROM " + quoted + " ORDER BY " + strings.Join(order, ","))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.New()
		for data.Next() {
			values, pointers := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := data.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(values)
			if err != nil {
				t.Fatal("业务摘要失败")
			}
			hash.Write(encoded)
			hash.Write([]byte{'\n'})
		}
		if err := errors.Join(data.Err(), data.Close()); err != nil {
			t.Fatal(err)
		}
		var digest [32]byte
		copy(digest[:], hash.Sum(nil))
		digests[table] = digest
	}
	return digests
}

func TestExportRealWALKeepsBusinessAndRemovesPhysicalSecrets(t *testing.T) {
	s, runtime, live := exportFixture(t)
	exportExec(t, live.DB(), "PRAGMA wal_autocheckpoint=0")
	authMarker := "AUTH_SESSION_PRIVATE_EXPORT_MARKER_" + strings.Repeat("x", 5000)
	exportExec(t, live.DB(), `INSERT INTO auth_tokens(id,kind,token_hash,name,created_at) VALUES(41,'api_token',?, ?,1)`, bytes.Repeat([]byte{0xb2}, 32), authMarker)
	exportExec(t, live.DB(), `INSERT INTO accounts(id,name,state,created_at) VALUES(17,'本地账号','cooling',1)`)
	exportExec(t, live.DB(), `INSERT INTO quota_events(id,account_id,kind,run_id,at) VALUES(51,17,'search',?,1)`, int64(0x51554f54414d4152))
	exportExec(t, live.DB(), `INSERT INTO ai_calls(id,at,model,purpose,ok,input_tokens,output_tokens) VALUES(61,1,'local','analysis',1,123,456)`)
	exportExec(t, live.DB(), `INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at,is_favorite) VALUES('test-item','商品','local',1,2,1)`)
	exportExec(t, live.DB(), `INSERT INTO settings(key,value_json,updated_at) VALUES('general.timezone','"UTC"',1)`)
	exportExec(t, live.DB(), `INSERT INTO meta(key,value) VALUES('unknown.keep','business-value')`)
	cipher, _ := security.NewCipher(runtime.source.MasterKey[:])
	blob, _ := cipher.Seal([]byte("test-proxy-password"), "proxies", "url_enc", "proxy-uuid")
	exportExec(t, live.DB(), `INSERT INTO proxies(id,uuid,name,url_enc,created_at) VALUES(11,'proxy-uuid','代理',?,1)`, blob)
	before := businessDigests(t, live.DB())
	if info, err := os.Stat(filepath.Join(s.deps.DataDir, "live.db-wal")); err != nil || info.Size() == 0 {
		t.Fatal("夹具没有真实已提交WAL")
	}
	originalKey := runtime.source.MasterKey
	runtime.after = func() error {
		paths, _ := filepath.Glob(filepath.Join(s.deps.DataDir, ".backup-export-*", "raw.db"))
		if len(paths) != 1 {
			return errors.New("缺少真实租约内快照")
		}
		raw, err := os.ReadFile(paths[0])
		if err != nil || !bytes.Contains(raw, []byte("AUTH_SESSION_PRIVATE_EXPORT_MARKER_")) || !bytes.Contains(raw, []byte("QUOTAMAR")) {
			return errors.New("敏感标记夹具未进入原始快照")
		}
		info, err := os.Stat(filepath.Dir(paths[0]))
		if err != nil || info.Mode().Perm() != 0700 {
			return errors.New("快照目录权限不正确")
		}
		if info, err := os.Stat(paths[0]); err != nil || info.Mode().Perm() != 0600 {
			return errors.New("快照文件权限不正确")
		}
		private, err := store.Open(t.Context(), paths[0])
		if err != nil {
			return errors.New("租约内快照无法打开")
		}
		for _, table := range []string{"auth_tokens", "quota_events"} {
			var count int
			if err := private.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 1 {
				private.Close()
				return errors.New("释放运行代租约前已修改快照")
			}
		}
		if err := private.Close(); err != nil {
			return errors.New("快照检查未能关闭")
		}
		runtime.source.MasterKey = sha256.Sum256([]byte("another runtime key"))
		return nil
	}
	// 默认文件与有效运行代故意不同，导出不能重新读取它。
	if err := os.WriteFile(filepath.Join(s.deps.DataDir, MasterKeyEntry), bytes.Repeat([]byte{0x6b}, MasterKeyBytes), 0600); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := checkingExportWriter{dst: &archive, check: func() {
		if runtime.leased.Load() {
			t.Fatal("写归档时仍持有运行代租约")
		}
	}}
	if err := s.Export(t.Context(), testPassword, writer); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "candidate")
	manifest, key, err := ReadArchive(t.Context(), bytes.NewReader(archive.Bytes()), int64(archive.Len()), testPassword, dir, s.deps.SchemaVersion)
	defer clear(key[:])
	if err != nil || key != originalKey || manifest.AppVersion != s.deps.AppVersion || manifest.CreatedAt != "2026-11-11T12:00:00Z" {
		t.Fatal("导出没有保持快照运行代", err)
	}
	cleanBytes, err := os.ReadFile(filepath.Join(dir, DatabaseEntry))
	if err != nil || bytes.Contains(cleanBytes, []byte("AUTH_SESSION_PRIVATE_EXPORT_MARKER_")) || bytes.Contains(cleanBytes, []byte("QUOTAMAR")) {
		t.Fatal("净副本残留被排除内容")
	}
	restored, err := store.Open(t.Context(), filepath.Join(dir, DatabaseEntry))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	for _, db := range []*sql.DB{live.DB(), restored.DB()} {
		want := 1
		if db == restored.DB() {
			want = 0
		}
		for _, table := range []string{"auth_tokens", "quota_events"} {
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != want {
				t.Fatal("排除表清理或原库保护错误", table, err)
			}
		}
	}
	restoredDigests, liveDigests := businessDigests(t, restored.DB()), businessDigests(t, live.DB())
	for table, want := range before {
		if restoredDigests[table] != want || liveDigests[table] != want {
			t.Fatal("业务表或序列发生变化", table)
		}
	}
	assertNoExportFiles(t, s.deps.DataDir)
}

type checkingExportWriter struct {
	dst   io.Writer
	check func()
}

func (w checkingExportWriter) Write(data []byte) (int, error) { w.check(); return w.dst.Write(data) }

func TestExportFailuresNeverChangeLiveStoreOrLeaveTemporaryFiles(t *testing.T) {
	for _, kind := range []string{"runtime", "writer", "schema", "cancelled", "empty_source", "private_sql"} {
		t.Run(kind, func(t *testing.T) {
			s, runtime, live := exportFixture(t)
			exportExec(t, live.DB(), `INSERT INTO meta(key,value) VALUES('keep','value')`)
			exportExec(t, live.DB(), `INSERT INTO auth_tokens(kind,token_hash,name) VALUES('api_token',?,'private-session')`, bytes.Repeat([]byte{0x38}, 32))
			before := businessDigests(t, live.DB())
			failure := errors.New(testPassword + s.deps.DataDir)
			var out bytes.Buffer
			var writer io.Writer = &out
			ctx := t.Context()
			want := ErrIO
			switch kind {
			case "runtime":
				runtime.after = func() error { return failure }
			case "writer":
				writer = &failWriter{remaining: 512, err: failure}
			case "schema":
				s.deps.SchemaVersion++
				want = ErrUnsupportedVersion
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			case "empty_source":
				runtime.source.Generation = ""
				want = ErrInvalidDependencies
			case "private_sql":
				runtime.after = func() error {
					paths, _ := filepath.Glob(filepath.Join(s.deps.DataDir, ".backup-export-*", "raw.db"))
					if len(paths) != 1 {
						return errors.New("缺少私有快照")
					}
					private, err := store.Open(t.Context(), paths[0])
					if err != nil {
						return err
					}
					_, err = private.DB().Exec(`CREATE TRIGGER block_export_delete BEFORE DELETE ON auth_tokens BEGIN SELECT RAISE(ABORT,'private-trigger-marker'); END`)
					return errors.Join(err, private.Close())
				}
			}
			err := s.Export(ctx, testPassword, writer)
			if !errors.Is(err, want) || strings.Contains(err.Error(), testPassword) || strings.Contains(err.Error(), s.deps.DataDir) {
				t.Fatal("导出失败原因或脱敏错误")
			}
			after := businessDigests(t, live.DB())
			var sessions int
			if err := live.DB().QueryRow("SELECT COUNT(*) FROM auth_tokens").Scan(&sessions); err != nil || sessions != 1 {
				t.Fatal("失败改动了原库会话")
			}
			for table, want := range before {
				if after[table] != want {
					t.Fatal("失败改动原库", table)
				}
			}
			assertNoExportFiles(t, s.deps.DataDir)
		})
	}
}
