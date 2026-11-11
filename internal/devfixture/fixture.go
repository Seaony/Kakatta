// Package devfixture 提供开发和断网测试共用的本地站点、模型与推送夹具。
package devfixture

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/Seaony/Kakatta/internal/xianyu/fake"
)

const modelKey = "local-formal-key"

type Metadata struct {
	Site struct {
		Home    string `json:"home"`
		APIHost string `json:"api_host"`
	} `json:"site"`
	ModelBaseURL string `json:"model_base_url"`
	PushURL      string `json:"push_url"`
	ModelKey     string `json:"model_key"`
	ControlURL   string `json:"control_url"`
}

type ModelReceipt struct {
	Purpose   string `json:"purpose"`
	Model     string `json:"model"`
	Status    int    `json:"http_status"`
	StartedAt int64  `json:"started_at"`
	EndedAt   int64  `json:"ended_at"`
}

type Fixture struct {
	ctx                     context.Context
	cancel                  context.CancelFunc
	xianyu                  *fake.Xianyu
	model, push             *httptest.Server
	metadata                Metadata
	mu                      sync.Mutex
	closeOnce               sync.Once
	autoLoginDone           chan struct{}
	modelTimeout, pushFails bool
	modelRequests           []ModelReceipt
	products                []map[string]any
	pushes                  []map[string]any
	loginCompletions        int64
	faultMode               string
}

func New(parent context.Context, controlURL string) *Fixture {
	ctx, cancel := context.WithCancel(parent)
	f := &Fixture{ctx: ctx, cancel: cancel, xianyu: fake.Start(), faultMode: "normal", modelRequests: []ModelReceipt{}, products: []map[string]any{}, pushes: []map[string]any{}}
	f.xianyu.EnableAutomaticLogin()
	f.xianyu.EnableSellerProfile()
	page1, page2 := ComparisonPages()
	f.xianyu.SetSearchItems(page1, page2)
	f.model = httptest.NewServer(http.HandlerFunc(f.serveModel))
	f.push = httptest.NewServer(http.HandlerFunc(f.servePush))
	f.metadata.Site.Home, f.metadata.Site.APIHost = f.xianyu.Home(), f.xianyu.APIHost()
	f.metadata.ModelBaseURL, f.metadata.PushURL = f.model.URL+"/v1", f.push.URL
	f.metadata.ModelKey, f.metadata.ControlURL = modelKey, controlURL
	return f
}

func (f *Fixture) Metadata() Metadata { return f.metadata }

func (f *Fixture) Close() {
	f.closeOnce.Do(func() {
		f.cancel()
		f.mu.Lock()
		done := f.autoLoginDone
		f.mu.Unlock()
		if done != nil {
			<-done
		}
		f.xianyu.Close()
		f.model.Close()
		f.push.Close()
	})
}

// EnableDevelopmentAutoLogin 只替本地开发完成已打开的假扫码，正式路径测试不启用。
func (f *Fixture) EnableDevelopmentAutoLogin() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.autoLoginDone != nil || f.ctx.Err() != nil {
		return
	}
	done := make(chan struct{})
	f.autoLoginDone = done
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-f.ctx.Done():
				return
			case <-ticker.C:
				f.xianyu.CompleteAutomaticLogin()
			}
		}
	}()
}

func (f *Fixture) State() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	counts := map[string]int64{"title_screen": 0, "analysis": 0, "profile": 0, "probe": 0, "market": 0}
	for _, request := range f.modelRequests {
		counts[request.Purpose]++
	}
	return map[string]any{
		"model_counts": counts, "model_requests": append([]ModelReceipt{}, f.modelRequests...),
		"products": append([]map[string]any{}, f.products...), "pushes": append([]map[string]any{}, f.pushes...),
		"search_queries": f.xianyu.SearchQueries(), "search_count": len(f.xianyu.SearchQueries()),
		"detail_count": f.xianyu.DetailHits(), "seller": f.xianyu.SellerHits(),
		"login_completions": f.loginCompletions,
		"fault_mode":        f.faultMode,
	}
}

func (f *Fixture) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /metadata", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, f.metadata) })
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, f.State()) })
	mux.HandleFunc("POST /control", f.control)
	return mux
}

func (f *Fixture) control(w http.ResponseWriter, r *http.Request) {
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
			f.xianyu.ExpireAutomaticLogin()
			behavior.Ret = "FAIL_SYS_SESSION_EXPIRED::登录失效"
		}
		f.xianyu.SetFilterBehavior("personal", behavior)
	case "fault":
		var behavior fake.SearchResponseBehavior
		switch command.Mode {
		case "normal", "risk":
		case "expired":
			behavior.Ret = "FAIL_SYS_SESSION_EXPIRED::登录失效"
		case "missing":
			behavior.MissingFields = true
		case "api_error":
			behavior.Status = http.StatusServiceUnavailable
		default:
			http.Error(w, "站点故障模式无效", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.xianyu.SetMaskOnSearch(command.Mode == "risk")
		f.xianyu.SetFilterBehavior("personal", fake.FilterBehavior{})
		f.xianyu.SetSearchResponseBehavior(behavior)
		f.faultMode = command.Mode
		f.mu.Unlock()
	default:
		http.Error(w, "未知夹具动作", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (f *Fixture) servePush(w http.ResponseWriter, r *http.Request) {
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

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
