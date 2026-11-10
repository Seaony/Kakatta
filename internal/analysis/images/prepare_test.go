package images

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
)

type prepareDoer func(*http.Request) (*http.Response, error)

func (d prepareDoer) Do(r *http.Request) (*http.Response, error) { return d(r) }

func prepareFixtureJPEG(t *testing.T) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, 24, 12))
	for y := range 12 {
		for x := range 24 {
			picture.Set(x, y, color.RGBA{R: uint8(x * 9), G: uint8(y * 17), B: 80, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, picture, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func newTestPreparer(t *testing.T, deps Dependencies) *Preparer {
	t.Helper()
	p, err := New(deps)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func successResponse(body []byte) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}
}

func TestPrepareNewValidatesDependenciesAndLimits(t *testing.T) {
	var typedNil *http.Client
	for _, tc := range []struct {
		name string
		deps Dependencies
	}{
		{"nil", Dependencies{}},
		{"typed_nil", Dependencies{HTTP: typedNil}},
		{"negative_bytes", Dependencies{HTTP: http.DefaultClient, Limits: Limits{SourceBytes: -1}}},
		{"negative_pixels", Dependencies{HTTP: http.DefaultClient, Limits: Limits{SourcePixels: -1}}},
		{"large_bytes", Dependencies{HTTP: http.DefaultClient, Limits: Limits{SourceBytes: SourceBytes + 1}}},
		{"large_pixels", Dependencies{HTTP: http.DefaultClient, Limits: Limits{SourcePixels: SourcePixels + 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if p, err := New(tc.deps); p != nil || err == nil || err.Error() != "analysis.images.invalid_configuration" {
				t.Fatalf("非法依赖不能构造准备器：%v %v", p, err)
			}
		})
	}
	p := newTestPreparer(t, Dependencies{HTTP: http.DefaultClient})
	if p.limits != (Limits{SourceBytes, SourcePixels}) || cap(p.slots) != DownloadConcurrency {
		t.Fatalf("默认边界不对：%+v", p.limits)
	}
	p = newTestPreparer(t, Dependencies{HTTP: http.DefaultClient, Limits: Limits{SourceBytes: 10, SourcePixels: 30}})
	if p.limits != (Limits{10, 30}) {
		t.Fatalf("测试边界不能被扩大：%+v", p.limits)
	}
}

func TestPrepareKeepsPrefixAndOrderWithoutRefillingFailures(t *testing.T) {
	picture := prepareFixtureJPEG(t)
	third := make(chan struct{})
	var fourth atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/first":
			select {
			case <-third:
			case <-r.Context().Done():
				return
			}
		case "/bad":
			w.WriteHeader(http.StatusNotFound)
			return
		case "/third":
			close(third)
		case "/fourth":
			fourth.Add(1)
		}
		_, _ = w.Write(picture)
	}))
	defer srv.Close()
	p := newTestPreparer(t, Dependencies{HTTP: srv.Client()})
	result, err := p.Prepare(context.Background(), []string{srv.URL + "/first", srv.URL + "/bad", srv.URL + "/third", srv.URL + "/fourth"}, 3)
	assertIssueTranslations(t, result.Skipped)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Images) != 2 || result.Images[0].DetailIndex != 0 || result.Images[1].DetailIndex != 2 || fourth.Load() != 0 {
		t.Fatalf("结果应按原图保序且不补失败图：%+v，后续请求%d", result, fourth.Load())
	}
	if len(result.Skipped) != 1 || result.Skipped[0].DetailIndex != 1 || result.Skipped[0].Reason.Code != "analysis.images.http_failed" || result.Skipped[0].Reason.Args["status"] != 404 {
		t.Fatalf("逐图失败原因不对：%+v", result.Skipped)
	}
	for _, data := range result.DataURLs() {
		source, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(data, "data:image/jpeg;base64,"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := jpeg.Decode(bytes.NewReader(source)); err != nil {
			t.Fatalf("结果必须是真实JPEG：%v", err)
		}
	}
	copy := result.DataURLs()
	copy[0] = "changed"
	if result.DataURLs()[0] == "changed" {
		t.Fatal("DataURLs返回了内部slice")
	}
}

