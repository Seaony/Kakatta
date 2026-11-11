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
	for _, test := range []struct{ name, body, code string }{
		{"unclosed", "<think>unfinished", "profile_thinking"},
		{"closed_only", " <think>只有思考</think> \n <think>第二段</think> ", "profile_thinking"},
		{"empty_without_tags", " ", "parse"},
		{"closed_then_answer", `<think>草稿</think>{"ok":true}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, _, accounting, _, deps := newContractFixture(t, 2, func(w http.ResponseWriter, _ *http.Request, _ int, _ map[string]any) {
				fmt.Fprint(w, contractSuccess(test.body, "stop", contractUsage()))
			})
			result, err := newContractClient(t, deps).Complete(context.Background(), contractJob(Profile))
			var failure *Failure
			if test.code == "" {
				if err != nil || result.Text != `{"ok":true}` {
					t.Fatalf("应保留思考之后的正文：%+v %v", result, err)
				}
			} else if !errors.As(err, &failure) || failure.Last.Code != "analysis.client."+test.code {
				t.Fatalf("思考与空正文的提示不正确：%v", err)
			}
			_, ends, _ := accounting.snapshot()
			if len(f.snapshot()) != 1 || len(ends) != 1 || ends[0].OK != (test.code == "") {
				t.Fatalf("清单生成不能重试或轮换，不可用响应不能记成功：%+v", ends)
			}
		})
	}
}

func TestDowngradeEventSurvivesQuotaAndRedactsProviderFields(t *testing.T) {
	f, repo, accounting, _, deps := newContractFixture(t, 1, func(w http.ResponseWriter, _ *http.Request, _ int, _ map[string]any) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"response_format unsupported","usage":{"prompt_tokens":2,"completion_tokens":3}}`)
	})
	secret := repo.providers[0].APIKey
	repo.providers[0].Name += " " + secret
	repo.providers[0].VisionModel += " " + secret
	accounting.limit = 5
	var loggedModel string
	deps.CompatLog = func(_ context.Context, key CompatKey, _, _ Compat) error { loggedModel = key.Model; return nil }
	result, err := newContractClient(t, deps).Complete(t.Context(), contractJob(Analysis))
	if !errors.Is(err, ErrDailyLimit) || len(f.snapshot()) != 1 || len(result.JSONDowngrades) != 1 {
		t.Fatalf("次次请求被额度挡住时应保留实际发生的降级：%+v %v", result.JSONDowngrades, err)
	}
	want := JSONDowngrade{JSONSchema, JSONObject, "provider-1 [REDACTED]", "vision-1 [REDACTED]"}
	if result.JSONDowngrades[0] != want || loggedModel != want.Model {
		t.Fatalf("降级日志的服务商或模型没有遮密：%+v %s", result.JSONDowngrades, loggedModel)
	}
}

func TestClientReasonsHaveBothTranslations(t *testing.T) {
	codes := []string{"no_provider", "daily_limit", "infrastructure", "config_changed", "invalid_request", "auth", "balance", "not_found", "unsupported_image", "daily_quota", "thinking_too_long", "rate_limit", "server", "network", "timeout", "parse", "protocol", "localhost", "cancelled", "test_failed", "profile_thinking"}
	for _, code := range codes {
		for _, lang := range []locales.Language{locales.ZhCN, locales.En, locales.Ja} {
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

func TestStructuredResultRedactsDecodedTextAndFinishReason(t *testing.T) {
	const secret = "fixture-key"
	const prefix = "说明\n```json\n"
	const suffix = "\n```\n附注"
	const body = `{"summary":"本地样例：\u0066ixture-key","evidence":"含有引号\"和反斜线\\","amount":9007199254740993}`
	_, repo, accounting, _, deps := newContractFixture(t, 1, func(w http.ResponseWriter, _ *http.Request, _ int, _ map[string]any) {
		fmt.Fprint(w, contractSuccess(prefix+body+suffix, secret, contractUsage()))
	})
	repo.providers[0].APIKey = secret
	job := contractJob(Analysis)
	job.Validate = func(text string) error {
		var value struct {
			Summary  string      `json:"summary"`
			Evidence string      `json:"evidence"`
			Amount   json.Number `json:"amount"`
		}
		if !strings.HasPrefix(text, prefix) || !strings.HasSuffix(text, suffix) {
			return errors.New("丢掉了围栏或前后说明")
		}
		if err := json.Unmarshal([]byte(text[len(prefix):len(text)-len(suffix)]), &value); err != nil {
			return err
		}
		if value.Summary != "本地样例：[REDACTED]" || value.Evidence != "含有引号\"和反斜线\\" || value.Amount != "9007199254740993" {
			return errors.New("结构化结果没有正确脱敏或改变了原有数据")
		}
		return nil
	}
	result, err := newContractClient(t, deps).Complete(t.Context(), job)
	if err != nil || job.Validate(result.Text) != nil {
		t.Fatal("业务解析与返回的结构化内容都应已经脱敏")
	}
	_, ends, _ := accounting.snapshot()
	if len(ends) != 1 || ends[0].FinishReason != "[REDACTED]" || result.Calls[0].End.FinishReason != "[REDACTED]" {
		t.Fatal("记账和调用结果的结束原因都应已经脱敏")
	}
}

func TestFailureDetailRedactsNumericCredential(t *testing.T) {
	const secret = "9147485552310773"
	_, repo, _, _, deps := newContractFixture(t, 1, func(w http.ResponseWriter, _ *http.Request, _ int, _ map[string]any) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"credential":`+secret+`,"number":9007199254740993}`)
	})
	repo.providers[0].APIKey = secret
	_, err := newContractClient(t, deps).Complete(t.Context(), contractJob(Profile))
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatal("应返回模型鉴权失败")
	}
	detail, _ := failure.Last.Args["detail"].(string)
	if detail != `{"credential":"[REDACTED]","number":9007199254740993}` {
		t.Fatal("模型错误应遮住数字格式的凭据并保留其他数字的精度")
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
