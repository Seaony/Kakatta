// Package service 共用业务数据和动作，不依赖 HTTP 传输层。
package service

import (
	"encoding/json"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/remote"
	"github.com/Seaony/Kakatta/internal/settings"
)

// 时间用 UTC 毫秒，金额用整数分；未知字段保留 null，读取端把列表初始化为空数组。
type TaskFields struct {
	Name              string           `json:"name"`
	Keyword           string           `json:"keyword"`
	Enabled           bool             `json:"enabled"`
	AccountID         *int64           `json:"account_id"`
	AccountPinned     bool             `json:"account_pinned"`
	MinPrice          *int64           `json:"min_price"`
	MaxPrice          *int64           `json:"max_price"`
	PersonalOnly      bool             `json:"personal_only"`
	FreeShippingOnly  bool             `json:"free_shipping_only"`
	PublishWindow     string           `json:"publish_window"`
	RegionProvince    *string          `json:"region_province"`
	RegionCity        *string          `json:"region_city"`
	RegionDistrict    *string          `json:"region_district"`
	MaxPages          int64            `json:"max_pages"`
	IntervalMinutes   int64            `json:"interval_minutes"`
	CrawlOverrides    settings.Values  `json:"crawl_overrides"`
	JudgeMode         analysis.Mode    `json:"judge_mode"`
	Requirement       string           `json:"requirement"`
	Profile           analysis.Profile `json:"profile"`
	BudgetMin         *int64           `json:"budget_min"`
	BudgetMax         *int64           `json:"budget_max"`
	MatchKeywords     []string         `json:"match_keywords"`
	ExcludeKeywords   []string         `json:"exclude_keywords"`
	AnalyzeOnFirstRun bool             `json:"analyze_on_first_run"`
	NotifyPolicy      string           `json:"notify_policy"`
	NotifyPriceDrop   bool             `json:"notify_price_drop"`
	ChannelIDs        []int64          `json:"channel_ids"`
}

type Task struct {
	ID int64 `json:"id"`
	TaskFields
	SearchGeneration      int64            `json:"search_generation"`
	BaselinePending       bool             `json:"baseline_pending"`
	SortIndex             int64            `json:"sort_index"`
	CreatedAt             *int64           `json:"created_at"`
	UpdatedAt             *int64           `json:"updated_at"`
	LastRunAt             *int64           `json:"last_run_at"`
	LastSuccessAt         *int64           `json:"last_success_at"`
	NextRunAt             *int64           `json:"next_run_at"`
	LastRunOutcome        *string          `json:"last_run_outcome"`
	LastError             *locales.Message `json:"last_error"`
	ConsecutiveFailures   int64            `json:"consecutive_failures"`
	FailurePausedUntil    *int64           `json:"failure_paused_until"`
	NoGoodHintDismissedAt *int64           `json:"no_good_hint_dismissed_at"`
}

// Patch 保留字段是否出现，null 能否清空由领域校验决定。
type TaskPatch map[string]json.RawMessage

type AccountPatch map[string]json.RawMessage

type ProxyPatch map[string]json.RawMessage

type TaskView struct {
	Task
	Runtime       TaskRuntime      `json:"runtime"`
	FindingCounts map[string]int64 `json:"finding_counts"`
	NoGoodHint    *NoGoodHint      `json:"no_good_hint"`
}

type NoGoodHint struct {
	SampleCount      int64                  `json:"sample_count"`
	Reasons          []analysis.ReasonCount `json:"reasons"`
	LatestAnalyzedAt int64                  `json:"latest_analyzed_at"`
}

type TaskSaveResult struct {
	Task     Task             `json:"task"`
	Effects  TaskSaveEffects  `json:"effects"`
	Warnings []settings.Issue `json:"warnings"`
}

type TaskSaveEffects struct {
	SearchReset               bool     `json:"search_reset"`
	Generation                int64    `json:"generation"`
	RescoredCount             int64    `json:"rescored_count"`
	NeedsReanalysisCount      int64    `json:"needs_reanalysis_count"`
	NeedsReanalysisIDs        []int64  `json:"needs_reanalysis_ids"`
	TitleRejectedCount        int64    `json:"title_rejected_count"`
	TitleRejectedIDs          []int64  `json:"title_rejected_ids"`
	AddedExcludeWords         []string `json:"added_exclude_words"`
	ExistingExcludeMatchCount int64    `json:"existing_exclude_match_count"`
}

type TaskSummary struct {
	ID        int64            `json:"id"`
	Name      string           `json:"name"`
	JudgeMode analysis.Mode    `json:"judge_mode"`
	Profile   analysis.Profile `json:"profile"`
	BudgetMin *int64           `json:"budget_min"`
	BudgetMax *int64           `json:"budget_max"`
}

