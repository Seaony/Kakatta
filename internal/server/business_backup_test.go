package server

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/backup"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/store"
)

const localArchivePassword = "仅供本地备份测试的口令"

type backupHTTPRuntime struct {
	source             backup.SnapshotSource
	active             bool
	validated          int
	installAttempts    int
	installed          int
	beforeInstallCheck func(context.Context) error
}

func (rt *backupHTTPRuntime) WithSnapshot(_ context.Context, read func(backup.SnapshotSource) error) error {
	rt.active = true
	defer func() { rt.active = false }()
	return read(rt.source)
}

func (rt *backupHTTPRuntime) Install(ctx context.Context, plan backup.RestorePlan) error {
	rt.installAttempts++
	if rt.beforeInstallCheck != nil {
		if err := rt.beforeInstallCheck(ctx); err != nil {
			return err
		}
	}
	if err := plan.Authorize(ctx); err != nil {
		return errors.Join(backup.ErrRestoreAuthorization, err)
	}
	if plan.ExpectedGeneration != rt.source.Generation || plan.KeySource != rt.source.KeySource || plan.MasterKey != rt.source.MasterKey {
		return errors.New("恢复计划丢失实际运行代或密钥")
	}
	candidate, err := store.Open(ctx, plan.DatabasePath)
	if err != nil {
		return err
	}
	defer candidate.Close()
	var taskCount, tokenCount, quotaCount int
	if err := candidate.DB().QueryRowContext(ctx, `SELECT (SELECT count(*) FROM tasks), (SELECT count(*) FROM auth_tokens), (SELECT count(*) FROM quota_events)`).Scan(&taskCount, &tokenCount, &quotaCount); err != nil {
		return err
	}
	if taskCount != 1 || tokenCount != 0 || quotaCount != 0 {
		return errors.New("候选业务数据或排除范围错误")
	}
	rt.installed++
	return nil
}