func TestPrepareDoesNotClampImageCountToFourOrOverflow(t *testing.T) {
	picture := prepareFixtureJPEG(t)
	var calls atomic.Int32
	p := newTestPreparer(t, Dependencies{HTTP: prepareDoer(func(*http.Request) (*http.Response, error) { calls.Add(1); return successResponse(picture), nil })})
	urls := []string{"https://images.invalid/1", "https://images.invalid/2", "https://images.invalid/3", "https://images.invalid/4", "https://images.invalid/5", "https://images.invalid/6"}
	for _, limit := range []int64{1, 5, math.MaxInt64} {
		calls.Store(0)
		result, err := p.Prepare(context.Background(), urls, limit)
		assertIssueTranslations(t, result.Skipped)
		want := min(int64(len(urls)), limit)
		if err != nil || int64(len(result.Images)) != want || int64(calls.Load()) != want {
			t.Fatalf("图片数不能夹为4或转换溢出，limit=%d result=%d calls=%d err=%v", limit, len(result.Images), calls.Load(), err)
		}
	}
	for _, limit := range []int64{0, -1} {
		if _, err := p.Prepare(context.Background(), urls, limit); err == nil || err.Error() != "analysis.images.invalid_count" {
			t.Fatalf("非正张数应报安全错误：%v", err)
		}
	}
}

func TestPrepareSharesThreeDownloadSlotsAcrossCalls(t *testing.T) {
	picture := prepareFixtureJPEG(t)
	entered := make(chan struct{}, 30)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var active, maximum atomic.Int32
	p := newTestPreparer(t, Dependencies{HTTP: prepareDoer(func(r *http.Request) (*http.Response, error) {
		now := active.Add(1)
		for old := maximum.Load(); now > old && !maximum.CompareAndSwap(old, now); old = maximum.Load() {
		}
		entered <- struct{}{}
		defer active.Add(-1)
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return successResponse(picture), nil
	})})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 4)
	for range 4 {
		go func() {
			_, err := p.Prepare(ctx, []string{"https://images.invalid/1", "https://images.invalid/2", "https://images.invalid/3"}, 3)
			done <- err
		}()
	}
	for range 3 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("没有启动三张下载")
		}
	}
	select {
	case <-entered:
		t.Fatal("跨Prepare下载并发超过3")
	case <-time.After(20 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	for range 4 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if maximum.Load() != 3 || active.Load() != 0 || len(p.slots) != 0 {
		t.Fatalf("共享并发或收尾错误：max%d active%d slots%d", maximum.Load(), active.Load(), len(p.slots))
	}
}

func TestPrepareCancellationStopsDispatchAndJoinsWorkers(t *testing.T) {
	entered := make(chan struct{}, 3)
	cancelSeen := make(chan struct{}, 3)
	finishClose := make(chan struct{})
	var finishOnce sync.Once
	defer finishOnce.Do(func() { close(finishClose) })
	var calls atomic.Int32
	p := newTestPreparer(t, Dependencies{HTTP: prepareDoer(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-r.Context().Done()
		cancelSeen <- struct{}{}
		<-finishClose
		return nil, r.Context().Err()
	})})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type answer struct {
		result Result
		err    error
	}
	done := make(chan answer, 1)
	urls := make([]string, 12)
	for i := range urls {
		urls[i] = fmt.Sprintf("https://images.invalid/%d", i)
	}
	go func() { result, err := p.Prepare(ctx, urls, 12); done <- answer{result, err} }()
	for range 3 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("请求未启动")
		}
	}
	cancel()
	for range 3 {
		select {
		case <-cancelSeen:
		case <-time.After(time.Second):
			t.Fatal("在途请求未收到取消")
		}
	}
	select {
	case <-done:
		t.Fatal("尚未退出的worker被遗留")
	default:
	}
	finishOnce.Do(func() { close(finishClose) })
	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) || len(got.result.Images) != 0 || len(got.result.Skipped) != 0 {
			t.Fatalf("取消不能改成坏图或部分成功：%+v %v", got.result, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker结束后Prepare未退出")
	}
	if calls.Load() != 3 || len(p.slots) != 0 {
		t.Fatalf("取消后继续派发或泄漏名额：calls%d slots%d", calls.Load(), len(p.slots))
	}
}