type Listing struct {
	ItemID                 string   `json:"item_id"`
	Title                  string   `json:"title"`
	DesktopURL             string   `json:"desktop_url"`
	FirstSeenAt            int64    `json:"first_seen_at"`
	LastSeenAt             int64    `json:"last_seen_at"`
	Price                  *int64   `json:"price"`
	PriceText              *string  `json:"price_text"`
	OriginalPriceText      *string  `json:"original_price_text"`
	ThumbnailURL           *string  `json:"thumbnail_url"`
	Area                   *string  `json:"area"`
	SellerID               *string  `json:"seller_id"`
	SellerNick             *string  `json:"seller_nick"`
	Description            *string  `json:"description"`
	ZhimaLevel             *string  `json:"zhima_level"`
	SaleStatus             *string  `json:"sale_status"`
	ImageURLs              []string `json:"image_urls"`
	Tags                   []string `json:"tags"`
	PublishedAt            *int64   `json:"published_at"`
	WantCount              *int64   `json:"want_count"`
	BrowseCount            *int64   `json:"browse_count"`
	SellerRegistrationDays *int64   `json:"seller_registration_days"`
	DetailFetchedAt        *int64   `json:"detail_fetched_at"`
	IsFavorite             bool     `json:"is_favorite"`
	IsDismissed            bool     `json:"is_dismissed"`
	FavoritedAt            *int64   `json:"favorited_at"`
	LastDropAmount         *int64   `json:"last_drop_amount"`
	LastDropAt             *int64   `json:"last_drop_at"`
}

type ListingPatch map[string]json.RawMessage

type FindingStage string

const (
	StageTitle  FindingStage = "title"
	StageDetail FindingStage = "detail"
	StageDone   FindingStage = "done"
)

const (
	DecisionPending analysis.Decision = "pending"
	DecisionFailed  analysis.Decision = "failed"
	DecisionSkipped analysis.Decision = "skipped"
)

type Finding struct {
	ID                    int64                    `json:"id"`
	TaskID                int64                    `json:"task_id"`
	ItemID                string                   `json:"item_id"`
	SearchGeneration      int64                    `json:"search_generation"`
	ProcessingRevision    int64                    `json:"-"`
	FoundAt               int64                    `json:"found_at"`
	IsBaseline            bool                     `json:"is_baseline"`
	Decision              analysis.Decision        `json:"decision"`
	Stage                 FindingStage             `json:"stage"`
	RejectKind            *analysis.RejectKind     `json:"reject_kind"`
	DecidedLayer          *int64                   `json:"decided_layer"`
	Reason                *locales.Message         `json:"reason"`
	Analysis              *analysis.Facts          `json:"analysis"`
	AnalysisWarnings      []locales.Message        `json:"analysis_warnings"`
	UserOverrides         analysis.UserOverrides   `json:"user_overrides"`
	Score                 *int64                   `json:"score"`
	Grade                 *analysis.Grade          `json:"grade"`
	ScoreBreakdown        *analysis.ScoreBreakdown `json:"score_breakdown"`
	FairPrice             *int64                   `json:"fair_price"`
	FairSample            *int64                   `json:"fair_sample"`
	FairBasis             *locales.Message         `json:"fair_basis"`
	ScoredPrice           *int64                   `json:"scored_price"`
	ScoredAt              *int64                   `json:"scored_at"`
	Summary               *string                  `json:"summary"`
	TitleScreenReason     *string                  `json:"title_screen_reason"`
	AnalysisProviderID    *int64                   `json:"analysis_provider_id"`
	AnalysisModel         *string                  `json:"analysis_model"`
	AnalyzedAt            *int64                   `json:"analyzed_at"`
	SellerType            *analysis.SellerType     `json:"seller_type"`
	SellerAssessment      *analysis.SellerResult   `json:"seller_assessment"`
	MatchedKeywords       []string                 `json:"matched_keywords"`
	Attempts              int64                    `json:"attempts"`
	Retryable             bool                     `json:"retryable"`
	ReanalyzeRequestedAt  *int64                   `json:"reanalyze_requested_at"`
	IsRead                bool                     `json:"is_read"`
	NotifyKind            *string                  `json:"notify_kind"`
	NotifySkipReason      *locales.Message         `json:"notify_skip_reason"`
	NotifiedAt            *int64                   `json:"notified_at"`
	RecommendedPrice      *int64                   `json:"recommended_price"`
	LastDropNotifiedPrice *int64                   `json:"last_drop_notified_price"`
	DropNotifiedAt        *int64                   `json:"drop_notified_at"`
}

