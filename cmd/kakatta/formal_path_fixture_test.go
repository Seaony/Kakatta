package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/xianyu/fake"
)

const formalFixtureKey = "local-formal-key"

type formalFixtureMetadata struct {
	Site struct {
		Home    string `json:"home"`
		APIHost string `json:"api_host"`
	} `json:"site"`
	ModelBaseURL string `json:"model_base_url"`
	PushURL      string `json:"push_url"`
	ModelKey     string `json:"model_key"`
	ControlURL   string `json:"control_url"`
}

type formalModelReceipt struct {
	Purpose   string `json:"purpose"`
	Model     string `json:"model"`
	Status    int    `json:"http_status"`
	StartedAt int64  `json:"started_at"`
	EndedAt   int64  `json:"ended_at"`
}

type formalFixtures struct {
	ctx                     context.Context
	cancel                  context.CancelFunc
	xianyu                  *fake.Xianyu
	model, push             *httptest.Server
	metadata                formalFixtureMetadata
	mu                      sync.Mutex
	modelTimeout, pushFails bool
	modelRequests           []formalModelReceipt
	products                []map[string]any
	pushes                  []map[string]any
	loginCompletions        int64
}

func newFormalFixtures(parent context.Context) *formalFixtures {
	ctx, cancel := context.WithCancel(parent)
	f := &formalFixtures{ctx: ctx, cancel: cancel, xianyu: fake.Start(), modelRequests: []formalModelReceipt{}, products: []map[string]any{}, pushes: []map[string]any{}}
	f.xianyu.EnableAutomaticLogin()
	f.xianyu.EnableSellerProfile()
	page1, page2 := coreComparisonPages()
	f.xianyu.SetSearchItems(page1, page2)
	f.model = httptest.NewServer(http.HandlerFunc(f.serveModel))
	f.push = httptest.NewServer(http.HandlerFunc(f.servePush))
	f.metadata.Site.Home, f.metadata.Site.APIHost = f.xianyu.Home(), f.xianyu.APIHost()
	f.metadata.ModelBaseURL, f.metadata.PushURL = f.model.URL+"/v1", f.push.URL
	f.metadata.ModelKey, f.metadata.ControlURL = formalFixtureKey, "http://127.0.0.1:19090"
	return f
}

func (f *formalFixtures) close() {
	f.cancel()
	f.xianyu.Close()
	f.model.Close()
	f.push.Close()
}

func (f *formalFixtures) state() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	counts := map[string]int64{"title_screen": 0, "analysis": 0, "profile": 0, "probe": 0, "market": 0}
	for _, request := range f.modelRequests {
		counts[request.Purpose]++
	}
	return map[string]any{
		"model_counts": counts, "model_requests": append([]formalModelReceipt{}, f.modelRequests...),
		"products": append([]map[string]any{}, f.products...), "pushes": append([]map[string]any{}, f.pushes...),
		"search_queries": f.xianyu.SearchQueries(), "search_count": len(f.xianyu.SearchQueries()),
		"detail_count": f.xianyu.DetailHits(), "seller": f.xianyu.SellerHits(),
		"login_completions": f.loginCompletions,
	}
}

func (f *formalFixtures) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		formalFixtureJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /metadata", func(w http.ResponseWriter, r *http.Request) { formalFixtureJSON(w, http.StatusOK, f.metadata) })
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) { formalFixtureJSON(w, http.StatusOK, f.state()) })
	mux.HandleFunc("POST /control", f.control)
	return mux
}

func (f *formalFixtures) control(w http.ResponseWriter, r *http.Request) {
	var command struct {
		Action string              `json:"action"`
		Mode   string              `json:"mode"`
		Pages  [][]fake.SearchItem `json:"pages"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&command) != nil {
		http.Error(w, "夹具控制参数无效", http.StatusBadRequest)
		return
	}
	switch command.Action {
	case "login_complete":
		f.xianyu.CompleteAutomaticLogin()
		f.mu.Lock()
		f.loginCompletions++
		f.mu.Unlock()
	case "search":
		if len(command.Pages) > 2 {
			http.Error(w, "假搜索只支持两页", http.StatusBadRequest)
			return
		}
		var first, second []fake.SearchItem
		if len(command.Pages) > 0 {
			first = command.Pages[0]
		}
		if len(command.Pages) > 1 {
			second = command.Pages[1]
		}
		f.xianyu.SetSearchItems(first, second)
	case "model":
		if command.Mode != "timeout" && command.Mode != "ok" {
			http.Error(w, "模型模式无效", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.modelTimeout = command.Mode == "timeout"
		f.mu.Unlock()
	case "push":
		if command.Mode != "fail" && command.Mode != "ok" {
			http.Error(w, "推送模式无效", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.pushFails = command.Mode == "fail"
		f.mu.Unlock()
	case "risk":
		if command.Mode != "expired" && command.Mode != "verify" && command.Mode != "none" {
			http.Error(w, "风险模式无效", http.StatusBadRequest)
			return
		}
		f.xianyu.SetMaskOnSearch(command.Mode == "verify")
		behavior := fake.FilterBehavior{}
		if command.Mode == "expired" {
			behavior.Ret = "FAIL_SYS_SESSION_EXPIRED::登录失效"
		}
		f.xianyu.SetFilterBehavior("personal", behavior)
	default:
		http.Error(w, "未知夹具动作", http.StatusBadRequest)
		return
	}
	formalFixtureJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (f *formalFixtures) servePush(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if r.Method != http.MethodPost || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body) != nil {
		http.Error(w, "本地推送请求无效", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	status := http.StatusNoContent
	if f.pushFails {
		status = http.StatusServiceUnavailable
	}
	body["http_status"], body["at"] = status, time.Now().UnixMilli()
	f.pushes = append(f.pushes, body)
	f.mu.Unlock()
	if status != http.StatusNoContent {
		http.Error(w, "本地推送渠道暂不可用", status)
		return
	}
	w.WriteHeader(status)
}

func formalFixtureJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func TestFormalPathFixtures(t *testing.T) {
	requireFormalPath(t)
	ctx, stop := signal.NotifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	f := newFormalFixtures(ctx)
	defer f.close()
	data, err := json.Marshal(f.metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("KAKATTA_FORMAL_DIR"), "fixture.json"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:19090")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: f.handler(), ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(cleanup); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatal(err)
		}
	}
}
