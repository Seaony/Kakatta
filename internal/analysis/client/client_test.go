package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type contractChange struct {
	ref    ProviderRef
	change ProviderChange
}

type contractRepo struct {
	mu                                                     sync.Mutex
	providers                                              []Provider
	compat                                                 map[string]Compat
	changes                                                []contractChange
	saves                                                  int
	providersErr, providerErr, loadErr, saveErr, changeErr error
}

func (r *contractRepo) Providers(ctx context.Context) ([]Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return slices.Clone(r.providers), r.providersErr
}

func (r *contractRepo) Provider(ctx context.Context, id int64) (Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Provider{}, err
	}
	if r.providerErr != nil {
		return Provider{}, r.providerErr
	}
	for _, p := range r.providers {
		if p.Ref.ID == id {
			return p, nil
		}
	}
	return Provider{}, errors.New("provider missing")
}

func contractCompatKey(k CompatKey) string {
	return strconv.FormatInt(k.Provider.ID, 10) + "/" + k.Provider.ConfigToken + "/" + k.BaseURL + "/" + k.Model
}

func contractCloneCompat(c Compat) Compat {
	if c.MaxTokensFloor != nil {
		v := *c.MaxTokensFloor
		c.MaxTokensFloor = &v
	}
	return c
}

func (r *contractRepo) LoadCompat(ctx context.Context, key CompatKey) (Compat, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Compat{}, err
	}
	if r.loadErr != nil {
		return Compat{}, r.loadErr
	}
	if c, ok := r.compat[contractCompatKey(key)]; ok {
		return contractCloneCompat(c), nil
	}
	return DefaultCompat(), nil
}

func (r *contractRepo) SaveCompat(ctx context.Context, key CompatKey, next Compat) (Compat, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Compat{}, err
	}
	if r.saveErr != nil {
		return Compat{}, r.saveErr
	}
	valid := false
	for _, p := range r.providers {
		if p.Ref == key.Provider {
			valid = true
		}
	}
	if !valid {
		return Compat{}, ErrConfigChanged
	}
	if r.compat == nil {
		r.compat = map[string]Compat{}
	}
	old, ok := r.compat[contractCompatKey(key)]
	if !ok {
		old = DefaultCompat()
	}
	rank := map[JSONMode]int{JSONSchema: 0, JSONObject: 1, JSONText: 2}
	if rank[next.JSONMode] > rank[old.JSONMode] {
		old.JSONMode = next.JSONMode
	}
	old.SendTemperature = old.SendTemperature && next.SendTemperature
	if next.MaxTokensParam == "max_completion_tokens" {
		old.MaxTokensParam = next.MaxTokensParam
	}
	if next.MaxTokensFloor != nil && (old.MaxTokensFloor == nil || *next.MaxTokensFloor > *old.MaxTokensFloor) {
		v := *next.MaxTokensFloor
		old.MaxTokensFloor = &v
	}
	r.compat[contractCompatKey(key)] = contractCloneCompat(old)
	r.saves++
	return contractCloneCompat(old), nil
}

func (r *contractRepo) ChangeProvider(ctx context.Context, ref ProviderRef, change ProviderChange) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.changeErr != nil {
		return r.changeErr
	}
	for i := range r.providers {
		if r.providers[i].Ref.ID != ref.ID {
			continue
		}
		if r.providers[i].Ref != ref {
			return ErrConfigChanged
		}
		if change.Kind == "blocked" {
			r.providers[i].ErrorState = change.Reason
		}
		if change.Kind == "test_success" || change.Kind == "probe_success" {
			r.providers[i].ErrorState = nil
		}
		r.changes = append(r.changes, contractChange{ref, change})
		return nil
	}
	return ErrConfigChanged
}

func (r *contractRepo) changeKinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var kinds []string
	for _, c := range r.changes {
		kinds = append(kinds, c.change.Kind)
	}
	return kinds
}

type contractAccounting struct {
	mu                  sync.Mutex
	starts              []CallStart
	ends                []CallEnd
	ended               map[int64]bool
	total, limit        int64
	beforeCount         int
	beforeErr, afterErr error
	afterCanceled       bool
}

func (a *contractAccounting) Before(ctx context.Context, start CallStart) (Ticket, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.beforeCount++
	if err := ctx.Err(); err != nil {
		return Ticket{}, err
	}
	if a.beforeErr != nil {
		return Ticket{}, a.beforeErr
	}
	if a.limit > 0 && a.total >= a.limit && start.Purpose != "test" {
		return Ticket{}, ErrDailyLimit
	}
	a.starts = append(a.starts, start)
	return Ticket{ID: int64(len(a.starts))}, nil
}

