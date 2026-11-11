package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/debugger"
	"github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

type formalNetworkKey struct{}

type formalRequestRecord struct {
	Event      string             `json:"event"`
	RequestID  network.RequestID  `json:"request_id"`
	Method     string             `json:"method"`
	Path       string             `json:"path"`
	Origin     string             `json:"origin,omitempty"`
	Phase      string             `json:"phase"`
	Identity   string             `json:"identity"`
	Initiator  *network.Initiator `json:"initiator"`
	Local      bool               `json:"exact_local_url"`
	Status     int                `json:"status,omitempty"`
	Reason     string             `json:"reason,omitempty"`
	Allowed    bool               `json:"allowed"`
	Network401 bool               `json:"network_401,omitempty"`
}

type formalRequestAudit struct {
	mu                 sync.Mutex
	phase              string
	identity           string
	fixtureImageOrigin string
	closed             bool
	requests           map[network.RequestID]*formalRequestRecord
	scripts            map[cdp.ScriptID]string
	writer             io.Writer
	events             *webSmokeEvents
	readBody           func(network.RequestID) ([]byte, error)
	pending            sync.WaitGroup
}

func newFormalRequestAudit(stage string, writer io.Writer, events *webSmokeEvents, readBody func(network.RequestID) ([]byte, error)) *formalRequestAudit {
	identity := "restoring"
	if stage == "first" {
		identity = "setup"
	}
	return &formalRequestAudit{phase: "01身份/" + stage, identity: identity, requests: make(map[network.RequestID]*formalRequestRecord), scripts: make(map[cdp.ScriptID]string), writer: writer, events: events, readBody: readBody}
}

func (a *formalRequestAudit) configureFixtureImages(home string) error {
	address, err := url.Parse(home)
	if err != nil || address.Scheme != "http" || address.Hostname() != "127.0.0.1" || address.User != nil ||
		address.Opaque != "" || address.RawQuery != "" || address.ForceQuery || address.Fragment != "" || address.RawFragment != "" ||
		address.RawPath != "" || (address.Path != "" && address.Path != "/") {
		return fmt.Errorf("正式图片夹具必须来自明确的本机回环地址")
	}
	port, err := strconv.Atoi(address.Port())
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("正式图片夹具必须指定有效端口")
	}
	origin := address.Scheme + "://" + address.Host
	if home != origin && home != origin+"/" {
		return fmt.Errorf("正式图片夹具不能带查询参数或片段")
	}
	a.fixtureImageOrigin = origin
	return nil
}

func formalNetworkPhase(ctx context.Context, phase string) {
	if audit, ok := ctx.Value(formalNetworkKey{}).(*formalRequestAudit); ok {
		audit.mu.Lock()
		audit.phase = phase
		audit.mu.Unlock()
	}
}

func formalInitializationSubmission(ctx context.Context) {
	if audit, ok := ctx.Value(formalNetworkKey{}).(*formalRequestAudit); ok {
		audit.mu.Lock()
		audit.identity = "initializing"
		audit.mu.Unlock()
	}
}

func (a *formalRequestAudit) issue(message string) {
	a.events.mu.Lock()
	a.events.issues = append(a.events.issues, "正式请求审计："+message)
	a.events.mu.Unlock()
}

func (a *formalRequestAudit) write(record formalRequestRecord) {
	if err := json.NewEncoder(a.writer).Encode(record); err != nil {
		a.issue("保存请求来源失败：" + err.Error())
	}
}