type FindingView struct {
	Finding
	Listing             Listing                    `json:"listing"`
	Task                TaskSummary                `json:"task"`
	ItemSpecs           *TaskItemSpecs             `json:"item_specs"`
	Seller              *SellerProfile             `json:"seller"`
	Deliveries          []DeliverySummary          `json:"deliveries"`
	PriceDropDeliveries []DeliverySummary          `json:"price_drop_deliveries,omitempty"`
	NotificationStatus  *FindingNotificationStatus `json:"notification_status,omitempty"`
	Questions           []analysis.ContactQuestion `json:"questions"`
	Checks              []analysis.EffectiveCheck  `json:"checks"`
	MarketComparison    *FindingMarketComparison   `json:"market_comparison"`
	PreviousID          *int64                     `json:"previous_id"`
	NextID              *int64                     `json:"next_id"`
}

type FindingNotificationStatus struct {
	State  string           `json:"state"`
	Reason *locales.Message `json:"reason"`
}

type FindingMarketComparison struct {
	AsOf           int64                    `json:"as_of"`
	ConfigKey      string                   `json:"config_key"`
	BrandNew       bool                     `json:"brand_new"`
	Fair           analysis.FairPrice       `json:"fair"`
	AdjacentPrices []analysis.AdjacentPrice `json:"adjacent_prices"`
}

// 三种结论只取一种，评分由当前任务和资料重新计算。
type Conclusion struct {
	RequestProfile analysis.Profile
	Facts          *analysis.Facts
	Warnings       []locales.Message
	Layer          *analysis.LayerResult
	Skipped        *locales.Message
	ProviderID     *int64
	Model          *string
}

type ListingView struct {
	Listing
	Seller   *SellerProfile `json:"seller"`
	Findings []FindingView  `json:"findings"`
	Prices   []PricePoint   `json:"prices"`
}

type TaskItemSpecs struct {
	TaskID    int64  `json:"task_id"`
	ItemID    string `json:"item_id"`
	UpdatedAt int64  `json:"updated_at"`
	analysis.ItemSpecs
}

type PricePoint struct {
	ID         int64  `json:"id"`
	ItemID     string `json:"item_id"`
	Price      int64  `json:"price"`
	ObservedAt int64  `json:"observed_at"`
}

type PriceChange struct {
	ItemID     string `json:"item_id"`
	Previous   *int64 `json:"previous"`
	Current    int64  `json:"current"`
	ObservedAt int64  `json:"observed_at"`
}

// 搜索和详情分开落盘，搜索缺字段时不能擦掉详情缓存和用户标记。
type SearchObservation struct {
	ItemID            string   `json:"item_id"`
	Title             string   `json:"title"`
	DesktopURL        string   `json:"desktop_url"`
	Price             *int64   `json:"price"`
	PriceText         *string  `json:"price_text"`
	OriginalPriceText *string  `json:"original_price_text"`
	ThumbnailURL      *string  `json:"thumbnail_url"`
	Area              *string  `json:"area"`
	SellerNick        *string  `json:"seller_nick"`
	PublishedAt       *int64   `json:"published_at"`
	WantCount         *int64   `json:"want_count"`
	Tags              []string `json:"tags"`
	FreeShipping      bool     `json:"free_shipping"`
	Inspected         bool     `json:"inspected"`
	ObservedAt        int64    `json:"observed_at"`
}

type DetailObservation struct {
	ItemID                 string   `json:"item_id"`
	ImageURLs              []string `json:"image_urls"`
	Description            *string  `json:"description"`
	WantCount              *int64   `json:"want_count"`
	BrowseCount            *int64   `json:"browse_count"`
	SellerID               *string  `json:"seller_id"`
	SellerRegistrationDays *int64   `json:"seller_registration_days"`
	ZhimaLevel             *string  `json:"zhima_level"`
	SaleStatus             *string  `json:"sale_status"`
	FetchedAt              int64    `json:"fetched_at"`
}

type SearchSaved struct {
	NewItemIDs          []string      `json:"new_item_ids"`
	NewFindingIDs       []int64       `json:"new_finding_ids"`
	TitleOnlyFindingIDs []int64       `json:"title_only_finding_ids"`
	PriceChanges        []PriceChange `json:"price_changes"`
	ScannedCount        int64         `json:"scanned_count"`
	NewCount            int64         `json:"new_count"`
	Layer1Rejected      int64         `json:"layer1_rejected"`
}