type trackedBody struct {
	reader io.Reader
	read   int
	closed bool
	err    error
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += n
	return n, err
}
func (b *trackedBody) Close() error { b.closed = true; return b.err }

func TestPrepareBoundsDeclaredAndStreamingSourceBytes(t *testing.T) {
	for _, declared := range []int64{50, -1} {
		t.Run(fmt.Sprint(declared), func(t *testing.T) {
			body := &trackedBody{reader: bytes.NewReader(bytes.Repeat([]byte{'x'}, 50))}
			p := newTestPreparer(t, Dependencies{Limits: Limits{SourceBytes: 10}, HTTP: prepareDoer(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, ContentLength: declared, Body: body}, nil
			})})
			result, err := p.Prepare(context.Background(), []string{"https://images.invalid/1"}, 1)
			assertIssueTranslations(t, result.Skipped)
			if err != nil || len(result.Skipped) != 1 || result.Skipped[0].Reason.Code != "analysis.images.resource_limit" || result.Skipped[0].Reason.Args["field"] != "source_bytes" || !body.closed {
				t.Fatalf("限体或关闭响应错误：%+v %v closed%v", result, err, body.closed)
			}
			wantRead := 11
			if declared > 10 {
				wantRead = 0
			}
			if body.read != wantRead {
				t.Fatalf("越界请求读了过多正文：%d，应为%d", body.read, wantRead)
			}
		})
	}
}

func TestPrepareThirtySecondDeadlineAndSafeTimeout(t *testing.T) {
	p := newTestPreparer(t, Dependencies{HTTP: prepareDoer(func(r *http.Request) (*http.Response, error) {
		deadline, present := r.Context().Deadline()
		remaining := time.Until(deadline)
		if !present || remaining > RequestTimeout || remaining < RequestTimeout-time.Second {
			t.Errorf("请求期限必须为30秒：%v %v", remaining, present)
		}
		return nil, context.DeadlineExceeded
	})})
	result, err := p.Prepare(context.Background(), []string{"https://images.invalid/1"}, 1)
	assertIssueTranslations(t, result.Skipped)
	if err != nil || len(result.Skipped) != 1 || result.Skipped[0].Reason.Code != "analysis.images.timeout" {
		t.Fatalf("单图超时应安全跳过：%+v %v", result, err)
	}
}

func TestPrepareRejectsUnsafeTargetsWithoutRequests(t *testing.T) {
	var requests atomic.Int32
	p := newTestPreparer(t, Dependencies{HTTP: prepareDoer(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("proxy password=never-show")
	})})
	result, err := p.Prepare(context.Background(), []string{"file:///secret", "ftp://images.invalid/photo", "http://user:never-show@images.invalid/photo", ":broken", "https://images.invalid/photo"}, 5)
	assertIssueTranslations(t, result.Skipped)
	if err != nil || len(result.Skipped) != 5 || requests.Load() != 1 {
		t.Fatalf("非法目标被访问或单图失败未隔离：%+v %v requests%d", result, err, requests.Load())
	}
	data, _ := json.Marshal(result.Skipped)
	if strings.Contains(string(data), "never-show") || strings.Contains(string(data), "https://images.invalid") || strings.Contains(string(data), "proxy") {
		t.Fatalf("跳过原因泄漏URL或凭据：%s", data)
	}
}

