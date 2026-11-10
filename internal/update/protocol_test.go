package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/service"
)

func TestUpdateLastModifiedFallbackAndRefresh(t *testing.T) {
	modified := "Mon, 02 Jan 2006 15:04:05 GMT"
	f := newUpdateFixture(t, func(n int64, w http.ResponseWriter, r *http.Request) {
		switch n {
		case 1:
			w.Header().Set("ETag", "invalid")
			w.Header().Set("Last-Modified", modified)
			writeRelease(w, "v1.2.4")
		case 2:
			if r.Header.Get("If-Modified-Since") != modified || r.Header.Get("If-None-Match") != "" {
				t.Errorf("没有回退到修改时间：%+v", r.Header)
			}
			w.Header().Set("ETag", `"fresh"`)
			w.WriteHeader(http.StatusNotModified)
		case 3:
			if r.Header.Get("If-None-Match") != `"fresh"` || r.Header.Get("If-Modified-Since") != "" {
				t.Errorf("etag没有优先或304新标记没保存：%+v", r.Header)
			}
			writeRelease(w, "v1.2.5")
		default:
			if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
				t.Error("新200没有标记时仍发送旧标记")
			}
			writeRelease(w, "v1.2.5")
		}
	})
	for range 4 {
		if err := f.checker.CheckDue(t.Context()); err != nil {
			t.Fatal(err)
		}
		f.now.Add(24 * time.Hour.Milliseconds())
	}
}

func TestUpdateRejects304WithoutConditionalCache(t *testing.T) {
	for _, hasPayload := range []bool{false, true} {
		t.Run(fmt.Sprint(hasPayload), func(t *testing.T) {
			f := newUpdateFixture(t, func(n int64, w http.ResponseWriter, _ *http.Request) {
				if hasPayload && n == 1 {
					writeRelease(w, "v1.2.4")
				} else {
					w.WriteHeader(http.StatusNotModified)
				}
			})
			if hasPayload {
				if err := f.checker.CheckDue(t.Context()); err != nil {
					t.Fatal(err)
				}
				f.now.Add(24 * time.Hour.Milliseconds())
			}
			if err := f.checker.CheckDue(t.Context()); err == nil {
				t.Fatal("没有发条件请求，仍接受304")
			}
			if status := f.read(t); !status.Stale || status.Error == nil || status.UpdateAvailable != nil {
				t.Fatalf("304被当成有效更新：%+v", status)
			}
		})
	}
}

func TestUpdateRedirectsStayOnOfficialHTTPS(t *testing.T) {
	for _, target := range []string{"http://api.github.com/fake", "https://evil.invalid/fake", "https://api.github.com.evil.invalid/fake", "https://api.github.com:443/fake", "https://private@api.github.com/fake"} {
		t.Run(target, func(t *testing.T) {
			f := newUpdateFixture(t, func(_ int64, w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", target)
				w.WriteHeader(http.StatusFound)
			})
			if err := f.checker.CheckDue(t.Context()); err == nil || f.calls.Load() != 1 {
				t.Fatalf("不安全重定向没有在外发前拦住：次数%d %v", f.calls.Load(), err)
			}
		})
	}
	for _, succeeds := range []bool{false, true} {
		t.Run(fmt.Sprint(succeeds), func(t *testing.T) {
			f := newUpdateFixture(t, func(n int64, w http.ResponseWriter, _ *http.Request) {
				if succeeds && n == 4 {
					writeRelease(w, "v1.2.4")
					return
				}
				w.Header().Set("Location", "https://api.github.com/fake/redirect")
				w.WriteHeader(http.StatusTemporaryRedirect)
			})
			err := f.checker.CheckDue(t.Context())
			if (err == nil) != succeeds || f.calls.Load() != 4 {
				t.Fatalf("三跳边界错误：次数%d %v", f.calls.Load(), err)
			}
		})
	}
}