func (a *contractAccounting) After(ctx context.Context, ticket Ticket, end CallEnd) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ctx.Err() != nil {
		a.afterCanceled = true
		return ctx.Err()
	}
	if a.ended == nil {
		a.ended = map[int64]bool{}
	}
	if a.ended[ticket.ID] {
		return errors.New("duplicate After")
	}
	a.ended[ticket.ID] = true
	a.ends = append(a.ends, end)
	if a.afterErr != nil {
		return a.afterErr
	}
	a.total += end.Usage.InputTokens + end.Usage.OutputTokens
	return nil
}

func (a *contractAccounting) snapshot() ([]CallStart, []CallEnd, int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.starts), slices.Clone(a.ends), a.total
}

type contractSlots struct {
	attempted                           chan struct{}
	mu                                  sync.Mutex
	sem                                 chan struct{}
	acquired, released, active, maximum int
}

func (s *contractSlots) Acquire(ctx context.Context) (func(), error) {
	if s.attempted != nil {
		s.attempted <- struct{}{}
	}
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	s.mu.Lock()
	s.acquired++
	s.active++
	s.maximum = max(s.maximum, s.active)
	s.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { <-s.sem; s.mu.Lock(); s.released++; s.active--; s.mu.Unlock() }) }, nil
}

func (s *contractSlots) counts() (int, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquired, s.released, s.maximum
}

type contractRequest struct {
	path, method, auth string
	body               map[string]any
}
type contractFixture struct {
	mu       sync.Mutex
	server   *httptest.Server
	requests []contractRequest
	reply    func(http.ResponseWriter, *http.Request, int, map[string]any)
}

func newContractFixture(t *testing.T, providers int, reply func(http.ResponseWriter, *http.Request, int, map[string]any)) (*contractFixture, *contractRepo, *contractAccounting, *contractSlots, Dependencies) {
	t.Helper()
	f := &contractFixture{reply: reply}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("请求不是JSON：%v", err)
		}
		f.mu.Lock()
		index := len(f.requests)
		f.requests = append(f.requests, contractRequest{r.URL.Path, r.Method, r.Header.Get("Authorization"), body})
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if f.reply == nil {
			fmt.Fprint(w, contractSuccess(`{"ok":true}`, "stop", nil))
			return
		}
		f.reply(w, r, index, body)
	}))
	t.Cleanup(f.server.Close)
	repo := &contractRepo{}
	for i := range providers {
		id := int64(i + 1)
		repo.providers = append(repo.providers, Provider{Ref: ProviderRef{ID: id, ConfigToken: "version-1"}, Name: fmt.Sprintf("provider-%d", id), BaseURL: f.server.URL + fmt.Sprintf("/p%d/v1", id), APIKey: "only-local-test-secret", VisionModel: fmt.Sprintf("vision-%d", id), TextModel: fmt.Sprintf("text-%d", id), Enabled: true, SortIndex: id})
	}
	accounting := &contractAccounting{}
	slots := &contractSlots{sem: make(chan struct{}, 2)}
	deps := Dependencies{Repository: repo, Accounting: accounting, Slots: slots, HTTP: f.server.Client(), Timeout: 3 * time.Second, CompatLog: func(context.Context, CompatKey, Compat, Compat) error { return nil }}
	return f, repo, accounting, slots, deps
}

func (f *contractFixture) snapshot() []contractRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

func newContractClient(t *testing.T, deps Dependencies) *Client {
	t.Helper()
	c, err := New(deps)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func contractSuccess(text, finish string, usage any) string {
	value := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": text}, "finish_reason": finish}}}
	if usage != nil {
		value["usage"] = usage
	}
	data, _ := json.Marshal(value)
	return string(data)
}

func contractUsage() map[string]any {
	return map[string]any{"prompt_tokens": 2, "completion_tokens": 3, "completion_tokens_details": map[string]any{"reasoning_tokens": 1}}
}

func contractImage() string {
	var b bytes.Buffer
	_ = jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil)
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
}

