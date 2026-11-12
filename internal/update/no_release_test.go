package update

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestUpdateNoReleaseIsSuccessfulEmptyResult(t *testing.T) {
	for _, version := range []string{"1.2.3", "dev"} {
		t.Run(version, func(t *testing.T) {
			f := newUpdateFixture(t, func(_ int64, w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("ETag", `"not-a-release"`)
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			})
			f.checker = f.newChecker(t, version)
			if err := f.checker.CheckDue(t.Context()); err != nil {
				t.Fatalf("没有发布记录被当作失败：%v", err)
			}
			status := f.read(t)
			if status.Stale || status.Error != nil || status.KnownRelease != nil || status.UpdateAvailable == nil || *status.UpdateAvailable || status.LastSuccessAt == nil || *status.LastSuccessAt != f.now.Load() || status.LastAttemptAt == nil || *status.LastAttemptAt != f.now.Load() {
				t.Fatalf("无发布结果没有被记录为成功：%+v", status)
			}
			f.checker = f.newChecker(t, version)
			if err := f.checker.CheckDue(t.Context()); err != nil || f.calls.Load() != 1 {
				t.Fatalf("重启后没有沿用每日期限：请求数%d，错误%v", f.calls.Load(), err)
			}
			if status := f.read(t); status.Stale || status.UpdateAvailable == nil || *status.UpdateAvailable {
				t.Fatalf("重启后丢失无发布结果：%+v", status)
			}
		})
	}
}

func TestUpdateNoReleaseClearsPreviousFailureAndConditionalCache(t *testing.T) {
	f := newUpdateFixture(t, func(n int64, w http.ResponseWriter, r *http.Request) {
		switch n {
		case 1:
			w.Header().Set("ETag", `"old-release"`)
			w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
			writeRelease(w, "v1.2.4")
		case 2:
			if r.Header.Get("If-None-Match") != `"old-release"` {
				t.Error("网络失败丢掉了上次成功的条件缓存")
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
				t.Error("无发布结果仍带着旧发布的条件缓存")
			}
			writeRelease(w, "v1.2.5")
		}
	})
	if err := f.checker.CheckDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	firstSuccess := *f.read(t).LastSuccessAt
	f.now.Add(24 * time.Hour.Milliseconds())
	workingTransport := f.client.Transport
	f.client.Transport = failedTransport{}
	f.checker = f.newChecker(t, "1.2.3")
	if err := f.checker.CheckDue(t.Context()); err == nil {
		t.Fatal("网络超时被当作成功")
	}
	if status := f.read(t); status.Error == nil || !status.Stale || status.KnownRelease == nil || status.LastSuccessAt == nil || *status.LastSuccessAt != firstSuccess || status.UpdateAvailable != nil {
		t.Fatalf("网络失败没有保留成功历史和失败状态：%+v", status)
	}
	f.now.Add(24 * time.Hour.Milliseconds())
	f.client.Transport = workingTransport
	f.checker = f.newChecker(t, "1.2.3")
	if err := f.checker.CheckDue(t.Context()); err != nil {
		t.Fatalf("无发布结果不能清除旧失败：%v", err)
	}
	status := f.read(t)
	if status.Error != nil || status.Stale || status.KnownRelease != nil || status.UpdateAvailable == nil || *status.UpdateAvailable || status.LastSuccessAt == nil || *status.LastSuccessAt != f.now.Load() {
		t.Fatalf("旧失败或旧发布没有清除：%+v", status)
	}
	var state cacheState
	if err := json.Unmarshal([]byte(f.raw(t)), &state); err != nil {
		t.Fatal(err)
	}
	if state.ETag != "" || state.LastModified != "" {
		t.Fatalf("404没有清除条件缓存：%+v", state)
	}
	f.now.Add(24 * time.Hour.Milliseconds())
	if err := f.checker.CheckDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	if status := f.read(t); status.Error != nil || status.Stale || status.KnownRelease == nil || status.KnownRelease.Version != "v1.2.5" || status.UpdateAvailable == nil || !*status.UpdateAvailable || f.calls.Load() != 3 {
		t.Fatalf("下一次发布未恢复正常检查：%+v，请求数%d", status, f.calls.Load())
	}
}
