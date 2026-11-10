package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/mod/semver"
)

const latestEndpoint = "https://api.github.com/repos/Seaony/Kakatta/releases/latest"
const responseLimit = 1 << 20

type fetchOutcome struct {
	release            *Release
	etag, lastModified string
	retryAt            *int64
}

func (c *Checker) fetch(ctx context.Context, state cacheState) (fetchOutcome, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, latestEndpoint, nil)
	if err != nil {
		return fetchOutcome{}, err
	}
	request.Header.Set("User-Agent", "Kakatta/"+c.deps.CurrentVersion)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	if state.ETag != "" {
		request.Header.Set("If-None-Match", state.ETag)
	} else if state.LastModified != "" {
		request.Header.Set("If-Modified-Since", state.LastModified)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return fetchOutcome{}, err
	}
	defer response.Body.Close()
	now := max(*state.LastAttemptAt, c.deps.Now().UTC().UnixMilli())
	result := fetchOutcome{retryAt: responseWait(response.Header, now)}
	switch response.StatusCode {
	case http.StatusNotModified:
		if state.KnownRelease == nil || state.ETag == "" && state.LastModified == "" {
			return result, errors.New("更新检查返回304但没有条件缓存")
		}
		result.release, result.etag, result.lastModified = state.KnownRelease, state.ETag, state.LastModified
		if _, present := response.Header["Etag"]; present {
			result.etag = responseETag(response.Header)
		}
		if _, present := response.Header["Last-Modified"]; present {
			result.lastModified = responseModified(response.Header)
		}
	case http.StatusOK:
		raw, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
		if err != nil {
			return result, err
		}
		if len(raw) > responseLimit {
			return result, errors.New("更新检查响应过大")
		}
		var payload struct {
			TagName    string `json:"tag_name"`
			URL        string `json:"html_url"`
			Draft      *bool  `json:"draft"`
			Prerelease *bool  `json:"prerelease"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		if err := decoder.Decode(&payload); err != nil {
			return result, err
		}
		release := Release{Version: payload.TagName, URL: payload.URL}
		if decoder.Decode(new(any)) != io.EOF || payload.Draft == nil || *payload.Draft || payload.Prerelease == nil || *payload.Prerelease || !validRelease(release) {
			return result, errors.New("更新检查发布信息无效")
		}
		result.release, result.etag, result.lastModified = &release, responseETag(response.Header), responseModified(response.Header)
	default:
		return result, errors.New("更新检查HTTP状态无效")
	}
	return result, nil
}

func fullVersion(version string) string {
	version = "v" + strings.TrimPrefix(version, "v")
	core, _, _ := strings.Cut(version, "-")
	core, _, _ = strings.Cut(core, "+")
	if strings.Count(core, ".") != 2 || !semver.IsValid(version) {
		return ""
	}
	return version
}

func validRelease(release Release) bool {
	version := fullVersion(release.Version)
	if version == "" || semver.Prerelease(version) != "" {
		return false
	}
	address, err := url.Parse(release.URL)
	return err == nil && address.Scheme == "https" && address.Host == "github.com" && address.User == nil && address.RawQuery == "" && !address.ForceQuery && address.Fragment == "" && path.Clean(address.Path) == address.Path && strings.HasPrefix(address.Path, "/Seaony/Kakatta/releases/") && len(address.Path) > len("/Seaony/Kakatta/releases/")
}

func validETag(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	value = strings.TrimPrefix(value, "W/")
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return false
	}
	for _, ch := range []byte(value[1 : len(value)-1]) {
		if ch != 0x21 && !(ch >= 0x23 && ch <= 0x7e) && ch < 0x80 {
			return false
		}
	}
	return true
}

func responseETag(headers http.Header) string {
	values := headers.Values("ETag")
	if len(values) != 1 || !validETag(values[0]) {
		return ""
	}
	return values[0]
}

func responseModified(headers http.Header) string {
	if value := headers.Get("Last-Modified"); value != "" {
		if _, err := http.ParseTime(value); err == nil {
			return value
		}
	}
	return ""
}

func responseWait(headers http.Header, now int64) *int64 {
	var until int64
	if value := headers.Get("Retry-After"); value != "" {
		if date, err := http.ParseTime(value); err == nil {
			until = date.UnixMilli()
		} else {
			until = secondsAfter(value, now)
		}
	}
	if headers.Get("X-RateLimit-Remaining") == "0" {
		if seconds, err := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64); err == nil && seconds > 0 && seconds <= math.MaxInt64/1000 {
			until = max(until, seconds*1000)
		}
	}
	until = max(until, secondsAfter(headers.Get("X-Poll-Interval"), now))
	if until <= now {
		return nil
	}
	return &until
}

func secondsAfter(raw string, now int64) int64 {
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seconds < 0 || seconds > (math.MaxInt64-now)/1000 {
		return 0
	}
	return now + seconds*1000
}
