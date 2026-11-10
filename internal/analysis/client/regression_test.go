package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
)

func TestTruncationBlocksAfterEightThousandForAllWorkloads(t *testing.T) {
	for _, kind := range []Kind{TitleScreen, Analysis, Profile, Market} {
		t.Run(string(kind), func(t *testing.T) {
			f, repo, accounting, _, deps := newContractFixture(t, 1, func(w http.ResponseWriter, _ *http.Request, _ int, _ map[string]any) {
				fmt.Fprint(w, contractSuccess("truncated", "length", contractUsage()))
			})
			result, err := newContractClient(t, deps).Complete(context.Background(), contractJob(kind))
			if kind == TitleScreen || kind == Analysis {
				if !errors.Is(err, ErrNoProvider) {
					t.Fatalf("全被禁用应留待分析：%v", err)
				}
			} else {
				var failure *Failure
				if !errors.As(err, &failure) || failure.Last.Code != "analysis.client.thinking_too_long" {
					t.Fatalf("单服务商用途应直接报截断：%v", err)
				}
			}
			requests := f.snapshot()
			if len(requests) != 2 || requests[1].body["max_tokens"] != float64(8000) {
				t.Fatalf("调高一次后仍截断就结束：%+v", requests)
			}
			_, ends, total := accounting.snapshot()
			if len(result.Calls) != 2 || len(ends) != 2 || ends[0].OK || ends[1].OK || total != 10 {
				t.Fatalf("截断请求也要完整计量：%+v %d", ends, total)
			}
			if !strings.Contains(fmt.Sprint(repo.changeKinds()), "blocked") {
				t.Fatal("重复截断没有禁用服务商")
			}
		})
	}
}

func TestHTTPTimeoutAccountsThenRotatesWithoutRetryingProvider(t *testing.T) {
	for _, flush := range []bool{false, true} {
		t.Run(fmt.Sprint(flush), func(t *testing.T) {
			f, _, accounting, _, deps := newContractFixture(t, 2, func(w http.ResponseWriter, r *http.Request, _ int, _ map[string]any) {
				if strings.Contains(r.URL.Path, "/p1/") {
					if flush {
						w.WriteHeader(http.StatusOK)
						w.(http.Flusher).Flush()
					}
					<-r.Context().Done()
					return
				}
				fmt.Fprint(w, contractSuccess(`{"ok":true}`, "stop", contractUsage()))
			})
			deps.Timeout = 250 * time.Millisecond
			result, err := newContractClient(t, deps).Complete(context.Background(), contractJob(Analysis))
			if err != nil || result.ProviderID != 2 {
				t.Fatalf("请求或正文超时应换下一家：%+v %v", result, err)
			}
			requests := f.snapshot()
			_, ends, _ := accounting.snapshot()
			if len(requests) != 2 || len(ends) != 2 || ends[0].OK || ends[0].ErrorKind != "timeout" || !ends[1].OK {
				t.Fatalf("超时记账/轮换错误：%+v %+v", requests, ends)
			}
		})
	}
}

func TestProfileUnclosedThinkingIsUnavailableWithoutParseRetry(t *testing.T) {
	f, _, accounting, _, deps := newContractFixture(t, 2, func(w http.ResponseWriter, _ *http.Request, _ int, _ map[string]any) {
		fmt.Fprint(w, contractSuccess("<think>unfinished", "stop", contractUsage()))
	})
	_, err := newContractClient(t, deps).Complete(context.Background(), contractJob(Profile))
	var failure *Failure
	if !errors.As(err, &failure) || failure.Last.Code != "analysis.client.profile_thinking" || len(f.snapshot()) != 1 {
		t.Fatalf("未闭合思考要给生成提示，不重试或轮换：%v", err)
	}
	_, ends, _ := accounting.snapshot()
	if len(ends) != 1 || ends[0].OK {
		t.Fatalf("不可用生成不能记成功：%+v", ends)
	}
}

func TestClientReasonsHaveBothTranslations(t *testing.T) {
	codes := []string{"no_provider", "daily_limit", "infrastructure", "config_changed", "invalid_request", "auth", "balance", "not_found", "unsupported_image", "daily_quota", "thinking_too_long", "rate_limit", "server", "network", "timeout", "parse", "protocol", "localhost", "cancelled", "test_failed", "profile_thinking"}
	for _, code := range codes {
		for _, lang := range []locales.Language{locales.ZhCN, locales.En} {
			if text, err := locales.Format(lang, reason(code, 0, "")); err != nil || text == "" {
				t.Fatalf("原因缺少%s文案%s：%v", lang, code, err)
			}
		}
	}
}

