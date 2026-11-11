package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

type Client struct {
	deps            Dependencies
	mu              sync.Mutex
	accountingFault error
}

func New(deps Dependencies) (*Client, error) {
	if nilDependency(deps.Repository) || nilDependency(deps.Accounting) || nilDependency(deps.Slots) || nilDependency(deps.HTTP) || deps.Timeout <= 0 || deps.CompatLog == nil {
		return nil, &Failure{Last: reason("invalid_request", 0, "")}
	}
	if h, ok := deps.HTTP.(*http.Client); ok {
		copy := *h
		copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		deps.HTTP = &copy
	}
	deps.Secrets = append([]string(nil), deps.Secrets...)
	return &Client{deps: deps}, nil
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

func (c *Client) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.accountingFault
}

func (c *Client) enter(ctx context.Context) (func(), error) {
	if err := c.check(ctx); err != nil {
		return nil, err
	}
	release, err := c.deps.Slots.Acquire(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, infrastructure(err, c.deps.Secrets)
	}
	if release == nil {
		return nil, infrastructure(errors.New("missing slot release"), c.deps.Secrets)
	}
	if err := c.check(ctx); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func (c *Client) Complete(ctx context.Context, req Request) (result Result, err error) {
	started := time.Now()
	defer func() { result.Duration = time.Since(started) }()
	if err := validateRequest(req); err != nil {
		return result, err
	}
	release, err := c.enter(ctx)
	if err != nil {
		return result, err
	}
	defer release()
	providers, err := c.deps.Repository.Providers(ctx)
	if err != nil {
		return result, c.storeError(ctx, err, c.deps.Secrets)
	}
	secrets := append([]string(nil), c.deps.Secrets...)
	var candidates []Provider
	for _, p := range providers {
		secrets = append(secrets, p.APIKey)
		if p.Enabled && p.ErrorState == nil {
			candidates = append(candidates, p)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].SortIndex == candidates[j].SortIndex {
			return candidates[i].Ref.ID < candidates[j].Ref.ID
		}
		return candidates[i].SortIndex < candidates[j].SortIndex
	})
	if len(candidates) == 0 {
		return result, ErrNoProvider
	}
	rotate := req.Kind == TitleScreen || req.Kind == Analysis
	if !rotate {
		candidates = candidates[:1]
	}
	allBlocked, retryable := true, false
	var last *Failure
	for _, provider := range candidates {
		part, callErr := c.runProvider(ctx, provider, req, string(req.Kind), secrets)
		result.Calls = append(result.Calls, part.Calls...)
		result.JSONDowngrades = append(result.JSONDowngrades, part.JSONDowngrades...)
		result.Thinking = result.Thinking || part.Thinking
		result.ProviderID, result.Model, result.JSONMode = part.ProviderID, part.Model, part.JSONMode
		if callErr == nil {
			result.Text = part.Text
			return result, nil
		}
		var failure *Failure
		if !errors.As(callErr, &failure) {
			return result, callErr
		}
		last = failure
		retryable = retryable || failure.Retryable
		allBlocked = allBlocked && permanent(failure.Last.Code)
	}
	if rotate && allBlocked {
		return result, ErrNoProvider
	}
	if last != nil {
		last.Retryable = retryable
		return result, last
	}
	return result, ErrNoProvider
}

func (c *Client) Test(ctx context.Context, providerID int64, title, analysis Request) (result TestResult, err error) {
	if title.Kind != TitleScreen || analysis.Kind != Analysis {
		return result, &Failure{Last: reason("invalid_request", 0, "")}
	}
	if err := validateRequest(title); err != nil {
		return result, err
	}
	if err := validateRequest(analysis); err != nil {
		return result, err
	}
	release, err := c.enter(ctx)
	if err != nil {
		return result, err
	}
	defer release()
	provider, err := c.deps.Repository.Provider(ctx, providerID)
	if err != nil {
		return result, c.storeError(ctx, err, c.deps.Secrets)
	}
	secrets := append(append([]string(nil), c.deps.Secrets...), provider.APIKey)
	result.Text.Result, result.Text.Err = c.runProvider(ctx, provider, title, "test", secrets)
	if result.Text.Err != nil {
		var failure *Failure
		if !errors.As(result.Text.Err, &failure) {
			return result, result.Text.Err
		}
	}
	result.Vision.Result, result.Vision.Err = c.runProvider(ctx, provider, analysis, "test", secrets)
	if result.Vision.Err != nil {
		return result, result.Vision.Err
	}
	if result.Text.Err != nil {
		return result, result.Text.Err
	}
	if err := c.deps.Repository.ChangeProvider(ctx, provider.Ref, ProviderChange{Kind: "test_success"}); err != nil {
		return result, c.storeError(ctx, err, secrets)
	}
	return result, nil
}