type SellerProfile struct {
	SellerID          string             `json:"seller_id"`
	Nick              *string            `json:"nick"`
	AvatarURL         *string            `json:"avatar_url"`
	Introduction      *string            `json:"introduction"`
	SellerCredit      *string            `json:"seller_credit"`
	BuyerCredit       *string            `json:"buyer_credit"`
	SellerCreditLevel *string            `json:"seller_credit_level"`
	BuyerCreditLevel  *string            `json:"buyer_credit_level"`
	ItemCount         *int64             `json:"item_count"`
	RateCount         *int64             `json:"rate_count"`
	OnSaleCount       *int64             `json:"on_sale_count"`
	SoldCount         *int64             `json:"sold_count"`
	SellerGoodRate    *float64           `json:"seller_good_rate"`
	BuyerGoodRate     *float64           `json:"buyer_good_rate"`
	SellerGoodCount   *int64             `json:"seller_good_count"`
	SellerRateCount   *int64             `json:"seller_rate_count"`
	BuyerGoodCount    *int64             `json:"buyer_good_count"`
	BuyerRateCount    *int64             `json:"buyer_rate_count"`
	RecentItems       []SellerRecentItem `json:"recent_items"`
	RecentRates       []SellerRecentRate `json:"recent_rates"`
	FetchedAt         int64              `json:"fetched_at"`
}

type SellerRecentItem struct {
	Title        string  `json:"title"`
	Price        *int64  `json:"price"`
	ThumbnailURL *string `json:"thumbnail_url"`
	SaleStatus   *string `json:"sale_status"`
}

type SellerRecentRate struct {
	Role *string `json:"role"`
	Kind *string `json:"kind"`
	Text *string `json:"text"`
}

type BlockedSeller struct {
	SellerID  string  `json:"seller_id"`
	Nick      *string `json:"nick"`
	CreatedAt *int64  `json:"created_at"`
}

type DeliverySummary struct {
	ID          int64            `json:"id"`
	At          *int64           `json:"at"`
	ChannelID   *int64           `json:"channel_id"`
	ChannelName *string          `json:"channel_name"`
	TaskID      *int64           `json:"task_id"`
	Kind        *string          `json:"kind"`
	ItemCount   *int64           `json:"item_count"`
	OK          *bool            `json:"ok"`
	Error       *locales.Message `json:"error"`
}

type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
	Total      int64   `json:"total"`
}

type ListQuery struct {
	Limit  int64  `json:"limit"`
	Cursor string `json:"cursor"`
}

type TaskQuery struct {
	ListQuery
	Q       string `json:"q"`
	Enabled *bool  `json:"enabled"`
}

type FindingQuery struct {
	ListQuery
	TaskID     *int64             `json:"task_id"`
	View       string             `json:"view"`
	Decision   *analysis.Decision `json:"decision"`
	PriceDrop  *bool              `json:"price_drop"`
	OverBudget *bool              `json:"over_budget"`
	Q          string             `json:"q"`
	Sort       string             `json:"sort"`
}

type ListingQuery struct {
	ListQuery
	Favorite  *bool  `json:"favorite"`
	Dismissed *bool  `json:"dismissed"`
	Q         string `json:"q"`
}

type RunQuery struct {
	ListQuery
	TaskID      *int64       `json:"task_id"`
	Outcome     *RunOutcome  `json:"outcome"`
	FailureKind *FailureKind `json:"failure_kind"`
	Q           string       `json:"q"`
	FromAt      *int64       `json:"from_at"`
	ToAt        *int64       `json:"to_at"`
}

type SystemEventQuery struct {
	ListQuery
	Resolved *bool `json:"resolved"`
}

type MarketSampleQuery struct {
	ListQuery
	ConfigKey string `json:"config_key"`
	BrandNew  *bool  `json:"brand_new"`
}

type OverviewQuery struct {
	Period string `json:"period"`
}

type BatchStatus string

const (
	BatchApplied BatchStatus = "applied"
	BatchSkipped BatchStatus = "skipped"
)

type BatchItem struct {
	ID     int64            `json:"id"`
	Status BatchStatus      `json:"status"`
	Reason *locales.Message `json:"reason"`
}

type BatchResult struct {
	Requested int64       `json:"requested"`
	Applied   int64       `json:"applied"`
	Skipped   int64       `json:"skipped"`
	Items     []BatchItem `json:"items"`
}

type MutationResult struct {
	Applied int64 `json:"applied"`
}

type ReanalysisPreview struct {
	AsOf              int64 `json:"as_of"`
	RequestedCount    int64 `json:"requested_count"`
	NeedsDetailCount  int64 `json:"needs_detail_count"`
	CachedDetailCount int64 `json:"cached_detail_count"`
}