func TestExtraCredentialsAreRedactedFromInfrastructureErrors(t *testing.T) {
	secret := `proxy-secret`
	_, repo, _, _, deps := newContractFixture(t, 1, nil)
	deps.Secrets = []string{secret}
	repo.loadErr = fmt.Errorf("proxy http://someone:%s@127.0.0.1:1234 failed", secret)
	_, err := newContractClient(t, deps).Complete(context.Background(), contractJob(Analysis))
	if !errors.Is(err, ErrInfrastructure) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "someone:") {
		t.Fatalf("额外代理凭据未遮密：%v", err)
	}
	encoded, _ := json.Marshal(reason("network", 0, redactText(`{"message":"proxy-secret"}`, []string{secret})))
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("转义详情保留了凭据：%s", encoded)
	}
}

type canceledHTTP struct{ calls int }

func (h *canceledHTTP) Do(*http.Request) (*http.Response, error) {
	h.calls++
	return nil, fmt.Errorf("request cancelled: %w", context.Canceled)
}

func TestTransportCancellationKeepsContextTypeAndDoesNotRotate(t *testing.T) {
	_, _, accounting, _, deps := newContractFixture(t, 2, nil)
	h := &canceledHTTP{}
	deps.HTTP = h
	_, err := newContractClient(t, deps).Complete(context.Background(), contractJob(Analysis))
	if !errors.Is(err, context.Canceled) || h.calls != 1 {
		t.Fatalf("传输取消不能降成普通失败或轮换：%v %d", err, h.calls)
	}
	_, ends, _ := accounting.snapshot()
	if len(ends) != 1 || ends[0].ErrorKind != "cancelled" {
		t.Fatalf("传输取消也要记账：%+v", ends)
	}
}

func TestRequestUsesAtomicallyMergedCompatibilityState(t *testing.T) {
	var repo *contractRepo
	f, stored, _, _, deps := newContractFixture(t, 1, func(w http.ResponseWriter, _ *http.Request, index int, _ map[string]any) {
		if index == 0 {
			repo.mu.Lock()
			p := repo.providers[0]
			key := CompatKey{Provider: p.Ref, BaseURL: p.BaseURL, Model: p.VisionModel}
			floor := int64(8000)
			repo.compat = map[string]Compat{contractCompatKey(key): {JSONMode: JSONText, SendTemperature: true, MaxTokensParam: "max_completion_tokens", MaxTokensFloor: &floor}}
			repo.mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"temperature unsupported"}`)
			return
		}
		fmt.Fprint(w, contractSuccess(`{"ok":true}`, "stop", contractUsage()))
	})
	repo = stored
	if _, err := newContractClient(t, deps).Complete(context.Background(), contractJob(Analysis)); err != nil {
		t.Fatal(err)
	}
	requests := f.snapshot()
	if len(requests) != 2 || requests[1].body["temperature"] != nil || requests[1].body["response_format"] != nil || requests[1].body["max_completion_tokens"] != float64(8000) {
		t.Fatalf("不能用本请求的旧快照覆盖并发兼容变化：%+v", requests)
	}
}

func TestResponseCannotWriteOverProviderEditedDuringRequest(t *testing.T) {
	var repo *contractRepo
	f, stored, _, _, deps := newContractFixture(t, 2, func(w http.ResponseWriter, _ *http.Request, _ int, _ map[string]any) {
		repo.mu.Lock()
		repo.providers[0].Ref.ConfigToken = "new-config"
		repo.mu.Unlock()
		fmt.Fprint(w, contractSuccess(`{"ok":true}`, "stop", contractUsage()))
	})
	repo = stored
	if _, err := newContractClient(t, deps).Complete(context.Background(), contractJob(Analysis)); !errors.Is(err, ErrConfigChanged) {
		t.Fatalf("在途旧响应不能改新配置：%v", err)
	}
	if len(f.snapshot()) != 1 || len(repo.changeKinds()) != 0 {
		t.Fatal("旧响应写了新状态或换了服务商")
	}
}