func TestPrepareClearsCookieJarAndChecksEveryRedirect(t *testing.T) {
	picture := prepareFixtureJPEG(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Errorf("图片请求泄漏目标凭据：%v", r.Header)
		}
		if r.URL.Path == "/start" {
			http.SetCookie(w, &http.Cookie{Name: "new", Value: "secret"})
			http.Redirect(w, r, "/finish", http.StatusFound)
			return
		}
		_, _ = w.Write(picture)
	}))
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	target, _ := url.Parse(srv.URL)
	jar.SetCookies(target, []*http.Cookie{{Name: "old", Value: "secret"}})
	supplied := srv.Client()
	supplied.Jar = jar
	supplied.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		req.Header.Set("Cookie", "callback=secret")
		req.Header.Set("Authorization", "Basic secret")
		return nil
	}
	p := newTestPreparer(t, Dependencies{HTTP: supplied})
	result, err := p.Prepare(context.Background(), []string{srv.URL + "/start"}, 1)
	assertIssueTranslations(t, result.Skipped)
	if err != nil || len(result.Images) != 1 || calls.Load() != 2 {
		t.Fatalf("正常重定向应保持无Cookie：%+v %v calls%d", result, err, calls.Load())
	}
	if supplied.Jar != jar || len(jar.Cookies(target)) != 1 {
		t.Fatal("构造准备器修改了调用方Jar")
	}
}

func TestPrepareRejectsUserinfoInRedirectWithoutFollowing(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "http://user:never-show@"+r.Host+"/finish", http.StatusFound)
	}))
	defer srv.Close()
	result, err := newTestPreparer(t, Dependencies{HTTP: srv.Client()}).Prepare(context.Background(), []string{srv.URL + "/start"}, 1)
	assertIssueTranslations(t, result.Skipped)
	if err != nil || len(result.Skipped) != 1 || result.Skipped[0].Reason.Code != "analysis.images.invalid_url" || calls.Load() != 1 {
		t.Fatalf("不安全跳转被跟随：%+v %v calls%d", result, err, calls.Load())
	}
	if data, _ := json.Marshal(result.Skipped); strings.Contains(string(data), "never-show") {
		t.Fatalf("跳转错误泄漏凭据：%s", data)
	}
}

func TestPrepareBadProxyNeverFallsBackToOrigin(t *testing.T) {
	var originCalls, proxyCalls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		originCalls.Add(1)
		_, _ = w.Write(prepareFixtureJPEG(t))
	}))
	defer origin.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls.Add(1)
		if r.Header.Get("Proxy-Authorization") == "" {
			t.Error("代理认证未保留")
		}
		w.WriteHeader(http.StatusProxyAuthRequired)
		_, _ = io.WriteString(w, "proxy user:never-show")
	}))
	defer proxy.Close()
	address, _ := url.Parse(proxy.URL)
	address.User = url.UserPassword("user", "never-show")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(address)
	defer transport.CloseIdleConnections()
	result, err := newTestPreparer(t, Dependencies{HTTP: &http.Client{Transport: transport}}).Prepare(context.Background(), []string{origin.URL + "/photo"}, 1)
	assertIssueTranslations(t, result.Skipped)
	if err != nil || len(result.Skipped) != 1 || originCalls.Load() != 0 || proxyCalls.Load() != 1 {
		t.Fatalf("代理失败后发生直连：%+v %v origin%d proxy%d", result, err, originCalls.Load(), proxyCalls.Load())
	}
	if data, _ := json.Marshal(result.Skipped); strings.Contains(string(data), "never-show") {
		t.Fatalf("代理错误泄漏凭据：%s", data)
	}
}

func TestPrepareDoesNotInheritEnvironmentProxy(t *testing.T) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	supplied := &http.Client{Transport: transport}
	p := newTestPreparer(t, Dependencies{HTTP: supplied})
	clone := p.http.(*http.Client)
	if clone == supplied || clone.Transport.(*http.Transport).Proxy != nil || reflect.ValueOf(transport.Proxy).Pointer() != reflect.ValueOf(http.ProxyFromEnvironment).Pointer() {
		t.Fatal("未隔离环境代理或改坏调用方配置")
	}
}

type timeoutBody struct{ closed bool }

func (*timeoutBody) Read([]byte) (int, error) { return 0, context.DeadlineExceeded }
func (b *timeoutBody) Close() error           { b.closed = true; return nil }