func (c *Client) Probe(ctx context.Context, providerID int64) (result Result, err error) {
	started := time.Now()
	defer func() { result.Duration = time.Since(started) }()
	release, err := c.enter(ctx)
	if err != nil {
		return result, err
	}
	defer release()
	provider, err := c.deps.Repository.Provider(ctx, providerID)
	if err != nil {
		return result, c.storeError(ctx, err, c.deps.Secrets)
	}
	secrets := append(append([]string(nil), c.deps.Secrets...), provider.APIKey)
	result, err = c.runProvider(ctx, provider, Request{Kind: Market, User: "Reply with OK"}, "probe", secrets)
	if err != nil {
		return result, err
	}
	if err := c.deps.Repository.ChangeProvider(ctx, provider.Ref, ProviderChange{Kind: "probe_success"}); err != nil {
		return result, c.storeError(ctx, err, secrets)
	}
	return result, nil
}

func validateRequest(req Request) error {
	if req.Kind != TitleScreen && req.Kind != Analysis && req.Kind != Profile && req.Kind != Market {
		return &Failure{Last: reason("invalid_request", 0, "")}
	}
	if req.Kind != Market && (req.Schema == nil || req.Validate == nil) {
		return &Failure{Last: reason("invalid_request", 0, "")}
	}
	if len(req.Images) != 0 && req.Kind != Analysis {
		return &Failure{Last: reason("invalid_request", 0, "")}
	}
	return nil
}

func (c *Client) storeError(ctx context.Context, err error, secrets []string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return infrastructure(err, secrets)
}

func reason(code string, status int, detail string) Reason {
	return Reason{Code: "analysis.client." + code, Args: map[string]any{"status": status, "detail": detail}}
}

func permanent(code string) bool {
	switch code {
	case "analysis.client.auth", "analysis.client.balance", "analysis.client.not_found", "analysis.client.unsupported_image", "analysis.client.daily_quota", "analysis.client.thinking_too_long":
		return true
	}
	return false
}

func (c *Client) runProvider(ctx context.Context, provider Provider, req Request, purpose string, secrets []string) (result Result, err error) {
	started := time.Now()
	defer func() { result.Duration = time.Since(started) }()
	model := provider.TextModel
	if req.Kind == Analysis {
		model = provider.VisionModel
	}
	if model == "" {
		model = provider.VisionModel
	}
	result.ProviderID, result.Model = provider.Ref.ID, model
	key := CompatKey{Provider: provider.Ref, BaseURL: provider.BaseURL, Model: model}
	compat, err := c.deps.Repository.LoadCompat(ctx, key)
	if err != nil {
		return result, c.storeError(ctx, err, secrets)
	}
	if !validCompat(compat) {
		return result, infrastructure(errors.New("invalid compatibility state"), secrets)
	}
	structured := req.Kind != Market && purpose != "probe"
	parseRetry := false
	for {
		if err := c.check(ctx); err != nil {
			return result, err
		}
		httpRequest, buildErr := buildRequest(ctx, provider, req, compat, purpose, parseRetry)
		if buildErr != nil {
			return result, &Failure{Last: reason("invalid_request", 0, "")}
		}
		mode := JSONText
		if structured {
			mode = compat.JSONMode
		}
		start := CallStart{Provider: provider.Ref, Model: model, Purpose: purpose, TaskID: req.TaskID, JSONMode: mode}
		ticket, beforeErr := c.deps.Accounting.Before(ctx, start)
		if beforeErr != nil {
			if errors.Is(beforeErr, ErrDailyLimit) {
				return result, ErrDailyLimit
			}
			return result, c.storeError(ctx, beforeErr, secrets)
		}
		requestCtx, cancel := context.WithTimeout(ctx, c.deps.Timeout)
		body, end, decoded, transportErr := c.physical(requestCtx, httpRequest.WithContext(requestCtx))
		cancel()
		result.Thinking = result.Thinking || decoded.thinking
		result.JSONMode = mode
		decoded.text = redactPlain(decoded.text, secrets)
		var next Compat
		var changed, blocked, retryable bool
		var failureReason Reason
		success := transportErr == nil && end.HTTPStatus >= 200 && end.HTTPStatus < 300
		switch {
		case transportErr != nil:
			failureReason, retryable, blocked = classifyFailure(end.HTTPStatus, transportErr.Error(), transportErr, provider.BaseURL)
		case !success:
			next, changed = compatibilityChange(end.HTTPStatus, body, compat, structured)
			failureReason, retryable, blocked = classifyFailure(end.HTTPStatus, string(body), nil, provider.BaseURL)
		case purpose == "probe":
			end.OK = true
		case decoded.finishReason == "length":
			failureReason = reason("thinking_too_long", end.HTTPStatus, string(body))
			if compat.MaxTokensFloor == nil || *compat.MaxTokensFloor < 8000 {
				floor := int64(8000)
				next, changed = compat, true
				next.MaxTokensFloor = &floor
			} else {
				blocked = true
			}
		default:
			_, decodeErr := decodeResponse(body, false)
			if req.Kind == Profile && decoded.incompleteThinking {
				failureReason = reason("profile_thinking", end.HTTPStatus, string(body))
			} else if decodeErr != nil || structured && req.Validate(decoded.text) != nil {
				failureReason = reason("parse", end.HTTPStatus, string(body))
			} else {
				end.OK = true
			}
		}
		if !end.OK {
			end.ErrorKind = strings.TrimPrefix(failureReason.Code, "analysis.client.")
			if changed {
				end.ErrorKind = "compatibility"
			}
		}
		call := Call{Start: start, End: end}
		result.Calls = append(result.Calls, call)
		if afterErr := c.finish(ctx, ticket, end, secrets); afterErr != nil {
			return result, afterErr
		}
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if errors.Is(transportErr, context.Canceled) {
			return result, context.Canceled
		}
		if changed {
			old := compat
			compat, err = c.saveCompat(ctx, key, old, next, secrets)
			if err != nil {
				return result, err
			}
			if old.JSONMode != compat.JSONMode {
				result.JSONDowngrades = append(result.JSONDowngrades, JSONDowngrade{
					From: old.JSONMode, To: compat.JSONMode,
					Provider: redactPlain(provider.Name, secrets), Model: redactPlain(model, secrets),
				})
			}
			continue
		}
		if end.OK {
			if err := c.deps.Repository.ChangeProvider(ctx, provider.Ref, ProviderChange{Kind: "request_success"}); err != nil {
				return result, c.storeError(ctx, err, secrets)
			}
			result.Text = decoded.text
			return result, nil
		}
		failureReason = cleanReason(failureReason, secrets)
		if blocked {
			if err := c.deps.Repository.ChangeProvider(ctx, provider.Ref, ProviderChange{Kind: "blocked", Reason: &failureReason}); err != nil {
				return result, c.storeError(ctx, err, secrets)
			}
		}
		if success && !parseRetry && (req.Kind == TitleScreen || req.Kind == Analysis) && failureReason.Code == "analysis.client.parse" {
			parseRetry = true
			continue
		}
		return result, &Failure{Last: failureReason, Retryable: retryable}
	}
}

