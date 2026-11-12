package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/backup"
)

func TestRuntimeFirstRunRestoreAndPasswordLogin(t *testing.T) {
	source, _ := runtimeFixture(t, backup.KeyDefaultFile)
	target, _ := runtimeFixture(t, backup.KeyDefaultFile)
	for _, table := range []string{"auth_tokens", "admin"} {
		if _, err := target.graph.database.DB().Exec("DELETE FROM " + table); err != nil {
			t.Fatal(err)
		}
	}
	var archive bytes.Buffer
	if err := source.backups.Export(t.Context(), "local-archive-password", &archive); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "local-backup.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(archive.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("password", "local-archive-password"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://kakatta.test/api/v1/backup/restore", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	target.ServeHTTP(response, request)
	var preview backup.RestorePreview
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &preview) != nil || preview.Ticket == "" {
		t.Fatal("新实例不能直接验证备份", response.Code)
	}
	old := target.graph
	confirmed := runtimeRequest(target, auth.Session{}, http.MethodPost, "/api/v1/backup/confirm", `{"ticket":"`+preview.Ticket+`"}`)
	if confirmed.Code != http.StatusOK || !strings.Contains(confirmed.Body.String(), `"restored":true`) || target.graph == old {
		t.Fatal("匿名确认没有实际替换运行代", confirmed.Code)
	}
	status := runtimeRequest(target, auth.Session{}, http.MethodGet, "/api/v1/setup", "")
	if !strings.Contains(status.Body.String(), `"initialized":true`) {
		t.Fatal("恢复没有带回管理员")
	}
	denied := runtimeRequest(target, auth.Session{}, http.MethodPost, "/api/v1/backup/confirm", `{}`)
	if denied.Code != http.StatusUnauthorized {
		t.Fatal("恢复后仍能匿名覆盖", denied.Code)
	}
	login := runtimeRequest(target, auth.Session{}, http.MethodPost, "/api/v1/auth/login", `{"username":"本地用户","password":"local-password"}`)
	if login.Code != http.StatusOK {
		t.Fatal("无法用备份密码重新登录", login.Code)
	}
	var session auth.Session
	for _, cookie := range login.Result().Cookies() {
		if cookie.Name == "kakatta_session" {
			session.Token = cookie.Value
		}
	}
	if session.Token == "" || runtimeRequest(target, session, http.MethodGet, "/api/v1/me", "").Code != http.StatusOK {
		t.Fatal("恢复后登录没有建立正常会话")
	}
}
