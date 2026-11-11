package images

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"

	"github.com/Seaony/Kakatta/internal/locales"
)

type preparationError string

func (e preparationError) Error() string { return string(e) }
func (e preparationError) Message() locales.Message {
	return locales.Message{Code: string(e), Args: map[string]any{}}
}

const errInvalidTarget preparationError = "analysis.images.invalid_url"

var downloadSlots = make(chan struct{}, DownloadConcurrency)

type Preparer struct {
	http   HTTPDoer
	limits Limits
	slots  chan struct{}
}

func New(deps Dependencies) (*Preparer, error) {
	limits, err := normalizeLimits(deps.Limits)
	if err != nil || nilHTTP(deps.HTTP) {
		return nil, preparationError("analysis.images.invalid_configuration")
	}
	doer := deps.HTTP
	if supplied, ok := doer.(*http.Client); ok {
		copy := *supplied
		copy.Jar = nil
		copy.Transport = withoutEnvironmentProxy(supplied.Transport)
		previous := copy.CheckRedirect
		copy.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return preparationError("analysis.images.download_failed")
			}
			if previous != nil {
				if err := previous(req, via); err != nil {
					return err
				}
			}
			if !validTarget(req.URL) {
				return errInvalidTarget
			}
			req.URL.Scheme = strings.ToLower(req.URL.Scheme)
			req.Header.Del("Cookie")
			req.Header.Del("Authorization")
			return nil
		}
		doer = &copy
	}
	return &Preparer{http: doer, limits: limits, slots: downloadSlots}, nil
}

func normalizeLimits(limits Limits) (Limits, error) {
	if limits.SourceBytes == 0 {
		limits.SourceBytes = SourceBytes
	}
	if limits.SourcePixels == 0 {
		limits.SourcePixels = SourcePixels
	}
	if limits.SourceBytes < 0 || limits.SourceBytes > SourceBytes || limits.SourcePixels < 0 || limits.SourcePixels > SourcePixels {
		return Limits{}, preparationError("analysis.images.invalid_configuration")
	}
	return limits, nil
}

func nilHTTP(doer HTTPDoer) bool {
	if doer == nil {
		return true
	}
	value := reflect.ValueOf(doer)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	}
	return false
}

func withoutEnvironmentProxy(transport http.RoundTripper) http.RoundTripper {
	if transport == nil {
		transport = http.DefaultTransport
	}
	base, ok := transport.(*http.Transport)
	if !ok || base.Proxy == nil || reflect.ValueOf(base.Proxy).Pointer() != reflect.ValueOf(http.ProxyFromEnvironment).Pointer() {
		return transport
	}
	copy := base.Clone()
	copy.Proxy = nil
	// 这条兼容默认传输的路径没有外部关闭者，不保留空闲连接。
	copy.DisableKeepAlives = true
	return copy
}

func validTarget(target *url.URL) bool {
	return target != nil && (strings.EqualFold(target.Scheme, "http") || strings.EqualFold(target.Scheme, "https")) && target.Host != "" && target.User == nil && target.Opaque == ""
}

func (p *Preparer) Prepare(ctx context.Context, detailURLs []string, imageCount int64) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if imageCount <= 0 {
		return Result{}, preparationError("analysis.images.invalid_count")
	}
	count := len(detailURLs)
	if imageCount < int64(count) {
		count = int(imageCount)
	}
	type outcome struct {
		image *Image
		issue *Issue
	}
	outcomes := make([]outcome, count)
	jobs := make(chan int)
	var workers sync.WaitGroup
	workers.Add(min(count, DownloadConcurrency))
	for range min(count, DownloadConcurrency) {
		go func() {
			defer workers.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				prepared, issue := p.downloadJPEG(ctx, detailURLs[index], index)
				var image *Image
				if prepared != nil {
					image = &Image{DetailIndex: index, DataURL: "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(prepared)}
				}
				outcomes[index] = outcome{image, issue}
			}
		}()
	}