func TestUpdateClientIsIndependentAndSendsNoJarCookies(t *testing.T) {
	f := newUpdateFixture(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("公共检查携带了凭据")
		}
		writeRelease(w, "v1.2.4")
	})
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	address, _ := url.Parse(latestEndpoint)
	jar.SetCookies(address, []*http.Cookie{{Name: "private_marker", Value: "secret"}})
	f.client.Jar, f.client.Timeout = jar, time.Minute
	f.checker = f.newChecker(t, "1.2.3")
	if f.checker.client.Timeout != 10*time.Second || f.checker.client.Jar != nil || f.client.Timeout != time.Minute || f.client.Jar != jar {
		t.Fatal("没有固定全程超时或改了调用方Client")
	}
	if err := f.checker.CheckDue(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type failedTransport struct{}

func (failedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("private proxy address: %w", context.DeadlineExceeded)
}

func TestUpdateTimeoutErrorIsCachedWithoutPrivateDetails(t *testing.T) {
	f := newUpdateFixture(t, nil)
	f.client.Transport = failedTransport{}
	f.checker = f.newChecker(t, "1.2.3")
	var business *service.ServiceError
	err := f.checker.CheckDue(t.Context())
	if !errors.As(err, &business) || business.Reason.Code != "update.check_failed" || len(business.Reason.Args) != 0 {
		t.Fatalf("内部网络超时未转成可翻译错误：%v", err)
	}
	status := f.read(t)
	if status.Error == nil || !status.Stale || status.UpdateAvailable != nil || strings.Contains(f.raw(t), "private proxy address") || strings.Contains(err.Error(), "private proxy address") || f.calls.Load() != 0 {
		t.Fatalf("网络失败丢失状态或暴露底层内容：%+v %v", status, err)
	}
}

func TestUpdateCancellationWhileReadingBody(t *testing.T) {
	started := make(chan struct{})
	f := newUpdateFixture(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":`))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.checker.CheckDue(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("没有开始读取正文")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("读正文期间不响应取消：%v", err)
	}
	if status := f.read(t); status.LastAttemptAt == nil || status.Error != nil || status.LastSuccessAt != nil {
		t.Fatalf("正文取消被记成成功或正常停机错误：%+v", status)
	}
}

func TestUpdateCacheCorruptionFailsClosed(t *testing.T) {
	for _, change := range []func(map[string]json.RawMessage){
		func(raw map[string]json.RawMessage) { raw["version"] = json.RawMessage(`2`) },
		func(raw map[string]json.RawMessage) { raw["extra"] = json.RawMessage(`1`) },
		func(raw map[string]json.RawMessage) { raw["last_attempt_at"] = json.RawMessage(`0`) },
		func(raw map[string]json.RawMessage) { raw["last_attempt_at"] = json.RawMessage(`null`) },
		func(raw map[string]json.RawMessage) { raw["known_release"] = json.RawMessage(`null`) },
		func(raw map[string]json.RawMessage) { raw["etag"] = json.RawMessage(`"invalid"`) },
		func(raw map[string]json.RawMessage) { raw["last_modified"] = json.RawMessage(`"invalid"`) },
		func(raw map[string]json.RawMessage) {
			raw["error"] = json.RawMessage(`{"code":"update.check_failed","args":null}`)
		},
		func(raw map[string]json.RawMessage) {
			raw["known_release"] = json.RawMessage(`{"version":"v1.2.4","url":"https://evil.invalid/release"}`)
		},
	} {
		f := newUpdateFixture(t, nil)
		if err := f.checker.CheckDue(t.Context()); err != nil {
			t.Fatal(err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(f.raw(t)), &raw); err != nil {
			t.Fatal(err)
		}
		change(raw)
		encoded, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		f.exec(t, `UPDATE meta SET value=? WHERE key='update.check.v1'`, string(encoded))
		for _, read := range []func() error{
			func() error { _, err := f.checker.Read(t.Context()); return err },
			func() error { return f.checker.CheckDue(t.Context()) },
		} {
			var business *service.ServiceError
			if err := read(); !errors.As(err, &business) || business.Reason.Code != "service.storage_failure" || len(business.Reason.Args) != 0 {
				t.Fatalf("坏缓存没有关闭读取/请求：%v", err)
			}
		}
		if f.calls.Load() != 1 {
			t.Fatal("坏缓存仍访问了远端")
		}
	}
}

func TestUpdateClaimFailureDoesNotFetch(t *testing.T) {
	f := newUpdateFixture(t, nil)
	f.exec(t, `CREATE TRIGGER reject_check_claim BEFORE INSERT ON meta WHEN NEW.key='update.check.v1' BEGIN SELECT RAISE(ABORT,'private write failure'); END`)
	var business *service.ServiceError
	if err := f.checker.CheckDue(t.Context()); !errors.As(err, &business) || business.Reason.Code != "service.storage_failure" || f.calls.Load() != 0 || f.raw(t) != "" {
		t.Fatalf("尝试未落库仍然外发：%v", err)
	}
}

func TestUpdateCompletionFailureKeepsAttempt(t *testing.T) {
	var f *updateFixture
	f = newUpdateFixture(t, func(_ int64, w http.ResponseWriter, _ *http.Request) {
		if _, err := f.db.Exec(`CREATE TRIGGER reject_check_finish BEFORE UPDATE ON meta WHEN NEW.key='update.check.v1' BEGIN SELECT RAISE(ABORT,'private write failure'); END`); err != nil {
			t.Error(err)
		}
		writeRelease(w, "v1.2.4")
	})
	var business *service.ServiceError
	if err := f.checker.CheckDue(t.Context()); !errors.As(err, &business) || business.Reason.Code != "service.storage_failure" {
		t.Fatalf("结果落库失败仍声称成功：%v", err)
	}
	status := f.read(t)
	if status.LastAttemptAt == nil || status.LastSuccessAt != nil || status.KnownRelease != nil || status.UpdateAvailable != nil || !status.Stale {
		t.Fatalf("失败后丢掉尝试或报告成功：%+v", status)
	}
	if err := f.checker.CheckDue(t.Context()); err != nil || f.calls.Load() != 1 {
		t.Fatalf("落库失败后绕过24小时间隔：%v", err)
	}
}
