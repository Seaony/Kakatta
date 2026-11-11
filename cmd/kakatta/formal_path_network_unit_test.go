package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
)

func formalAuditRequest(a *formalRequestAudit, id, method, path string) {
	a.receive(&network.EventRequestWillBeSent{RequestID: network.RequestID(id), Request: &network.Request{Method: method, URL: formalBaseURL + path, Headers: network.Headers{"Cookie": "synthetic-private-cookie", "Authorization": "synthetic-private-key"}},
		Initiator: &network.Initiator{Type: network.InitiatorTypeScript, Stack: &runtime.StackTrace{CallFrames: []*runtime.CallFrame{{FunctionName: "localSource", URL: formalBaseURL + "/assets/app.js", LineNumber: 7, ColumnNumber: 3}}}}})
}

func formalAuditResponse(a *formalRequestAudit, id, path string, status int) {
	a.receive(&network.EventResponseReceived{RequestID: network.RequestID(id), Response: &network.Response{URL: formalBaseURL + path, Status: int64(status)}})
}

func TestFormalAuditKeeps401ClosedExceptVerifiedAnonymousIdentity(t *testing.T) {
	for _, test := range []struct {
		name, stage, method, path, body string
		readError                       bool
		wantFailure                     bool
	}{
		{"登录前身份探测", "restart", "GET", "/api/v1/me", `{"error":{"code":"auth.unauthenticated"}}`, false, false},
		{"初始化前不查身份", "first", "GET", "/api/v1/me", `{"error":{"code":"auth.unauthenticated"}}`, false, true},
		{"设置始终受保护", "restart", "GET", "/api/v1/settings", `{"error":{"code":"auth.unauthenticated"}}`, false, true},
		{"写请求不放行", "restart", "POST", "/api/v1/me", `{"error":{"code":"auth.unauthenticated"}}`, false, true},
		{"查询参数不能借用例外", "restart", "GET", "/api/v1/me?unexpected=1", `{"error":{"code":"auth.unauthenticated"}}`, false, true},
		{"其他原因不放行", "restart", "GET", "/api/v1/me", `{"error":{"code":"auth.invalid_credentials"}}`, false, true},
		{"坏正文不放行", "restart", "GET", "/api/v1/me", `{`, false, true},
		{"正文读取失败不放行", "restart", "GET", "/api/v1/me", "", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var trace bytes.Buffer
			events := &webSmokeEvents{base: formalBaseURL}
			a := newFormalRequestAudit(test.stage, &trace, events, func(network.RequestID) ([]byte, error) {
				if test.readError {
					return nil, errors.New("本地模拟正文不可读")
				}
				return []byte(test.body), nil
			})
			if test.stage != "first" {
				a.identity = "signed_out"
			}
			formalAuditRequest(a, "probe", test.method, test.path)
			formalAuditResponse(a, "probe", test.path, http.StatusUnauthorized)
			a.receive(&log.EventEntryAdded{Entry: &log.Entry{Level: log.EntryLevelError, Source: log.EntrySourceNetwork, NetworkRequestID: "probe", URL: formalBaseURL + test.path, Text: "HTTP 401 Unauthorized"}})
			a.receive(&network.EventLoadingFinished{RequestID: "probe"})
			a.finish()
			if got := len(events.issues) != 0; got != test.wantFailure {
				t.Fatal("401审计判定不符", events.issues)
			}
			var record formalRequestRecord
			if json.Unmarshal(bytes.Split(trace.Bytes(), []byte{'\n'})[0], &record) != nil || record.RequestID != "probe" || record.Method != test.method || record.Phase == "" || record.Initiator.Stack.CallFrames[0].FunctionName != "localSource" {
				t.Fatal("请求记录缺少方法、阶段或发起栈")
			}
			if bytes.Contains(trace.Bytes(), []byte("synthetic-private")) {
				t.Fatal("请求记录包含了凭据Header")
			}
		})
	}
}

func TestFormalAuditSetupOnlyReadsSetupUntilExplicitSubmission(t *testing.T) {
	for _, test := range []struct {
		identity, method, path string
		wantFailure            bool
	}{{"setup", "GET", "/api/v1/setup", false}, {"setup", "GET", "/assets/app.js", false}, {"setup", "GET", "/api/v1/settings", true}, {"setup", "POST", "/api/v1/setup", true}, {"initializing", "POST", "/api/v1/setup", false}, {"initializing", "GET", "/api/v1/me", true}} {
		t.Run(test.identity+test.method+test.path, func(t *testing.T) {
			events := &webSmokeEvents{base: formalBaseURL}
			a := newFormalRequestAudit("first", &bytes.Buffer{}, events, nil)
			a.identity = test.identity
			formalAuditRequest(a, "read", test.method, test.path)
			a.finish()
			if got := len(events.issues) != 0; got != test.wantFailure {
				t.Fatal("初始化期请求范围不符", events.issues)
			}
		})
	}
}