type ReanalysisStatus string

const (
	ReanalysisRejected ReanalysisStatus = "rejected"
	ReanalysisQueued   ReanalysisStatus = "queued"
	ReanalysisCached   ReanalysisStatus = "cached"
)

type ReanalysisItem struct {
	FindingID   int64            `json:"finding_id"`
	Status      ReanalysisStatus `json:"status"`
	Reason      *locales.Message `json:"reason"`
	NeedsDetail bool             `json:"needs_detail"`
}

type ReanalysisResult struct {
	Items            []ReanalysisItem `json:"items"`
	NeedsDetailCount int64            `json:"needs_detail_count"`
}

type ItemSpecsUpdateResult struct {
	ItemSpecs     TaskItemSpecs `json:"item_specs"`
	RescoredCount int64         `json:"rescored_count"`
}

type AccountState string

const (
	AccountSignedOut         AccountState = "signed_out"
	AccountActive            AccountState = "active"
	AccountCooling           AccountState = "cooling"
	AccountNeedsVerification AccountState = "needs_verification"
	AccountDenied            AccountState = "denied"
	AccountExpired           AccountState = "expired"
)

type Account struct {
	ID                 int64            `json:"id"`
	Name               string           `json:"name"`
	ProxyID            *int64           `json:"proxy_id"`
	Nickname           *string          `json:"nickname"`
	AvatarURL          *string          `json:"avatar_url"`
	State              AccountState     `json:"state"`
	StateReason        *locales.Message `json:"state_reason"`
	StateChangedAt     *int64           `json:"state_changed_at"`
	CooldownUntil      *int64           `json:"cooldown_until"`
	VerifyLevel        int64            `json:"verify_level"`
	VerifyURL          *string          `json:"-"`
	LastCooldownEndAt  *int64           `json:"last_cooldown_end_at"`
	CooldownManualUsed bool             `json:"cooldown_manual_used"`
	LastRunEndedAt     *int64           `json:"last_run_ended_at"`
	NextGapSeconds     *int64           `json:"next_gap_seconds"`
	LastUsedAt         *int64           `json:"last_used_at"`
	CreatedAt          *int64           `json:"created_at"`
}

type AccountView struct {
	Account
	Quota             QuotaView    `json:"quota"`
	Session           *SessionView `json:"session"`
	ReloginAfterPause bool         `json:"relogin_after_pause"`
}

type QuotaView struct {
	HourlyUsed       int64 `json:"hourly_used"`
	DailySearchUsed  int64 `json:"daily_search_used"`
	DailyDetailUsed  int64 `json:"daily_detail_used"`
	HourlyLimit      int64 `json:"hourly_limit"`
	DailySearchLimit int64 `json:"daily_search_limit"`
	DailyDetailLimit int64 `json:"daily_detail_limit"`
	DayStartAt       int64 `json:"day_start_at"`
	AsOf             int64 `json:"as_of"`
}

type SessionKind string

const (
	SessionRun          SessionKind = "run"
	SessionLogin        SessionKind = "login"
	SessionVerification SessionKind = "verification"
)

type SessionPhase string

const (
	SessionQueued       SessionPhase = "queued"
	SessionOpening      SessionPhase = "opening"
	SessionRunning      SessionPhase = "running"
	SessionWaitingLogin SessionPhase = "waiting_login"
	SessionVerifyHold   SessionPhase = "verify_hold"
	SessionVerifying    SessionPhase = "verifying"
	SessionClosing      SessionPhase = "closing"
)

type SessionView struct {
	ID                  string           `json:"id"`
	AccountID           int64            `json:"account_id"`
	Kind                SessionKind      `json:"kind"`
	Phase               SessionPhase     `json:"phase"`
	RunID               *int64           `json:"run_id"`
	TaskID              *int64           `json:"task_id"`
	QueuedAt            *int64           `json:"queued_at"`
	StartedAt           *int64           `json:"started_at"`
	ExpiresAt           *int64           `json:"expires_at"`
	RemotePath          *string          `json:"remote_path"`
	Viewport            *remote.Viewport `json:"viewport"`
	RefreshVerification bool             `json:"refresh_verification,omitempty"`
}

type TaskPhase string

const (
	TaskIdle       TaskPhase = "idle"
	TaskQueued     TaskPhase = "queued"
	TaskRunning    TaskPhase = "running"
	TaskFinalizing TaskPhase = "finalizing"
)