func contractJob(kind Kind) Request {
	r := Request{Kind: kind, System: "请输出 JSON。", User: "本地虚构样例JSON"}
	if kind == Market {
		return r
	}
	r.Schema = &Schema{Name: "contract", JSON: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["ok"],"properties":{"ok":{"type":"boolean"}}}`)}
	r.Validate = func(text string) error {
		var value map[string]any
		if err := json.Unmarshal([]byte(text), &value); err != nil {
			return err
		}
		if value["ok"] != true {
			return errors.New("missing ok")
		}
		return nil
	}
	if kind == Analysis {
		r.Images = []string{contractImage()}
	}
	return r
}

func TestContractPurposeParameters(t *testing.T) {
	for _, tc := range []struct {
		kind         Kind
		model        string
		tokens, temp float64
	}{
		{TitleScreen, "text-1", 3000, .1}, {Analysis, "vision-1", 2000, .1}, {Profile, "text-1", 2000, .3}, {Market, "text-1", 400, 0},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			f, _, a, s, deps := newContractFixture(t, 1, nil)
			c := newContractClient(t, deps)
			result, err := c.Complete(context.Background(), contractJob(tc.kind))
			if err != nil {
				t.Fatal(err)
			}
			requests := f.snapshot()
			if len(requests) != 1 {
				t.Fatalf("得到%d个物理请求", len(requests))
			}
			r := requests[0]
			if r.path != "/p1/v1/chat/completions" || r.method != "POST" || r.auth != "Bearer only-local-test-secret" || r.body["model"] != tc.model || r.body["max_tokens"] != tc.tokens {
				t.Fatalf("用途参数不符：%+v", r)
			}
			if tc.kind == Market {
				if _, ok := r.body["temperature"]; ok {
					t.Fatal("行情不能发temperature")
				}
				if _, ok := r.body["response_format"]; ok {
					t.Fatal("行情不能发response_format")
				}
			} else {
				if r.body["temperature"] != tc.temp {
					t.Fatalf("temperature=%v", r.body["temperature"])
				}
				format, _ := r.body["response_format"].(map[string]any)
				schema, _ := format["json_schema"].(map[string]any)
				if format["type"] != "json_schema" || schema["strict"] != true || schema["name"] != "contract" {
					t.Fatalf("结构化格式错误：%v", format)
				}
			}
			starts, ends, _ := a.snapshot()
			if len(starts) != 1 || starts[0].Purpose != string(tc.kind) || len(ends) != 1 || !ends[0].OK || ends[0].Usage.Present || len(result.Calls) != 1 {
				t.Fatalf("请求计量不符：%+v %+v %+v", starts, ends, result)
			}
			if acquired, released, _ := s.counts(); acquired != 1 || released != 1 {
				t.Fatalf("名额没有包住逻辑调用：%d/%d", acquired, released)
			}
		})
	}
}

func TestContractEmptyKeyAndTextModelFallback(t *testing.T) {
	f, repo, _, _, deps := newContractFixture(t, 1, nil)
	repo.providers[0].APIKey, repo.providers[0].TextModel = "", ""
	if _, err := newContractClient(t, deps).Complete(context.Background(), contractJob(TitleScreen)); err != nil {
		t.Fatal(err)
	}
	r := f.snapshot()[0]
	if r.auth != "" || r.body["model"] != "vision-1" {
		t.Fatalf("空Key或文字模型回退错误：%+v", r)
	}
}

func TestContractCompatibilityAndParseAttemptsAreSeparate(t *testing.T) {
	f, repo, accounting, slots, deps := newContractFixture(t, 1, func(w http.ResponseWriter, _ *http.Request, index int, _ map[string]any) {
		switch index {
		case 0:
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"response_format unsupported","usage":{"prompt_tokens":2,"completion_tokens":3}}`)
		case 1:
			w.WriteHeader(422)
			fmt.Fprint(w, `{"error":"json_object unsupported","usage":{"prompt_tokens":2,"completion_tokens":3}}`)
		case 2:
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"temperature not supported","usage":{"prompt_tokens":2,"completion_tokens":3}}`)
		case 3:
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"use max_completion_tokens instead of max_tokens","usage":{"prompt_tokens":2,"completion_tokens":3}}`)
		case 4:
			fmt.Fprint(w, contractSuccess("cut off", "length", contractUsage()))
		case 5:
			fmt.Fprint(w, contractSuccess("not json", "stop", contractUsage()))
		default:
			fmt.Fprint(w, contractSuccess(`{"ok":true}`, "stop", contractUsage()))
		}
	})
	c := newContractClient(t, deps)
	result, err := c.Complete(context.Background(), contractJob(Analysis))
	if err != nil {
		t.Fatal(err)
	}
	requests := f.snapshot()
	if len(requests) != 7 || len(result.Calls) != 7 {
		t.Fatalf("兼容调整不能消耗解析重试：请求%d，Call%d", len(requests), len(result.Calls))
	}
	wantDowngrades := []JSONDowngrade{{JSONSchema, JSONObject, "provider-1", "vision-1"}, {JSONObject, JSONText, "provider-1", "vision-1"}}
	if !slices.Equal(result.JSONDowngrades, wantDowngrades) {
		t.Fatalf("应只记录两个结构化输出降级：%+v", result.JSONDowngrades)
	}
	if _, ok := requests[2].body["response_format"]; ok {
		t.Fatal("text级别不发response_format")
	}
	if _, ok := requests[3].body["temperature"]; ok {
		t.Fatal("不支持温度后不能继续发送")
	}
	if _, ok := requests[4].body["max_tokens"]; ok {
		t.Fatal("token参数切换后不能保留旧参数")
	}
	if requests[5].body["max_completion_tokens"] != float64(8000) {
		t.Fatal("截断后应提升到8000")
	}
	_, ends, total := accounting.snapshot()
	if len(ends) != 7 || total != 35 {
		t.Fatalf("所有失败与重发也要计输入+输出：%d/%d", len(ends), total)
	}
	for i, end := range ends {
		if end.OK != (i == 6) {
			t.Fatalf("第%d次可用结果标记错误：%+v", i, end)
		}
	}
	if acquired, released, _ := slots.counts(); acquired != 1 || released != 1 {
		t.Fatalf("兼容循环重新拿了名额：%d/%d", acquired, released)
	}
	repo.mu.Lock()
	saved := repo.saves
	repo.mu.Unlock()
	if saved != 5 {
		t.Fatalf("兼容变更保存次数=%d", saved)
	}
	if _, err := c.Complete(context.Background(), contractJob(Analysis)); err != nil {
		t.Fatal(err)
	}
	last := f.snapshot()[7]
	if last.body["max_completion_tokens"] != float64(8000) || last.body["temperature"] != nil || last.body["response_format"] != nil {
		t.Fatalf("后续请求没复用兼容数据：%+v", last.body)
	}
}

