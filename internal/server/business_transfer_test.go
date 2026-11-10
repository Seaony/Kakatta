package server

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
)

func newTransferHTTPFixture(t *testing.T, afterSnapshot ...func() error) (*businessFixture, *atomic.Bool) {
	t.Helper()
	f := prepareBusinessFixture(t)
	active := new(atomic.Bool)
	var err error
	f.service, err = service.New(t.Context(), service.Dependencies{
		DB: f.store.DB(), Settings: f.settings.base, Cipher: f.cipher, DataDir: f.dataDir,
		Resources: failedBusinessResources{}, Models: failedBusinessModels{}, Notifier: failedBusinessNotifier{}, ImageCache: f.imageCache,
		ReadSnapshot: func(ctx context.Context, read func(*sql.Tx) error) error {
			active.Store(true)
			defer active.Store(false)
			err := f.store.ReadSnapshot(ctx, read)
			if err == nil && len(afterSnapshot) != 0 {
				err = afterSnapshot[0]()
			}
			return err
		},
		Now: func() time.Time { return time.UnixMilli(f.clock.Load()) },
		Publish: func(kind string, reason *locales.Message, data json.RawMessage) error {
			return f.server.Publish(Event{Type: kind, Reason: reason, Data: data})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.service.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := f.server.RegisterService(f.service); err != nil {
		t.Fatal(err)
	}
	if err := f.server.RegisterTransfers(f.service); err != nil {
		t.Fatal(err)
	}
	return f, active
}

type uploadTestPart struct {
	name, contents string
	file           bool
}

func uploadTestBody(t *testing.T, parts ...uploadTestPart) (string, string, string) {
	t.Helper()
	var body bytes.Buffer
	multipartWriter := multipart.NewWriter(&body)
	for _, part := range parts {
		var writer io.Writer
		var err error
		if part.file {
			writer, err = multipartWriter.CreateFormFile(part.name, "local-fixture")
		} else {
			writer, err = multipartWriter.CreateFormField(part.name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(writer, part.contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := multipartWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return body.String(), multipartWriter.FormDataContentType(), multipartWriter.Boundary()
}

func uploadTestRequest(f *businessFixture, path, body, media string, authenticated bool, options ...func(*http.Request)) *httptest.ResponseRecorder {
	return f.request(http.MethodPost, path, body, authenticated, append([]func(*http.Request){func(r *http.Request) {
		r.Header.Set("Content-Type", media)
	}}, options...)...)
}

func TestBusinessTransfersTruncatedMultipartWritesNothing(t *testing.T) {
	f, _ := newTransferHTTPFixture(t)
	body, media, boundary := uploadTestBody(t, uploadTestPart{"file", `{"format":"kakatta.tasks","version":1,"tasks":[{"keyword":"不可保存"}]}`, true})
	body = strings.TrimSuffix(body, "--"+boundary+"--\r\n") + "--" + boundary
	response := uploadTestRequest(f, "/api/v1/tasks/import", body, media, true)
	requireStatus(t, response, http.StatusBadRequest)
	if resultCount(t, f, "SELECT COUNT(*) FROM tasks") != 0 {
		t.Fatal("不完整的上传写入了任务")
	}
}

func TestBusinessTransfersHTTPBodyCompletionAndProgress(t *testing.T) {
	f, _ := newTransferHTTPFixture(t)
	local := httptest.NewServer(f.server)
	defer local.Close()
	body, media, _ := uploadTestBody(t, uploadTestPart{"file", `{"format":"kakatta.tasks","version":1,"tasks":[{"keyword":"本地上传"}]}`, true})
	open := func(t *testing.T, size int) *net.TCPConn {
		t.Helper()
		raw, err := net.DialTimeout("tcp", local.Listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		conn := raw.(*net.TCPConn)
		t.Cleanup(func() { _ = conn.Close() })
		if err := conn.SetDeadline(time.Now().Add(18 * time.Second)); err != nil {
			t.Fatal(err)
		}
		_, err = fmt.Fprintf(conn, "POST /api/v1/tasks/import HTTP/1.1\r\nHost: %s\r\nConnection: close\r\nContent-Type: %s\r\nContent-Length: %d\r\nCookie: %s=%s\r\n\r\n",
			local.Listener.Addr().String(), media, size, cookieName, f.session.Token)
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}
	check := func(t *testing.T, conn *net.TCPConn, status int) {
		t.Helper()
		response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != status {
			t.Fatalf("响应状态为 %d，预期 %d", response.StatusCode, status)
		}
		if _, err := io.Copy(io.Discard, response.Body); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("分界完整但传输截断", func(t *testing.T) {
		conn := open(t, len(body)+1)
		if _, err := io.WriteString(conn, body); err != nil {
			t.Fatal(err)
		}
		if err := conn.CloseWrite(); err != nil {
			t.Fatal(err)
		}
		check(t, conn, http.StatusBadRequest)
		if resultCount(t, f, "SELECT COUNT(*) FROM tasks") != 0 {
			t.Fatal("HTTP 请求尚未完整到达就写入了任务")
		}
	})
	t.Run("持续读取可以超过初始期限", func(t *testing.T) {
		conn := open(t, len(body))
		for i := range 4 {
			if i > 0 {
				time.Sleep(3500 * time.Millisecond)
			}
			if _, err := io.WriteString(conn, body[i*len(body)/4:(i+1)*len(body)/4]); err != nil {
				t.Fatal(err)
			}
		}
		check(t, conn, http.StatusOK)
		if resultCount(t, f, "SELECT COUNT(*) FROM tasks WHERE keyword='本地上传'") != 1 {
			t.Fatal("持续上传超过初始期限后未正常导入")
		}
	})
}

func TestBusinessTransfersImportValidatesUploadBeforeWritingAndKeepsPartialReport(t *testing.T) {
	f, _ := newTransferHTTPFixture(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	path := "/api/v1/tasks/import"
	file := uploadTestPart{"file", `{"format":"kakatta.tasks","version":1,"tasks":[{"keyword":"saved"},{"keyword":"fail"},{"keyword":"left"}]}`, true}
	for _, parts := range [][]uploadTestPart{nil, {file, file}, {file, {"other", "private-input", false}}} {
		body, media, _ := uploadTestBody(t, parts...)
		requireStatus(t, uploadTestRequest(f, path, body, media, true), http.StatusBadRequest)
	}
	body, media, _ := uploadTestBody(t, file)
	requireStatus(t, uploadTestRequest(f, path, body, "application/json", true), http.StatusUnsupportedMediaType)
	requireStatus(t, uploadTestRequest(f, path, body, media, false), http.StatusUnauthorized)
	requireStatus(t, uploadTestRequest(f, path, body, media, true, crossSiteTransferRequest), http.StatusForbidden)
	if resultCount(t, f, "SELECT COUNT(*) FROM tasks") != 0 {
		t.Fatal("上传字段或鉴权不合法时写入了任务")
	}
	resultExec(t, f, `CREATE TRIGGER fail_import BEFORE INSERT ON tasks WHEN NEW.keyword='fail' BEGIN SELECT RAISE(ABORT,'private-sql-error'); END`)
	file.contents = strings.Repeat(" ", maxBody+1) + file.contents
	body, media, _ = uploadTestBody(t, file)
	response := uploadTestRequest(f, path, body, media, true)
	requireStatus(t, response, http.StatusOK)
	report := decodeBody[service.TaskImportReport](t, response)
	if report.Complete || report.Total != 3 || report.Imported != 1 || report.Failed != 1 || report.Unprocessed != 1 ||
		report.Failure == nil || len(report.Items) != 2 || strings.Contains(response.Body.String(), "private-sql-error") ||
		resultCount(t, f, "SELECT COUNT(*) FROM tasks WHERE keyword='saved' AND baseline_pending=1") != 1 ||
		resultCount(t, f, "SELECT COUNT(*) FROM tasks") != 1 {
		t.Fatalf("实际导入与部分提交报告不一致：%+v", report)
	}
	assertTransferTempEmpty(t, tmp)
}

func crossSiteTransferRequest(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }

func assertTransferTempEmpty(t *testing.T, directory string) {
	t.Helper()
	if entries, err := os.ReadDir(directory); err != nil || len(entries) != 0 {
		t.Fatal("请求结束后仍留有临时文件", entries, err)
	}
}

type stagedHTTPRecorder struct {
	*httptest.ResponseRecorder
	beforeHeader func(int)
}

func (w stagedHTTPRecorder) WriteHeader(status int) {
	w.beforeHeader(status)
	w.ResponseRecorder.WriteHeader(status)
}

func TestBusinessTransfersDownloadAfterSnapshotAndRejectPartialOutput(t *testing.T) {
	var fail atomic.Bool
	f, active := newTransferHTTPFixture(t, func() error {
		if fail.Load() {
			return errors.New("private-snapshot-error")
		}
		return nil
	})
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	taskID := resultTask(t, f)
	otherTask := resultTask(t, f)
	for i := range 205 {
		resultFinding(t, f, taskID, fmt.Sprintf("export-%03d", i), int64(i), false)
	}
	resultFinding(t, f, taskID, "excluded", 900, false)
	resultFinding(t, f, otherTask, "export-other-task", 901, false)
	path := fmt.Sprintf("/api/v1/tasks/%d/findings/export?view=all&decision=matched&sort=latest&q=export", taskID)
	request := httptest.NewRequest(http.MethodGet, "http://kakatta.test"+path, nil)
	request.AddCookie(&http.Cookie{Name: cookieName, Value: f.session.Token})
	recorder := httptest.NewRecorder()
	f.server.ServeHTTP(stagedHTTPRecorder{recorder, func(status int) {
		if status != http.StatusOK || active.Load() {
			t.Fatal("开始下载时数据库快照尚未释放")
		}
		entries, err := os.ReadDir(tmp)
		if err != nil || len(entries) != 1 {
			t.Fatal("下载应先在私有临时文件生成", entries, err)
		}
		info, err := entries[0].Info()
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("下载临时文件权限错误", info, err)
		}
	}}, request)
	requireStatus(t, recorder, http.StatusOK)
	body := recorder.Body.String()
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(body, "\ufeff"))).ReadAll()
	if err != nil || len(rows) != 206 || !strings.HasPrefix(body, "\ufeff") ||
		!strings.Contains(strings.Join(rows[1], " "), "export-204") ||
		!strings.Contains(strings.Join(rows[205], " "), "export-000") ||
		!strings.HasPrefix(recorder.Header().Get("Content-Type"), "text/csv") ||
		!strings.Contains(recorder.Header().Get("Content-Disposition"), "attachment") ||
		recorder.Header().Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Fatal("CSV 不是完整的筛选与排序结果", len(rows), err)
	}
	for _, parameter := range []string{"limit=1", "cursor=x", "task_id=1"} {
		requireStatus(t, f.request(http.MethodGet, path+"&"+parameter, "", true), http.StatusBadRequest)
	}
	requireStatus(t, f.request(http.MethodGet, path, "", false), http.StatusUnauthorized)
	requireStatus(t, f.request(http.MethodGet, "/api/v1/tasks/export", "", false), http.StatusUnauthorized)
	fail.Store(true)
	response := f.request(http.MethodGet, path, "", true)
	requireStatus(t, response, http.StatusServiceUnavailable)
	if response.Header().Get("Content-Disposition") != "" || strings.Contains(response.Body.String(), "export-204") ||
		strings.Contains(response.Body.String(), "private-snapshot-error") {
		t.Fatal("生成失败时泄漏部分文件或底层错误")
	}
	assertTransferTempEmpty(t, tmp)
}

func TestBusinessTransfersRunLogLanguageAndFilters(t *testing.T) {
	f, _ := newTransferHTTPFixture(t)
	task := resultTask(t, f)
	now := f.clock.Load()
	for _, row := range []struct {
		name string
		at   int64
	}{{"wanted-task", now}, {"other-task", now}, {"wanted-older", now - 1000}} {
		resultExec(t, f, `INSERT INTO run_records(task_id,task_name,trigger,started_at,ended_at,outcome,failure_kind,page_count,warnings_json,message)
			VALUES(?,?,'manual',?,?,'failed','restart',0,'[]','{"code":"run.no_account","args":{}}')`, task, row.name, row.at, row.at)
	}
	path := fmt.Sprintf("/api/v1/runs/export?task_id=%d&outcome=failed&failure_kind=restart&q=wanted&from_at=%d&to_at=%d", task, now-500, now+1)
	requireStatus(t, f.request(http.MethodGet, path, "", false), http.StatusUnauthorized)
	requireStatus(t, f.request(http.MethodGet, path+"&limit=1", "", true), http.StatusBadRequest)
	for _, preference := range []struct{ setting, header string }{{"system", "en-US,zh-CN;q=0.5"}, {"en", "zh-CN"}} {
		encoded, err := json.Marshal(preference.setting)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.settings.Apply(t.Context(), settings.Values{"general.language": encoded}); err != nil {
			t.Fatal(err)
		}
		response := f.request(http.MethodGet, path, "", true, func(r *http.Request) {
			r.Header.Set("Accept-Language", preference.header)
		})
		requireStatus(t, response, http.StatusOK)
		translated, err := locales.Format(locales.En, locales.Message{Code: "run.no_account"})
		body := response.Body.String()
		if err != nil || !strings.Contains(body, translated) || !strings.Contains(body, "wanted-task") ||
			strings.Contains(body, "other-task") || strings.Contains(body, "wanted-older") ||
			!strings.HasPrefix(response.Header().Get("Content-Type"), "text/plain") || strings.Count(body, "\n") != 2 {
			t.Fatal("日志语言或筛选错误", preference, body)
		}
	}
}

func TestBusinessTransfersRegisterBesideTaskParameterRoute(t *testing.T) {
	f, _ := newTransferHTTPFixture(t)
	response := f.request(http.MethodGet, "/api/v1/tasks/export", "", true)
	requireStatus(t, response, http.StatusOK)
	value := decodeBody[struct {
		Format string            `json:"format"`
		Tasks  []json.RawMessage `json:"tasks"`
	}](t, response)
	if value.Format != "kakatta.tasks" || value.Tasks == nil || len(value.Tasks) != 0 {
		t.Fatal("静态导出路由必须覆盖任务编号路由并返回真实空任务文件")
	}
	if f.server.RegisterTransfers(nil) == nil || f.server.RegisterTransfers(f.service) == nil {
		t.Fatal("空服务或重复装配必须拒绝")
	}
}