type TaskRuntime struct {
	TaskID    int64           `json:"task_id"`
	Phase     TaskPhase       `json:"phase"`
	RunID     *int64          `json:"run_id"`
	AccountID *int64          `json:"account_id"`
	QueuedAt  *int64          `json:"queued_at"`
	StartedAt *int64          `json:"started_at"`
	Blocked   *RuntimeBlocked `json:"blocked"`
}

type RuntimeBlocked struct {
	Reason        locales.Message `json:"reason"`
	NextAllowedAt *int64          `json:"next_allowed_at"`
}

type SubmissionStatus string

const (
	SubmissionQueued  SubmissionStatus = "queued"
	SubmissionStarted SubmissionStatus = "started"
)

type RunSubmission struct {
	TaskID int64            `json:"task_id"`
	Status SubmissionStatus `json:"status"`
	RunID  *int64           `json:"run_id"`
}

type RunRef struct {
	RunID            int64 `json:"run_id"`
	TaskID           int64 `json:"task_id"`
	SearchGeneration int64 `json:"search_generation"`
}

// 同代重分析也要核版本，旧结果不能盖过新请求。
type WorkRef struct {
	RunRef
	FindingID          int64
	ProcessingRevision int64
}

type ErrorKind string

const (
	ErrorBadRequest  ErrorKind = "bad_request"
	ErrorInvalid     ErrorKind = "invalid"
	ErrorNotFound    ErrorKind = "not_found"
	ErrorConflict    ErrorKind = "conflict"
	ErrorUnavailable ErrorKind = "unavailable"
)

type ServiceError struct {
	Kind          ErrorKind        `json:"kind"`
	Reason        locales.Message  `json:"reason"`
	Fields        []settings.Issue `json:"fields"`
	NextAllowedAt *int64           `json:"next_allowed_at"`
}

type ProxyView struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
}

type ProxyTestResult struct {
	OK     bool             `json:"ok"`
	Reason *locales.Message `json:"reason"`
}

type RunTrigger string

const (
	TriggerAuto   RunTrigger = "auto"
	TriggerManual RunTrigger = "manual"
)

type RunOutcome string

const (
	RunSuccess RunOutcome = "success"
	RunFailed  RunOutcome = "failed"
	RunSkipped RunOutcome = "skipped"
)

type FailureKind string

const (
	FailureRiskVerify FailureKind = "risk_verify"
	FailureRiskDeny   FailureKind = "risk_deny"
	FailureRiskEnv    FailureKind = "risk_env"
	FailureLogin      FailureKind = "login"
	FailureNetwork    FailureKind = "network"
	FailurePage       FailureKind = "page"
	FailureTimeout    FailureKind = "timeout"
	FailureQuota      FailureKind = "quota"
	FailureConfig     FailureKind = "config"
	FailureCancelled  FailureKind = "cancelled"
	FailureRestart    FailureKind = "restart"
)

// 未结束时 outcome、failure_kind、message 为 null，不把数据库占位当最终结果。
type RunRecord struct {
	ID               int64             `json:"id"`
	TaskID           *int64            `json:"task_id"`
	TaskName         string            `json:"task_name"`
	Trigger          RunTrigger        `json:"trigger"`
	AccountID        *int64            `json:"account_id"`
	AccountName      *string           `json:"account_name"`
	ProxyName        *string           `json:"proxy_name"`
	StartedAt        *int64            `json:"started_at"`
	EndedAt          *int64            `json:"ended_at"`
	Outcome          *RunOutcome       `json:"outcome"`
	FailureKind      *FailureKind      `json:"failure_kind"`
	Message          *locales.Message  `json:"message"`
	RepeatCount      int64             `json:"repeat_count"`
	PageCount        *int64            `json:"page_count"`
	ScannedCount     *int64            `json:"scanned_count"`
	NewCount         *int64            `json:"new_count"`
	Layer1Rejected   *int64            `json:"layer1_rejected"`
	Layer2Count      *int64            `json:"layer2_count"`
	Layer2Rejected   *int64            `json:"layer2_rejected"`
	DetailCount      *int64            `json:"detail_count"`
	SellerPageCount  *int64            `json:"seller_page_count"`
	AnalyzedCount    *int64            `json:"analyzed_count"`
	RecommendedCount *int64            `json:"recommended_count"`
	NotifiedCount    *int64            `json:"notified_count"`
	Warnings         []locales.Message `json:"warnings"`
}

type RunView = RunRecord

type StepStatus string

const (
	StepOK   StepStatus = "ok"
	StepWarn StepStatus = "warn"
	StepFail StepStatus = "fail"
)

type RunStep struct {
	RunID   int64            `json:"run_id"`
	Seq     int64            `json:"seq"`
	At      *int64           `json:"at"`
	Status  *StepStatus      `json:"status"`
	Message *locales.Message `json:"message"`
	Detail  *locales.Message `json:"detail"`
}

