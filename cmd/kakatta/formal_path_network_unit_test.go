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

func TestFormalAuditFixtureImageKeepsAllNetworkChecks(t *testing.T) {
	const imageOrigin = "http://127.0.0.1:44289"
	for _, test := range []struct {
		name, address, method, identity string
		resource                        network.ResourceType
		configured                      bool
		status                          int
		loadFailed, consoleError        bool
		wantFailure                     bool
	}{
		{"本轮原图正常加载", imageOrigin + "/images/1.jpg", "GET", "authenticated", network.ResourceTypeImage, true, 200, false, false, false},
		{"未配置图片源仍拒绝", imageOrigin + "/images/1.jpg", "GET", "authenticated", network.ResourceTypeImage, false, 200, false, false, true},
		{"初始化阶段拒绝", imageOrigin + "/images/1.jpg", "GET", "setup", network.ResourceTypeImage, true, 200, false, false, true},
		{"恢复身份前拒绝", imageOrigin + "/images/1.jpg", "GET", "restoring", network.ResourceTypeImage, true, 200, false, false, true},
		{"外域拒绝", "https://example.invalid/images/1.jpg", "GET", "authenticated", network.ResourceTypeImage, true, 200, false, false, true},
		{"不同回环端口拒绝", "http://127.0.0.1:44290/images/1.jpg", "GET", "authenticated", network.ResourceTypeImage, true, 200, false, false, true},
		{"写请求拒绝", imageOrigin + "/images/1.jpg", "POST", "authenticated", network.ResourceTypeImage, true, 200, false, false, true},
		{"非图片资源拒绝", imageOrigin + "/images/1.jpg", "GET", "authenticated", network.ResourceTypeFetch, true, 200, false, false, true},
		{"非数字图片拒绝", imageOrigin + "/images/avatar.jpg", "GET", "authenticated", network.ResourceTypeImage, true, 200, false, false, true},
		{"其它路径拒绝", imageOrigin + "/assets/1.jpg", "GET", "authenticated", network.ResourceTypeImage, true, 200, false, false, true},
		{"同夹具API拒绝", imageOrigin + "/api/v1/settings", "GET", "authenticated", network.ResourceTypeImage, true, 200, false, false, true},
		{"查询参数拒绝", imageOrigin + "/images/1.jpg?token=synthetic-private-query", "GET", "authenticated", network.ResourceTypeImage, true, 200, false, false, true},
		{"空查询参数拒绝", imageOrigin + "/images/1.jpg?", "GET", "authenticated", network.ResourceTypeImage, true, 200, false, false, true},
		{"userinfo拒绝", "http://synthetic-private-user@127.0.0.1:44289/images/1.jpg", "GET", "authenticated", network.ResourceTypeImage, true, 200, false, false, true},
		{"fragment拒绝", imageOrigin + "/images/1.jpg#hidden", "GET", "authenticated", network.ResourceTypeImage, true, 200, false, false, true},
		{"编码路径拒绝", imageOrigin + "/images/%31.jpg", "GET", "authenticated", network.ResourceTypeImage, true, 200, false, false, true},
		{"图片404保留错误", imageOrigin + "/images/1.jpg", "GET", "authenticated", network.ResourceTypeImage, true, 404, false, false, true},
		{"图片503保留错误", imageOrigin + "/images/1.jpg", "GET", "authenticated", network.ResourceTypeImage, true, 503, false, false, true},
		{"加载失败保留错误", imageOrigin + "/images/1.jpg", "GET", "authenticated", network.ResourceTypeImage, true, 0, true, false, true},
		{"控制台错误保留", imageOrigin + "/images/1.jpg", "GET", "authenticated", network.ResourceTypeImage, true, 200, false, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var trace bytes.Buffer
			events := &webSmokeEvents{base: formalBaseURL}
			a := newFormalRequestAudit("first", &trace, events, nil)
			a.identity = test.identity
			if test.configured {
				if err := a.configureFixtureImages(imageOrigin + "/"); err != nil {
					t.Fatal(err)
				}
			}
			a.receive(&network.EventRequestWillBeSent{RequestID: "image", Type: test.resource, Request: &network.Request{Method: test.method, URL: test.address}, Initiator: &network.Initiator{Type: network.InitiatorTypeOther}})
			if !test.wantFailure && (len(events.pending) != 1 || events.lastActivity.IsZero()) {
				t.Error("已接纳的图片必须计入页面请求等待")
			}
			if test.status != 0 {
				a.receive(&network.EventResponseReceived{RequestID: "image", Response: &network.Response{URL: test.address, Status: int64(test.status)}})
			}
			if test.loadFailed {
				a.receive(&network.EventLoadingFailed{RequestID: "image", ErrorText: "本地图片模拟加载失败"})
			} else {
				a.receive(&network.EventLoadingFinished{RequestID: "image"})
			}
			if test.consoleError {
				a.receive(&runtime.EventConsoleAPICalled{Type: runtime.ConsoleAPICalledTypeError})
			}
			a.finish()
			if got := len(events.issues) > 0; got != test.wantFailure {
				t.Fatal("图片请求审计判定不符", events.issues)
			}
			issues := strings.Join(events.issues, "\n")
			if test.status >= 400 && !strings.Contains(issues, "HTTP "+strconv.Itoa(test.status)) {
				t.Fatal("图片HTTP错误没有继续检查", issues)
			}
			if test.loadFailed && !strings.Contains(issues, "本地图片模拟加载失败") {
				t.Fatal("图片加载失败没有继续检查", issues)
			}
			if test.consoleError && !strings.Contains(issues, "控制台 error") {
				t.Fatal("图片加载期间控制台错误没有继续检查", issues)
			}
			if len(events.pending) != 0 {
				t.Fatal("图片结束后仍占用页面请求等待")
			}
			lines := bytes.Split(bytes.TrimSpace(trace.Bytes()), []byte{'\n'})
			if test.status != 0 && len(lines) != 2 {
				t.Fatal("图片请求和响应必须完整保留", trace.String())
			}
			var record formalRequestRecord
			if json.Unmarshal(lines[0], &record) != nil || record.Path == "" || record.Method != test.method || record.Initiator == nil || record.Initiator.Type != network.InitiatorTypeOther {
				t.Fatal("图片请求来源记录不完整", trace.String())
			}
			if !test.wantFailure && record.Origin != imageOrigin {
				t.Fatal("图片记录缺少准确的夹具来源", trace.String())
			}
			if bytes.Contains(trace.Bytes(), []byte("synthetic-private")) {
				t.Fatal("图片记录不得带查询凭据或userinfo")
			}
		})
	}
}

func TestFormalAuditFixtureImageOriginMustBeExactLoopback(t *testing.T) {
	for _, home := range []string{"https://127.0.0.1:44289/", "http://localhost:44289/", "http://example.invalid:44289/", "http://127.0.0.1/", "http://127.0.0.1:0/", "http://127.0.0.1:65536/", "http://user@127.0.0.1:44289/", "http://127.0.0.1:44289/?secret=1", "http://127.0.0.1:44289/?", "http://127.0.0.1:44289/#fragment", "http://127.0.0.1:44289/#", "http://127.0.0.1:44289/images/"} {
		t.Run(home, func(t *testing.T) {
			a := newFormalRequestAudit("first", &bytes.Buffer{}, &webSmokeEvents{base: formalBaseURL}, nil)
			if err := a.configureFixtureImages(home); err == nil || a.fixtureImageOrigin != "" {
				t.Fatal("不准确的图片夹具来源被配置成功")
			}
		})
	}
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