func (c *Client) physical(ctx context.Context, request *http.Request) ([]byte, CallEnd, decodedResponse, error) {
	started := time.Now()
	var end CallEnd
	response, err := c.deps.HTTP.Do(request)
	var body []byte
	if response != nil {
		end.HTTPStatus = response.StatusCode
		if response.Body != nil {
			body, err = readResponse(response.Body, err)
		}
	}
	decoded, _ := decodeResponse(body, true)
	end.Usage, end.FinishReason, end.Duration = decoded.usage, decoded.finishReason, time.Since(started)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if response == nil && err == nil {
		err = errors.New("empty HTTP response")
	}
	return body, end, decoded, err
}

func readResponse(body io.ReadCloser, prior error) ([]byte, error) {
	data, readErr := io.ReadAll(body)
	closeErr := body.Close()
	if prior != nil {
		return data, prior
	}
	if readErr != nil {
		return data, readErr
	}
	return data, closeErr
}

func (c *Client) finish(ctx context.Context, ticket Ticket, end CallEnd, secrets []string) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.deps.Timeout)
	defer cancel()
	if err := c.deps.Accounting.After(cleanup, ticket, end); err != nil {
		failure := infrastructure(err, secrets)
		// 记账失败后不能继续按旧用量放行；只有新进程才重新开始。
		c.mu.Lock()
		if c.accountingFault == nil {
			c.accountingFault = failure
		}
		fault := c.accountingFault
		c.mu.Unlock()
		return fault
	}
	return nil
}

func validCompat(c Compat) bool {
	return modeRank(c.JSONMode) >= 0 && (c.MaxTokensParam == "max_tokens" || c.MaxTokensParam == "max_completion_tokens") && (c.MaxTokensFloor == nil || *c.MaxTokensFloor >= 0)
}

func modeRank(mode JSONMode) int {
	switch mode {
	case JSONSchema:
		return 0
	case JSONObject:
		return 1
	case JSONText:
		return 2
	}
	return -1
}

func includesCompat(saved, requested Compat) bool {
	if !validCompat(saved) || modeRank(saved.JSONMode) < modeRank(requested.JSONMode) || !requested.SendTemperature && saved.SendTemperature || requested.MaxTokensParam == "max_completion_tokens" && saved.MaxTokensParam != requested.MaxTokensParam {
		return false
	}
	if requested.MaxTokensFloor != nil && (saved.MaxTokensFloor == nil || *saved.MaxTokensFloor < *requested.MaxTokensFloor) {
		return false
	}
	return true
}

func (c *Client) saveCompat(ctx context.Context, key CompatKey, old, requested Compat, secrets []string) (Compat, error) {
	saved, err := c.deps.Repository.SaveCompat(ctx, key, requested)
	if err != nil {
		return Compat{}, c.storeError(ctx, err, secrets)
	}
	if !includesCompat(saved, requested) || !includesCompat(saved, old) || reflect.DeepEqual(old, saved) {
		return Compat{}, infrastructure(fmt.Errorf("compatibility did not advance"), secrets)
	}
	key.Model = redactPlain(key.Model, secrets)
	if err := c.deps.CompatLog(ctx, key, old, saved); err != nil {
		return Compat{}, c.storeError(ctx, err, secrets)
	}
	return saved, nil
}