func TestPrepareBodyReadUsesSameDeadlineAndCloses(t *testing.T) {
	body := &timeoutBody{}
	p := newTestPreparer(t, Dependencies{HTTP: prepareDoer(func(r *http.Request) (*http.Response, error) {
		deadline, present := r.Context().Deadline()
		if !present || time.Until(deadline) > RequestTimeout {
			t.Error("GET和正文必须共用30秒期限")
		}
		return &http.Response{StatusCode: 200, ContentLength: -1, Body: body}, nil
	})})
	result, err := p.Prepare(context.Background(), []string{"https://images.invalid/photo"}, 1)
	assertIssueTranslations(t, result.Skipped)
	if err != nil || len(result.Skipped) != 1 || result.Skipped[0].Reason.Code != "analysis.images.timeout" || !body.closed {
		t.Fatalf("正文超时应安全跳过并关闭：%+v %v closed%v", result, err, body.closed)
	}
}

func TestPrepareConversionFailuresAndEmptyDetailsStayImageFree(t *testing.T) {
	picture := prepareFixtureJPEG(t)
	for _, tc := range []struct {
		name   string
		source []byte
		limits Limits
		code   string
	}{
		{"invalid", []byte("not an image; secret"), Limits{}, "analysis.images.invalid_image"},
		{"pixels", picture, Limits{SourcePixels: 1}, "analysis.images.resource_limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			p := newTestPreparer(t, Dependencies{HTTP: prepareDoer(func(*http.Request) (*http.Response, error) { calls.Add(1); return successResponse(tc.source), nil }), Limits: tc.limits})
			result, err := p.Prepare(context.Background(), []string{"https://images.invalid/photo"}, 1)
			assertIssueTranslations(t, result.Skipped)
			if err != nil || len(result.Images) != 0 || len(result.Skipped) != 1 || result.Skipped[0].Reason.Code != tc.code {
				t.Fatalf("转换失败只能跳过本图：%+v %v", result, err)
			}
			if data, _ := json.Marshal(result.Skipped); strings.Contains(string(data), "secret") {
				t.Fatalf("转换错误泄漏原文：%s", data)
			}
			result, err = p.Prepare(context.Background(), nil, 5)
			assertIssueTranslations(t, result.Skipped)
			if err != nil || len(result.Images) != 0 || len(result.Skipped) != 0 || len(result.DataURLs()) != 0 || calls.Load() != 1 {
				t.Fatalf("空详情不能制造主图兜底：%+v %v calls%d", result, err, calls.Load())
			}
		})
	}
}

func TestPrepareConfigurationAndCountErrorsHaveBothTranslations(t *testing.T) {
	_, configuration := New(Dependencies{})
	p := newTestPreparer(t, Dependencies{HTTP: prepareDoer(func(*http.Request) (*http.Response, error) { t.Error("非法张数不应请求"); return nil, nil })})
	_, count := p.Prepare(context.Background(), nil, 0)
	for _, err := range []error{configuration, count} {
		var structured interface{ Message() locales.Message }
		if !errors.As(err, &structured) {
			t.Fatalf("安全错误必须带可翻译Message：%v", err)
		}
		message := structured.Message()
		if err.Error() != message.Code || len(message.Args) != 0 {
			t.Fatalf("错误只能含静态代码：%v %+v", err, message)
		}
		for _, language := range []locales.Language{locales.ZhCN, locales.En} {
			if text, formatErr := locales.Format(language, message); formatErr != nil || text == "" {
				t.Fatalf("%s缺%s文案：%v", language, message.Code, formatErr)
			}
		}
	}
}

func assertIssueTranslations(t *testing.T, issues []Issue) {
	t.Helper()
	for _, issue := range issues {
		if issue.Reason.Args == nil {
			t.Fatalf("实际Issue参数必须为对象：%+v", issue)
		}
		for _, language := range []locales.Language{locales.ZhCN, locales.En} {
			if text, err := locales.Format(language, issue.Reason); err != nil || text == "" {
				t.Fatalf("实际Issue缺%s文案或参数不合法：%+v %v", language, issue, err)
			}
		}
	}
}
