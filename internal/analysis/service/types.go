// Package modelservice 保存模型配置、用量和提示词，复用同一个请求客户端。
package modelservice

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/analysis/prompts"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/security"
)

type Dependencies struct {
	DB       *sql.DB
	Settings *appsettings.Store
	Cipher   *security.Cipher
	Now      func() time.Time
	Publish  func(kind string, reason *locales.Message, data json.RawMessage) error
	System   func(context.Context, int64) error // 事件提交后调用既有系统通知发送器。
}

// API 只返回安全视图；请求用的解密配置留在 client.Repository 内。
type API interface {
	Complete(context.Context, client.Request) (client.Result, error)
	ListProviders(context.Context) ([]ProviderView, error)
	GetProvider(context.Context, int64) (ProviderView, error)
	CreateProvider(context.Context, ProviderInput) (ProviderSaveResult, error)
	UpdateProvider(context.Context, int64, ProviderInput) (ProviderSaveResult, error)
	SetProviderEnabled(context.Context, int64, bool) (ProviderView, error)
	ReorderProviders(context.Context, []int64) ([]ProviderView, error)
	DeleteProvider(context.Context, int64) error
	ListPrompts(context.Context) ([]PromptView, error)
	SavePrompt(context.Context, client.Kind, string) (PromptView, error)
	RestorePrompt(context.Context, client.Kind) (PromptView, error)
	PromptSnapshot(context.Context) (prompts.Snapshot, error)
	GenerateProfile(context.Context, GenerateProfileInput) (GeneratedDraft, error)
	TestProvider(context.Context, int64) (TrialReport, error)
	BudgetStatus(context.Context) (BudgetStatus, error)
	Usage(context.Context, UsageQuery) (UsageReport, error)
	ProbeDue(context.Context) error
	Close(context.Context) error
}

// 同一应用实例共享这些适配器，不能通过重建 HTTP 客户端清掉记账故障。
type Adapters interface {
	client.Repository
	client.Accounting
	client.Slots
}

type ProviderView struct {
	ID          int64            `json:"id"`
	Name        string           `json:"name"`
	Preset      *string          `json:"preset"`
	BaseURL     string           `json:"base_url"`
	VisionModel string           `json:"vision_model"`
	TextModel   *string          `json:"text_model"`
	APIKeySet   bool             `json:"api_key_set"`
	Enabled     bool             `json:"enabled"`
	SortIndex   int64            `json:"sort_index"`
	ErrorState  *locales.Message `json:"error_state"`
	ErrorAt     *int64           `json:"error_at"`
}

type ProviderInput struct {
	Name        string  `json:"name"`
	Preset      *string `json:"preset"`
	BaseURL     string  `json:"base_url"`
	VisionModel string  `json:"vision_model"`
	TextModel   *string `json:"text_model"`
	APIKey      *string `json:"api_key"` // nil 保留；非 nil 整体替换，包括明确清空。
	Enabled     bool    `json:"enabled"`
}

type ProviderSaveResult struct {
	Provider ProviderView      `json:"provider"`
	Warnings []locales.Message `json:"warnings"`
}

type PromptView struct {
	Kind         client.Kind    `json:"kind"`
	Content      string         `json:"content"`
	Default      string         `json:"default"`
	Customized   bool           `json:"customized"`
	RequiredKeys []string       `json:"required_keys"`
	Schema       *client.Schema `json:"schema"`
	AssetVersion int            `json:"asset_version"`
	UpdatedAt    *int64         `json:"updated_at"`
}

type GenerateProfileInput struct {
	TaskID      *int64           `json:"task_id"`
	Keyword     string           `json:"keyword"`
	Requirement string           `json:"requirement"`
	Current     analysis.Profile `json:"current"`
}

type GeneratedDraft struct {
	Profile     analysis.Profile  `json:"profile"`
	Suggestions SuggestionsView   `json:"suggestions"`
	Warnings    []locales.Message `json:"warnings"`
	Request     client.Result     `json:"-"`
}

type SuggestionsView struct {
	BudgetMin    *int64   `json:"budget_min"`
	BudgetMax    *int64   `json:"budget_max"`
	Keyword      *string  `json:"keyword"`
	ExcludeWords []string `json:"exclude_words"`
}

type TrialStepState string

const (
	TrialPassed TrialStepState = "passed"
	TrialFailed TrialStepState = "failed"
	TrialNotRun TrialStepState = "not_run"
)

type TrialReport struct {
	Passed bool             `json:"passed"`
	Reason *locales.Message `json:"reason"`
	Text   TrialStepReport  `json:"text"`
	Vision TrialStepReport  `json:"vision"`
}

type TrialStepReport struct {
	State           TrialStepState    `json:"state"`
	Reason          *locales.Message  `json:"reason"`
	Model           *string           `json:"model"`
	JSONMode        *client.JSONMode  `json:"json_mode"`
	Thinking        *bool             `json:"thinking"`
	InputTokens     int64             `json:"input_tokens"`
	OutputTokens    int64             `json:"output_tokens"`
	ReasoningTokens *int64            `json:"reasoning_tokens"`
	UsageIncomplete bool              `json:"usage_incomplete"`
	DurationMS      *int64            `json:"duration_ms"`
	HTTPStatus      *int              `json:"http_status"`
	Warnings        []locales.Message `json:"warnings"`
}

type BudgetStatus struct {
	Limit          int64  `json:"limit"`
	ProtectedUsed  int64  `json:"protected_used"`
	ProtectedStart int64  `json:"protected_start"`
	EffectiveNow   int64  `json:"effective_now"`
	NextCheckAt    *int64 `json:"next_check_at"`
	Blocked        bool   `json:"blocked"`
}

type UsageWindow string

const (
	UsageToday  UsageWindow = "today"
	Usage7Days  UsageWindow = "7d"
	Usage30Days UsageWindow = "30d"
)

type UsageQuery struct {
	Window  UsageWindow `json:"window"`
	TaskID  *int64      `json:"task_id"`
	Purpose *string     `json:"purpose"`
}

type UsageTotal struct {
	Calls             int64    `json:"calls"`
	OKCalls           int64    `json:"ok_calls"`
	DurationMS        int64    `json:"duration_ms"`
	InputTokens       int64    `json:"input_tokens"`
	OutputTokens      int64    `json:"output_tokens"`
	ReasoningTokens   *int64   `json:"reasoning_tokens"`
	MissingUsageCalls int64    `json:"missing_usage_calls"`
	SuccessRate       *float64 `json:"success_rate"`
	AverageDurationMS *float64 `json:"average_duration_ms"`
}

type TaskUsage struct {
	TaskID   *int64     `json:"task_id"`
	TaskName *string    `json:"task_name"`
	Deleted  bool       `json:"deleted"`
	Total    UsageTotal `json:"total"`
}

type PurposeUsage struct {
	Purpose string     `json:"purpose"`
	Total   UsageTotal `json:"total"`
}

type UsageReport struct {
	From         int64          `json:"from"`
	Until        int64          `json:"until"`
	Timezone     string         `json:"timezone"`
	RetainedFrom *int64         `json:"retained_from"`
	Incomplete   bool           `json:"incomplete"`
	Total        UsageTotal     `json:"total"`
	ByTask       []TaskUsage    `json:"by_task"`
	ByPurpose    []PurposeUsage `json:"by_purpose"`
}