func newBackupHTTPFixture(t *testing.T) (*businessFixture, *backupHTTPRuntime) {
	t.Helper()
	f := prepareBusinessFixture(t)
	rt := &backupHTTPRuntime{source: backup.SnapshotSource{
		Store: f.store, MasterKey: [32]byte(bytes.Repeat([]byte{4}, 32)), KeySource: backup.KeyDefaultFile, Generation: "test-generation",
	}}
	backups, err := backup.New(backup.Dependencies{
		DataDir: f.dataDir, AppVersion: "local-test", SchemaVersion: store.SchemaVersion(), Runtime: rt,
		Now: func() time.Time { return time.UnixMilli(f.clock.Load()) },
		ValidateCandidate: func(ctx context.Context, path string, key [backup.MasterKeyBytes]byte, schema int) error {
			rt.validated++
			if err := store.ValidateBackup(ctx, path, schema); err != nil {
				return err
			}
			candidate, err := store.Open(ctx, path)
			if err != nil {
				return err
			}
			defer candidate.Close()
			cipher, err := security.NewCipher(key[:])
			if err != nil {
				return err
			}
			return candidate.ReadSnapshot(ctx, func(tx *sql.Tx) error {
				_, err := auth.ValidateStoredTx(ctx, tx, cipher)
				return err
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backups.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := f.server.RegisterBackup(backups); err != nil {
		t.Fatal(err)
	}
	if f.server.RegisterBackup(nil) == nil || f.server.RegisterBackup(backups) == nil {
		t.Fatal("空备份服务或重复装配必须拒绝")
	}
	return f, rt
}

func requestTestArchive(t *testing.T, f *businessFixture, rt *backupHTTPRuntime) string {
	t.Helper()
	body := `{"password":"` + localArchivePassword + `"}`
	request := httptest.NewRequest(http.MethodPost, "http://kakatta.test/api/v1/backup/export", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: cookieName, Value: f.session.Token})
	response := httptest.NewRecorder()
	f.server.ServeHTTP(stagedHTTPRecorder{response, func(status int) {
		if status != http.StatusOK || rt.active {
			t.Fatal("发送 ZIP 时仍占用快照")
		}
	}}, request)
	requireStatus(t, response, http.StatusOK)
	raw := response.Body.Bytes()
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(archive.File) != 2 ||
		response.Header().Get("Content-Type") != "application/zip" ||
		response.Header().Get("Content-Length") != strconv.Itoa(len(raw)) ||
		!strings.Contains(response.Header().Get("Content-Disposition"), "attachment") ||
		bytes.Contains(raw, []byte(localArchivePassword)) || bytes.Contains(raw, []byte(f.session.Token)) {
		t.Fatal("未收到完整私有 ZIP", err)
	}
	return string(raw)
}

func requestRestoreArchive(t *testing.T, f *businessFixture, archive, password string) *httptest.ResponseRecorder {
	t.Helper()
	body, media, _ := uploadTestBody(t, uploadTestPart{"file", archive, true}, uploadTestPart{"password", password, false})
	return uploadTestRequest(f, "/api/v1/backup/restore", body, media, true)
}

func TestBusinessBackupEncryptedRoundTripAndTicketOwner(t *testing.T) {
	f, runtime := newBackupHTTPFixture(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	resultTask(t, f)
	resultExec(t, f, `INSERT INTO accounts(id,name,state,created_at) VALUES(17,'本地账号','cooling',1)`)
	resultExec(t, f, `INSERT INTO quota_events(id,account_id,kind,run_id,at) VALUES(51,17,'search',1,1)`)
	archive := requestTestArchive(t, f, runtime)
	resultTask(t, f)
	wrong := requestRestoreArchive(t, f, archive, "wrong-archive-password")
	requireStatus(t, wrong, http.StatusUnprocessableEntity)
	if errorCode(wrong) != backup.ErrAuthentication.Error() || strings.Contains(wrong.Body.String(), "wrong-archive-password") {
		t.Fatal("错误口令响应不正确")
	}
	response := requestRestoreArchive(t, f, archive, localArchivePassword)
	requireStatus(t, response, http.StatusOK)
	preview := decodeBody[backup.RestorePreview](t, response)
	public := decodeBody[map[string]any](t, response)
	if len(public) != 3 || preview.Ticket == "" || preview.Manifest.Format != backup.Format ||
		preview.ExpiresAt != f.clock.Load()+(15*time.Minute).Milliseconds() ||
		runtime.validated != 1 || runtime.installAttempts != 0 || resultCount(t, f, "SELECT count(*) FROM tasks") != 2 {
		t.Fatal("预览不应泄漏候选内部数据或替换实时数据库")
	}
	other, err := f.auth.CreateToken(t.Context(), f.session.Identity, "同管理员的另一凭据")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/backup/confirm"
	ticket := `{"ticket":"` + preview.Ticket + `"}`
	response = f.request(http.MethodPost, path, ticket, false, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+other.Token)
	})
	requireStatus(t, response, http.StatusConflict)
	if errorCode(response) != backup.ErrRestoreTicket.Error() || runtime.installAttempts != 0 {
		t.Fatal("其他凭据能消费本次预览")
	}
	response = f.request(http.MethodPost, path, ticket, true)
	requireStatus(t, response, http.StatusOK)
	if restored := decodeBody[map[string]bool](t, response); len(restored) != 1 || !restored["restored"] || runtime.installed != 1 {
		t.Fatal("未使用真实候选完成确认")
	}
	var cookie *http.Cookie
	for _, value := range response.Result().Cookies() {
		if value.Name == cookieName {
			cookie = value
		}
	}
	if cookie == nil || cookie.MaxAge != -1 || cookie.Value != "" || !cookie.HttpOnly || cookie.Path != "/" {
		t.Fatal("确认成功后未清理浏览器会话")
	}
	requireStatus(t, f.request(http.MethodPost, path, ticket, true), http.StatusConflict)
	if runtime.installAttempts != 1 {
		t.Fatal("一次性恢复票据被重复执行")
	}
	assertTransferTempEmpty(t, tmp)
}

func TestBusinessBackupCandidateAuthErrorIsInvalidArchive(t *testing.T) {
	f, runtime := newBackupHTTPFixture(t)
	resultExec(t, f, "UPDATE admin SET password_hash='invalid-private-candidate-hash'")
	response := requestRestoreArchive(t, f, requestTestArchive(t, f, runtime), localArchivePassword)
	requireStatus(t, response, http.StatusUnprocessableEntity)
	value := decodeBody[ErrorEnvelope](t, response)
	if value.Error.Code != backup.ErrInvalidArchive.Error() || len(value.Fields) != 1 || value.Fields[0].Key != "file" ||
		strings.Contains(response.Body.String(), "invalid-private-candidate-hash") || runtime.validated != 1 || runtime.installAttempts != 0 {
		t.Fatal("损坏的候选凭据应归为上传文件错误并去敏")
	}
}

func TestBusinessBackupChecksOriginalIdentityAfterInstallDrain(t *testing.T) {
	for _, test := range []struct {
		name, code string
		status     int
		before     func(context.Context, *businessFixture) error
	}{
		{"吊销", backup.ErrRestoreAuthorization.Error(), http.StatusForbidden, func(ctx context.Context, f *businessFixture) error {
			return f.auth.Logout(ctx, f.session.Identity)
		}},
		{"认证存储故障", auth.ErrStorage.Error(), http.StatusServiceUnavailable, func(ctx context.Context, f *businessFixture) error {
			_, err := f.store.DB().ExecContext(ctx, "DROP TABLE auth_tokens")
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, runtime := newBackupHTTPFixture(t)
			resultTask(t, f)
			response := requestRestoreArchive(t, f, requestTestArchive(t, f, runtime), localArchivePassword)
			requireStatus(t, response, http.StatusOK)
			preview := decodeBody[backup.RestorePreview](t, response)
			runtime.beforeInstallCheck = func(ctx context.Context) error { return test.before(ctx, f) }
			response = f.request(http.MethodPost, "/api/v1/backup/confirm", `{"ticket":"`+preview.Ticket+`"}`, true)
			requireStatus(t, response, test.status)
			if errorCode(response) != test.code || runtime.installAttempts != 1 || runtime.installed != 0 {
				t.Fatal("排空后授权失效或故障仍能安装候选")
			}
		})
	}
}

func TestBusinessBackupStrictInputsAndProtection(t *testing.T) {
	f, runtime := newBackupHTTPFixture(t)
	workPath := filepath.Join(f.dataDir, backup.WorkDirName)
	workBefore, err := os.ReadDir(workPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"export", "restore", "confirm"} {
		path := "/api/v1/backup/" + name
		requireStatus(t, f.request(http.MethodPost, path, "{}", false), http.StatusUnauthorized)
		requireStatus(t, f.request(http.MethodPost, path, "{}", true, crossSiteTransferRequest), http.StatusForbidden)
		requireStatus(t, f.request(http.MethodPost, path+"?unknown=x", "{}", true), http.StatusBadRequest)
	}
	for _, item := range []struct{ route, body string }{
		{"export", `{"password":null}`}, {"export", `{"password":"a","password":"b"}`},
		{"confirm", `{"ticket":null}`}, {"confirm", `{"ticket":"secret","other":"secret"}`},
	} {
		requireStatus(t, f.request(http.MethodPost, "/api/v1/backup/"+item.route, item.body, true), http.StatusBadRequest)
	}
	file := uploadTestPart{"file", "not-an-archive", true}
	password := uploadTestPart{"password", localArchivePassword, false}
	for _, parts := range [][]uploadTestPart{{file}, {password}, {file, password, password}, {file, password, {"other", "", false}}} {
		body, media, _ := uploadTestBody(t, parts...)
		requireStatus(t, uploadTestRequest(f, "/api/v1/backup/restore", body, media, true), http.StatusBadRequest)
	}
	password.contents = strings.Repeat("x", maxBody+1)
	body, media, _ := uploadTestBody(t, file, password)
	requireStatus(t, uploadTestRequest(f, "/api/v1/backup/restore", body, media, true), http.StatusRequestEntityTooLarge)
	if runtime.validated != 0 || runtime.installAttempts != 0 {
		t.Fatal("无效输入触发了候选校验或安装")
	}
	workAfter, err := os.ReadDir(workPath)
	if err != nil || len(workAfter) != len(workBefore) {
		t.Fatal("无效输入改变了备份工作目录", err)
	}
	for i, entry := range workAfter {
		if entry.Name() != workBefore[i].Name() || entry.Type() != workBefore[i].Type() {
			t.Fatal("无效输入留有备份临时文件", entry.Name())
		}
	}
	if files, err := os.ReadDir(f.dataDir); err != nil {
		t.Fatal(err)
	} else {
		for _, file := range files {
			if file.Name() != backup.WorkDirName && strings.HasPrefix(file.Name(), ".backup-") {
				t.Fatal("无效输入留有备份私有目录")
			}
		}
	}
}
