package fake

import (
	"io"
	"net/http"
	"net/url"
	"sync"
)

type SearchFault struct {
	First         bool
	Final         bool
	Step          string
	TruncatedJSON bool
	Hold          bool
}

type SearchFaultGate struct {
	Entered <-chan struct{}
	Done    <-chan struct{}
	entered chan struct{}
	done    chan struct{}
	release chan struct{}
	once    sync.Once
	finish  sync.Once
	opened  sync.Once
}

type searchFaultState struct {
	options SearchFault
	gate    *SearchFaultGate
}

func (f *Xianyu) SetSearchFault(keyword string, options SearchFault) *SearchFaultGate {
	entered, done := make(chan struct{}), make(chan struct{})
	gate := &SearchFaultGate{Entered: entered, Done: done, entered: entered, done: done, release: make(chan struct{})}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.searchFaults == nil {
		f.searchFaults = map[string]searchFaultState{}
	}
	f.searchFaults[keyword] = searchFaultState{options: options, gate: gate}
	f.faultGates = append(f.faultGates, gate)
	return gate
}

// 移除配置不释放已挂起的请求，便于同时验证其它账号。
func (f *Xianyu) ClearSearchFault(keyword string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.searchFaults, keyword)
}

func (g *SearchFaultGate) Release() { g.opened.Do(func() { close(g.release) }) }

func (f *Xianyu) releaseSearchFaults() {
	f.mu.Lock()
	gates := append([]*SearchFaultGate(nil), f.faultGates...)
	f.mu.Unlock()
	for _, gate := range gates {
		gate.Release()
	}
}

func (f *Xianyu) RequestCount() int64 { return f.requests.Load() }

func (f *Xianyu) serveSearchFault(w http.ResponseWriter, r *http.Request, body []byte) bool {
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return false
	}
	f.mu.Lock()
	state, exists := f.searchFaults[values.Get("q")]
	f.mu.Unlock()
	if !exists {
		return false
	}
	opts, query := state.options, r.URL.Query()
	if opts.First && query.Get("first") != "1" || opts.Final && query.Get("final") != "1" || opts.Step != "" && query.Get("step") != opts.Step {
		return false
	}
	state.gate.once.Do(func() { close(state.gate.entered) })
	defer state.gate.finish.Do(func() { close(state.gate.done) })
	if opts.Hold {
		select {
		case <-state.gate.release:
		case <-r.Context().Done():
			return true
		}
	}
	if opts.TruncatedJSON {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ret":["SUCCESS::调用成功"],"data":{"resultList":[`)
		return true
	}
	return false
}