func (a *formalRequestAudit) receive(event any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	fixtureImage := false
	switch value := event.(type) {
	case *network.EventRequestWillBeSent:
		address, err := url.Parse(value.Request.URL)
		if err != nil {
			a.issue("请求地址无效")
			break
		}
		record := &formalRequestRecord{Event: "request", RequestID: value.RequestID, Method: value.Request.Method, Path: address.Path,
			Phase: a.phase, Identity: a.identity, Initiator: value.Initiator, Local: value.Request.URL == formalBaseURL+address.Path, Allowed: true}
		if address.Host != "" {
			record.Origin = address.Scheme + "://" + address.Host
		}
		name, imagePath := strings.CutPrefix(address.Path, "/images/")
		number, jpeg := strings.CutSuffix(name, ".jpg")
		fixtureImage = a.identity == "authenticated" && a.fixtureImageOrigin != "" &&
			value.Request.Method == http.MethodGet && value.Type == network.ResourceTypeImage &&
			imagePath && jpeg && number != "" && strings.Trim(number, "0123456789") == "" &&
			value.Request.URL == a.fixtureImageOrigin+address.Path
		a.requests[value.RequestID] = record
		if address.Host == "127.0.0.1:18080" && len(address.Path) >= 5 && address.Path[:5] == "/api/" && a.identity != "authenticated" {
			record.Allowed = record.Local && (record.Method == http.MethodGet && record.Path == "/api/v1/setup" ||
				a.identity == "initializing" && record.Method == http.MethodPost && record.Path == "/api/v1/setup" ||
				a.identity == "restoring" && record.Method == http.MethodGet && record.Path == "/api/v1/me" ||
				a.identity == "signed_out" && (record.Method == http.MethodGet && record.Path == "/api/v1/me" || record.Method == http.MethodPost && (record.Path == "/api/v1/auth/login" || record.Path == "/api/v1/auth/totp")))
			if !record.Allowed {
				a.issue(fmt.Sprintf("%s/%s 发起未授权请求 %s %s（request_id=%s），来源见请求记录", record.Phase, record.Identity, record.Method, record.Path, record.RequestID))
			}
		}
		a.write(*record)
	case *network.EventResponseReceived:
		if record := a.requests[value.RequestID]; record != nil {
			record.Status, record.Event = int(value.Response.Status), "response"
			if record.Local && record.Allowed && record.Status == http.StatusOK && (record.Method == http.MethodGet && record.Path == "/api/v1/me" || record.Method == http.MethodPost && (record.Path == "/api/v1/setup" || record.Path == "/api/v1/auth/login" || record.Path == "/api/v1/auth/totp")) {
				a.identity = "authenticated"
			}
			if record.Status == http.StatusUnauthorized && !formalExpectedIdentityProbe(*record) {
				a.issue(fmt.Sprintf("%s %s 的401不是允许的匿名身份探测（request_id=%s）", record.Method, record.Path, record.RequestID))
			}
			a.write(*record)
		}
	case *network.EventLoadingFinished:
		if record := a.requests[value.RequestID]; record != nil && record.Status == http.StatusUnauthorized && formalExpectedIdentityProbe(*record) {
			copy := *record
			a.pending.Add(1)
			go a.checkIdentityReason(copy)
		}
	case *debugger.EventPaused:
		stack := &runtime.StackTrace{}
		for _, frame := range value.CallFrames {
			location := frame.Location
			var column int64
			if location.ColumnNumber != nil {
				column = *location.ColumnNumber
			}
			stack.CallFrames = append(stack.CallFrames, &runtime.CallFrame{FunctionName: frame.FunctionName, ScriptID: location.ScriptID, URL: a.scripts[location.ScriptID], LineNumber: location.LineNumber, ColumnNumber: column})
		}
		a.write(formalRequestRecord{Event: "debugger_paused", Phase: a.phase, Identity: a.identity, Reason: string(value.Reason), Initiator: &network.Initiator{Type: network.InitiatorTypeScript, Stack: stack}})
	case *debugger.EventResumed:
		a.write(formalRequestRecord{Event: "debugger_resumed", Phase: a.phase, Identity: a.identity})
	case *debugger.EventScriptParsed:
		a.scripts[value.ScriptID] = value.URL
	case *log.EventEntryAdded:
		entry := value.Entry
		if entry.Level == log.EntryLevelError && entry.Source == log.EntrySourceNetwork && entry.URL == formalBaseURL+"/api/v1/me" && strings.Contains(entry.Text, "401") {
			if record := a.requests[entry.NetworkRequestID]; record != nil && formalExpectedIdentityProbe(*record) && (record.Status == 0 || record.Status == http.StatusUnauthorized) {
				record.Network401 = true
				return
			}
		}
	}
	a.events.mu.Lock()
	// 只有审计已确认的GET /me响应可以绕过通用冒烟的HTTP错误检查。
	a.events.allowSignedOut = false
	a.events.mu.Unlock()
	if value, ok := event.(*network.EventResponseReceived); ok {
		if record := a.requests[value.RequestID]; record != nil && record.Status == http.StatusUnauthorized && formalExpectedIdentityProbe(*record) {
			return
		}
	}
	if fixtureImage {
		request := event.(*network.EventRequestWillBeSent)
		a.events.mu.Lock()
		if a.events.pending == nil {
			a.events.pending = make(map[network.RequestID]struct{})
		}
		a.events.pending[request.RequestID] = struct{}{}
		a.events.lastActivity = time.Now()
		a.events.mu.Unlock()
	} else {
		a.events.receive(event)
	}
}