type RunDetail struct {
	Run   RunView       `json:"run"`
	Steps Page[RunStep] `json:"steps"`
}

type SystemEventKind string

const (
	EventAccountVerify    SystemEventKind = "account_verify"
	EventAccountLogin     SystemEventKind = "account_login"
	EventAccountDeny      SystemEventKind = "account_deny"
	EventAIError          SystemEventKind = "ai_error"
	EventAIBudget         SystemEventKind = "ai_budget"
	EventTaskFailurePause SystemEventKind = "task_failure_pause"
	EventXianyuLayout     SystemEventKind = "xianyu_layout"
	EventLoginFailures    SystemEventKind = "login_failures"
)

type SystemEvent struct {
	ID         int64            `json:"id"`
	Kind       *SystemEventKind `json:"kind"`
	SubjectID  *int64           `json:"subject_id"`
	Message    *locales.Message `json:"message"`
	CreatedAt  *int64           `json:"created_at"`
	ResolvedAt *int64           `json:"resolved_at"`
	NotifiedAt *int64           `json:"notified_at"`
}

type MarketView struct {
	Market                analysis.MarketResult `json:"market"`
	Insight               *MarketInsight        `json:"insight"`
	TodayNewCount         int64                 `json:"today_new_count"`
	TodayRecommendedCount int64                 `json:"today_recommended_count"`
	PersonalSellerShare   *float64              `json:"personal_seller_share"`
}

type MarketInsight struct {
	ID          int64                  `json:"id"`
	TaskID      *int64                 `json:"task_id"`
	GeneratedAt *int64                 `json:"generated_at"`
	Stats       *analysis.MarketResult `json:"stats"`
	Text        *string                `json:"text"`
	ProviderID  *int64                 `json:"provider_id"`
	Model       *string                `json:"model"`
}

type MarketSampleView struct {
	ItemID     string  `json:"item_id"`
	Title      string  `json:"title"`
	Price      int64   `json:"price"`
	SellerID   *string `json:"seller_id"`
	ObservedAt int64   `json:"observed_at"`
	ConfigKey  string  `json:"config_key"`
	BrandNew   bool    `json:"brand_new"`
	DesktopURL string  `json:"desktop_url"`
}

type RegionNode struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Level    string  `json:"level"`
	ParentID *string `json:"parent_id"`
}

type CleanupResult struct {
	Deleted       map[string]int64  `json:"deleted"`
	ImagesDeleted int64             `json:"images_deleted"`
	Warnings      []locales.Message `json:"warnings"`
}

type OverviewView struct {
	Period            string               `json:"period"`
	Timezone          string               `json:"timezone"`
	FromAt            int64                `json:"from_at"`
	ToAt              int64                `json:"to_at"`
	AsOf              int64                `json:"as_of"`
	Coverage          CoverageView         `json:"coverage"`
	Cards             OverviewCards        `json:"cards"`
	Comparison        *OverviewComparison  `json:"comparison"`
	Activity          []ActivityBucket     `json:"activity"`
	Tasks             []TaskOverview       `json:"tasks"`
	GradeDistribution []GradeCount         `json:"grade_distribution"`
	Accounts          AccountOverview      `json:"accounts"`
	AI                AIOverview           `json:"ai"`
	Notifications     NotificationOverview `json:"notifications"`
	Exceptions        ExceptionOverview    `json:"exceptions"`
}

type CoverageView struct {
	FromAt    *int64 `json:"from_at"`
	ToAt      int64  `json:"to_at"`
	IsPartial bool   `json:"is_partial"`
}

type OverviewCards struct {
	ScannedCount            int64 `json:"scanned_count"`
	NewCount                int64 `json:"new_count"`
	RecommendedCount        int64 `json:"recommended_count"`
	RecommendedAICount      int64 `json:"recommended_ai_count"`
	RecommendedKeywordCount int64 `json:"recommended_keyword_count"`
	PriceDropCount          int64 `json:"price_drop_count"`
}

type OverviewComparison struct {
	FromAt      int64            `json:"from_at"`
	ToAt        int64            `json:"to_at"`
	Scanned     MetricComparison `json:"scanned"`
	New         MetricComparison `json:"new"`
	Recommended MetricComparison `json:"recommended"`
	PriceDrop   MetricComparison `json:"price_drop"`
}

type MetricComparison struct {
	PreviousCount int64    `json:"previous_count"`
	Delta         int64    `json:"delta"`
	Ratio         *float64 `json:"ratio"`
}

