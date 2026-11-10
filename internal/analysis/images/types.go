// Package images 准备模型要看的详情图，不读取浏览器 Cookie 或数据库。
package images

import (
	"net/http"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
)

const (
	SourceBytes         int64 = 20 << 20
	SourcePixels        int64 = 40_000_000
	DownloadConcurrency       = 3
	RequestTimeout            = 30 * time.Second
	MaxEdge                   = 1024
	JPEGQuality               = 80
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Limits struct {
	SourceBytes  int64 `json:"source_bytes"`
	SourcePixels int64 `json:"source_pixels"`
}

type Dependencies struct {
	HTTP   HTTPDoer // 根提供固定账号代理、无 Cookie 的传输。
	Limits Limits
}

type Image struct {
	DetailIndex int    `json:"detail_index"` // 原详情图列表下标，从 0 开始。
	DataURL     string `json:"-"`
}

type Issue struct {
	DetailIndex int             `json:"detail_index"`
	Reason      locales.Message `json:"reason"`
}

type Result struct {
	Images  []Image `json:"-"`
	Skipped []Issue `json:"skipped"`
}