func TestFormalAuditDoesNotIgnoreUnrelatedOrUnfinished401ConsoleEntries(t *testing.T) {
	for _, requestID := range []network.RequestID{"other", "probe"} {
		events := &webSmokeEvents{base: formalBaseURL}
		a := newFormalRequestAudit("restart", &bytes.Buffer{}, events, nil)
		a.identity = "signed_out"
		formalAuditRequest(a, "probe", "GET", "/api/v1/me")
		a.receive(&log.EventEntryAdded{Entry: &log.Entry{Level: log.EntryLevelError, Source: log.EntrySourceNetwork, URL: formalBaseURL + "/api/v1/me", NetworkRequestID: requestID, Text: "HTTP 401 Unauthorized"}})
		a.finish()
		if len(events.issues) == 0 {
			t.Fatal("没有对应已核验HTTP响应的401被忽略")
		}
	}
}

func TestFormalAuditRevokesIdentityExceptionAfterSuccessfulLoginOrRestore(t *testing.T) {
	for _, success := range []struct{ method, path, identity string }{{"GET", "/api/v1/me", "signed_out"}, {"POST", "/api/v1/setup", "initializing"}, {"POST", "/api/v1/auth/login", "signed_out"}, {"POST", "/api/v1/auth/totp", "signed_out"}} {
		t.Run(success.path, func(t *testing.T) {
			events := &webSmokeEvents{base: formalBaseURL}
			a := newFormalRequestAudit("restart", &bytes.Buffer{}, events, nil)
			a.identity = success.identity
			formalAuditRequest(a, "success", success.method, success.path)
			formalAuditResponse(a, "success", success.path, http.StatusOK)
			if a.identity != "authenticated" {
				t.Fatal("成功登录或恢复后没有关闭匿名期")
			}
			formalAuditRequest(a, "lost", http.MethodGet, "/api/v1/me")
			formalAuditResponse(a, "lost", "/api/v1/me", http.StatusUnauthorized)
			a.finish()
			if len(events.issues) == 0 {
				t.Fatal("成功登录后仍放行了401")
			}
		})
	}
}

func TestFormalAuditRestorationRequiresExistingSession(t *testing.T) {
	for _, stage := range []string{"restart", "upgrade"} {
		for _, test := range []struct {
			method, path string
			status       int
			wantFailure  bool
		}{{"GET", "/api/v1/setup", 200, false}, {"GET", "/api/v1/me", 200, false}, {"GET", "/api/v1/me", 401, true}, {"POST", "/api/v1/auth/login", 200, true}, {"POST", "/api/v1/auth/totp", 200, true}, {"GET", "/api/v1/settings", 200, true}} {
			t.Run(stage+test.method+test.path+strconv.Itoa(test.status), func(t *testing.T) {
				events := &webSmokeEvents{base: formalBaseURL}
				a := newFormalRequestAudit(stage, &bytes.Buffer{}, events, nil)
				if a.identity != "restoring" {
					t.Fatal("重启和升级必须恢复原会话，不能进入允许重新登录的阶段")
				}
				formalAuditRequest(a, "restore", test.method, test.path)
				formalAuditResponse(a, "restore", test.path, test.status)
				a.finish()
				if got := len(events.issues) != 0; got != test.wantFailure {
					t.Fatal("恢复原会话的请求限制不符", events.issues)
				}
				if test.method == "GET" && test.path == "/api/v1/me" && test.status == 200 && a.identity != "authenticated" {
					t.Fatal("原会话恢复成功后没有进入已登录状态")
				}
			})
		}
	}
}

func TestFormalInitialTokenRequiresPrivateRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "初始化令牌.txt")
	token := strings.Repeat("x", 43)
	if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := formalInitialToken(path); err != nil || got != token {
		t.Fatal("合法令牌文件未通过", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := formalInitialToken(path); err == nil {
		t.Fatal("公开权限的令牌文件未拒绝")
	}
	link := filepath.Join(t.TempDir(), "初始化令牌.txt")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := formalInitialToken(link); err == nil {
		t.Fatal("令牌文件符号链接未拒绝")
	}
}