type ActivityBucket struct {
	FromAt    int64 `json:"from_at"`
	ToAt      int64 `json:"to_at"`
	NewCount  int64 `json:"new_count"`
	SeenCount int64 `json:"seen_count"`
}

type TaskOverviewState string

const (
	OverviewMonitoring TaskOverviewState = "monitoring"
	OverviewChecking   TaskOverviewState = "checking"
	OverviewError      TaskOverviewState = "error"
	OverviewPaused     TaskOverviewState = "paused"
)

type TaskOverview struct {
	TaskID             int64                 `json:"task_id"`
	Name               string                `json:"name"`
	JudgeMode          analysis.Mode         `json:"judge_mode"`
	State              TaskOverviewState     `json:"state"`
	Reason             *locales.Message      `json:"reason"`
	SuccessRate        *float64              `json:"success_rate"`
	AverageDurationMS  *float64              `json:"average_duration_ms"`
	RecommendationRate *float64              `json:"recommendation_rate"`
	NextRunAt          *int64                `json:"next_run_at"`
	ScannedCount       int64                 `json:"scanned_count"`
	NewFindingCount    int64                 `json:"new_finding_count"`
	RecommendedCount   int64                 `json:"recommended_count"`
	MinPrice           *int64                `json:"min_price"`
	Trend              []analysis.DailyPrice `json:"trend"`
}

type GradeCount struct {
	Kind  string `json:"kind"`
	Count int64  `json:"count"`
}

type AccountOverview struct {
	Total  int64 `json:"total"`
	Active int64 `json:"active"`
}

type AIOverview struct {
	Count             int64    `json:"count"`
	SuccessRate       *float64 `json:"success_rate"`
	AverageDurationMS *float64 `json:"average_duration_ms"`
	Tokens            int64    `json:"tokens"`
}

type NotificationOverview struct {
	Count       int64    `json:"count"`
	SuccessRate *float64 `json:"success_rate"`
}

type ExceptionOverview struct {
	UnresolvedEvents    []SystemEvent `json:"unresolved_events"`
	RiskFailureCount    int64         `json:"risk_failure_count"`
	NetworkFailureCount int64         `json:"network_failure_count"`
}

type RunLogOverview struct {
	Coverage          CoverageView    `json:"coverage"`
	FromAt            int64           `json:"from_at"`
	ToAt              int64           `json:"to_at"`
	AsOf              int64           `json:"as_of"`
	TotalCount        int64           `json:"total_count"`
	SuccessCount      int64           `json:"success_count"`
	FailedCount       int64           `json:"failed_count"`
	SkippedCount      int64           `json:"skipped_count"`
	SuccessRate       *float64        `json:"success_rate"`
	AverageDurationMS *float64        `json:"average_duration_ms"`
	Buckets           []RunHourBucket `json:"buckets"`
}

type RunHourBucket struct {
	FromAt       int64 `json:"from_at"`
	ToAt         int64 `json:"to_at"`
	SuccessCount int64 `json:"success_count"`
	FailedCount  int64 `json:"failed_count"`
	SkippedCount int64 `json:"skipped_count"`
}

type SystemStatus struct {
	AsOf          int64                `json:"as_of"`
	Scheduler     SchedulerStatus      `json:"scheduler"`
	Browsers      BrowserStatus        `json:"browsers"`
	Configuration []ConfigurationCheck `json:"configuration"`
	Quotas        []AccountQuotaView   `json:"quotas"`
	Resources     SystemResources      `json:"resources"`
}

type SchedulerStatus struct {
	Running      bool   `json:"running"`
	Accepting    bool   `json:"accepting"`
	NextWakeAt   *int64 `json:"next_wake_at"`
	QueuedCount  int64  `json:"queued_count"`
	RunningCount int64  `json:"running_count"`
}

type BrowserStatus struct {
	Limit    int64         `json:"limit"`
	Active   int64         `json:"active"`
	Sessions []SessionView `json:"sessions"`
}

type ConfigurationCheck struct {
	Key    string           `json:"key"`
	OK     bool             `json:"ok"`
	Reason *locales.Message `json:"reason"`
}

type AccountQuotaView struct {
	AccountID int64     `json:"account_id"`
	Name      string    `json:"name"`
	Quota     QuotaView `json:"quota"`
}

type SystemResources struct {
	DiskTotalBytes   *int64 `json:"disk_total_bytes"`
	DiskFreeBytes    *int64 `json:"disk_free_bytes"`
	MemoryTotalBytes *int64 `json:"memory_total_bytes"`
	MemoryUsedBytes  *int64 `json:"memory_used_bytes"`
}