dispatch:
	for index := range count {
		select {
		case <-ctx.Done():
			break dispatch
		case jobs <- index:
		}
	}
	close(jobs)
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	var result Result
	for _, outcome := range outcomes {
		if outcome.image != nil {
			result.Images = append(result.Images, *outcome.image)
		}
		if outcome.issue != nil {
			result.Skipped = append(result.Skipped, *outcome.issue)
		}
	}
	return result, nil
}

// DownloadJPEG 供本地缩略图使用，沿用详情图的下载和解码限制。
func (p *Preparer) DownloadJPEG(ctx context.Context, address string) ([]byte, error) {
	prepared, issue := p.downloadJPEG(ctx, address, 0)
	if issue != nil {
		return nil, preparationError(issue.Reason.Code)
	}
	return prepared, ctx.Err()
}

func (p *Preparer) downloadJPEG(ctx context.Context, address string, index int) ([]byte, *Issue) {
	target, err := url.Parse(address)
	if err != nil || !validTarget(target) {
		return nil, skipped(index, "invalid_url", nil)
	}
	target.Scheme = strings.ToLower(target.Scheme)
	select {
	case <-ctx.Done():
		return nil, nil
	case p.slots <- struct{}{}:
	}
	defer func() { <-p.slots }()
	if ctx.Err() != nil {
		return nil, nil
	}
	requestCtx, cancel := context.WithTimeout(ctx, RequestTimeout)
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		cancel()
		return nil, skipped(index, "invalid_url", nil)
	}
	response, err := p.http.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		issue := skipped(index, "download_failed", nil)
		if requestCtx.Err() != nil || timeoutError(err) {
			issue = skipped(index, "timeout", nil)
		}
		if errors.Is(err, errInvalidTarget) {
			issue = skipped(index, "invalid_url", nil)
		}
		cancel()
		return nil, issue
	}
	if response == nil || response.Body == nil {
		cancel()
		return nil, skipped(index, "download_failed", nil)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_ = response.Body.Close()
		cancel()
		return nil, skipped(index, "http_failed", map[string]any{"status": response.StatusCode})
	}
	if response.ContentLength > p.limits.SourceBytes {
		_ = response.Body.Close()
		cancel()
		return nil, skipped(index, "resource_limit", map[string]any{"field": "source_bytes"})
	}
	source, readErr := io.ReadAll(io.LimitReader(response.Body, p.limits.SourceBytes+1))
	closeErr := response.Body.Close()
	timedOut := requestCtx.Err() != nil || timeoutError(readErr)
	cancel()
	if ctx.Err() != nil {
		return nil, nil
	}
	if readErr != nil || closeErr != nil || timedOut {
		if timedOut {
			return nil, skipped(index, "timeout", nil)
		}
		return nil, skipped(index, "download_failed", nil)
	}
	if int64(len(source)) > p.limits.SourceBytes {
		return nil, skipped(index, "resource_limit", map[string]any{"field": "source_bytes"})
	}
	prepared, err := JPEG(source, p.limits)
	if ctx.Err() != nil {
		return nil, nil
	}
	if err != nil {
		var message interface{ Message() locales.Message }
		if errors.As(err, &message) {
			return nil, &Issue{DetailIndex: index, Reason: message.Message()}
		}
		return nil, skipped(index, "invalid_image", nil)
	}
	return prepared, nil
}

func timeoutError(err error) bool {
	var networkError net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout()
}

func skipped(index int, code string, args map[string]any) *Issue {
	if args == nil {
		args = map[string]any{}
	}
	return &Issue{DetailIndex: index, Reason: locales.Message{Code: "analysis.images." + code, Args: args}}
}

func (r Result) DataURLs() []string {
	data := make([]string, len(r.Images))
	for index, image := range r.Images {
		data[index] = image.DataURL
	}
	return data
}