func TestContractAnalysisParseRetryTemperature(t *testing.T) {
	f, _, a, _, deps := newContractFixture(t, 1, func(w http.ResponseWriter, _ *http.Request, index int, _ map[string]any) {
		if index == 0 {
			fmt.Fprint(w, contractSuccess("bad", "stop", contractUsage()))
			return
		}
		fmt.Fprint(w, contractSuccess(`{"ok":true}`, "stop", contractUsage()))
	})
	if _, err := newContractClient(t, deps).Complete(context.Background(), contractJob(Analysis)); err != nil {
		t.Fatal(err)
	}
	r := f.snapshot()
	if len(r) != 2 || r[0].body["temperature"] != .1 || r[1].body["temperature"] != .05 {
		t.Fatalf("解析重试温度不符：%+v", r)
	}
	_, ends, total := a.snapshot()
	if len(ends) != 2 || ends[0].OK || !ends[1].OK || total != 10 {
		t.Fatalf("解析失败用量或OK错误：%+v/%d", ends, total)
	}
}

func TestContractProviderFailureAggregation(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		statuses              []int
		body                  string
		noProvider, retryable bool
	}{
		{"permanent", []int{401, 403}, `{"error":"bad credentials"}`, true, false},
		{"mixed", []int{503, 401}, `{"error":"failed"}`, false, true},
		{"parse", []int{200, 200}, contractSuccess("bad", "stop", nil), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, repo, _, slots, deps := newContractFixture(t, 2, func(w http.ResponseWriter, r *http.Request, _ int, _ map[string]any) {
				i := 0
				if strings.Contains(r.URL.Path, "/p2/") {
					i = 1
				}
				w.WriteHeader(tc.statuses[i])
				fmt.Fprint(w, tc.body)
			})
			_, err := newContractClient(t, deps).Complete(context.Background(), contractJob(Analysis))
			if tc.noProvider {
				if !errors.Is(err, ErrNoProvider) {
					t.Fatalf("全永久错误应该await：%v", err)
				}
			} else {
				var failed *Failure
				if !errors.As(err, &failed) || failed.Retryable != tc.retryable {
					t.Fatalf("聚合Retryable错误：%#v %v", failed, err)
				}
			}
			wantRequests := 2
			if tc.name == "parse" {
				wantRequests = 4
			}
			if len(f.snapshot()) != wantRequests {
				t.Fatalf("物理请求数=%d，应为%d", len(f.snapshot()), wantRequests)
			}
			if acquired, released, _ := slots.counts(); acquired != 1 || released != 1 {
				t.Fatalf("轮换重新取得名额：%d/%d", acquired, released)
			}
			if tc.name == "permanent" && len(repo.changeKinds()) != 2 {
				t.Fatalf("永久错误未写供应商状态：%v", repo.changeKinds())
			}
		})
	}
}

func TestContractProfileAndMarketNeverRotate(t *testing.T) {
	for _, kind := range []Kind{Profile, Market} {
		t.Run(string(kind), func(t *testing.T) {
			f, _, _, _, deps := newContractFixture(t, 2, func(w http.ResponseWriter, _ *http.Request, _ int, _ map[string]any) {
				w.WriteHeader(503)
				fmt.Fprint(w, `{"error":"busy"}`)
			})
			if _, err := newContractClient(t, deps).Complete(context.Background(), contractJob(kind)); err == nil {
				t.Fatal("服务商失败不能成功")
			}
			if len(f.snapshot()) != 1 || !strings.Contains(f.snapshot()[0].path, "/p1/") {
				t.Fatalf("%s不允许轮换：%+v", kind, f.snapshot())
			}
		})
	}
}
