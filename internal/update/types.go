// Package update 保存发布检查结果，由服务入口安排检查时间。
package update

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
)

type Dependencies struct {
	DB             *sql.DB
	Settings       *appsettings.Store
	CurrentVersion string
	Now            func() time.Time
	HTTP           *http.Client
}

type Release struct {
	Version string `json:"version"`
	URL     string `json:"url"`
}

type Status struct {
	CurrentVersion  string           `json:"current_version"`
	Enabled         bool             `json:"enabled"`
	LastAttemptAt   *int64           `json:"last_attempt_at"`
	LastSuccessAt   *int64           `json:"last_success_at"`
	NextCheckAt     *int64           `json:"next_check_at"`
	KnownRelease    *Release         `json:"known_release"`
	UpdateAvailable *bool            `json:"update_available"`
	Stale           bool             `json:"stale"`
	Error           *locales.Message `json:"error"`
}

type API interface {
	Read(context.Context) (Status, error)
	CheckDue(context.Context) error
}