func formalExpectedIdentityProbe(record formalRequestRecord) bool {
	return record.Local && record.Identity == "signed_out" && record.Method == http.MethodGet && record.Path == "/api/v1/me" && record.Allowed
}

func (a *formalRequestAudit) checkIdentityReason(record formalRequestRecord) {
	defer a.pending.Done()
	body, err := a.readBody(record.RequestID)
	var response struct {
		Error struct{ Code string } `json:"error"`
	}
	valid := err == nil && json.Unmarshal(body, &response) == nil && response.Error.Code == "auth.unauthenticated"
	a.mu.Lock()
	defer a.mu.Unlock()
	record.Event, record.Reason, record.Allowed = "identity_probe", response.Error.Code, valid
	if original := a.requests[record.RequestID]; original != nil {
		original.Reason = response.Error.Code
	}
	if !valid {
		a.issue(fmt.Sprintf("GET /me的401缺少auth.unauthenticated原因（request_id=%s，读取失败=%t）", record.RequestID, err != nil))
	}
	a.write(record)
}

func (a *formalRequestAudit) finish() {
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
	a.pending.Wait()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, record := range a.requests {
		if record.Network401 && record.Status != http.StatusUnauthorized {
			a.issue("身份探测的控制台401没有对应HTTP响应：" + string(record.RequestID))
		}
		if record.Status == http.StatusUnauthorized && formalExpectedIdentityProbe(*record) && record.Reason == "" {
			a.issue("匿名身份探测没有完成原因核验：" + string(record.RequestID))
		}
	}
}

func formalReadResponseBody(browser context.Context, id network.RequestID) ([]byte, error) {
	ctx, cancel := context.WithTimeout(browser, 3*time.Second)
	defer cancel()
	var body []byte
	_, err := chromedp.Run(ctx, chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
		result, err := chromedp.Call(ctx, network.GetResponseBody, network.GetResponseBodyParams{RequestID: id})
		body = result.Body
		return err
	}))
	return body, err
}

func TestFormalIdentityProbeRequiresExactMethodPathAndPhase(t *testing.T) {
	for _, test := range []struct {
		identity, method, path string
		want                   bool
	}{{"signed_out", "GET", "/api/v1/me", true}, {"setup", "GET", "/api/v1/me", false}, {"authenticated", "GET", "/api/v1/me", false}, {"signed_out", "POST", "/api/v1/me", false}, {"signed_out", "GET", "/api/v1/settings", false}} {
		if got := formalExpectedIdentityProbe(formalRequestRecord{Identity: test.identity, Method: test.method, Path: test.path, Allowed: true, Local: true}); got != test.want {
			t.Fatal("身份探测范围过宽", test)
		}
	}
}
