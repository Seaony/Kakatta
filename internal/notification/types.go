// Package notification 负责通知判定、格式化、渠道排队和送达记录。
package notification

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/notify"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/service"
)

type Dependencies struct {
	DB                    *sql.DB
	Settings              *appsettings.Store
	Cipher                *security.Cipher
	Sender                *notify.Sender
	Now                   func() time.Time
	Publish               func(kind string, reason *locales.Message, data json.RawMessage) error
	VerificationHeldUntil func(accountID int64, nowMS int64) *int64
}

type Service struct {
	db                    *sql.DB
	settings              *appsettings.Store
	cipher                *security.Cipher
	sender                *notify.Sender
	now                   func() time.Time
	publish               func(string, *locales.Message, json.RawMessage) error
	verificationHeldUntil func(int64, int64) *int64

	mu             sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	closed         bool
	storageErr     error
	activeJobs     int
	jobsIdle       chan struct{}
	paused         int
	intentInFlight map[int64]struct{}
	eventInFlight  map[int64]struct{}
	queues         channelQueues
	channelJobs    map[channelIdentity]*channelJobs
	receipts       []func(context.Context) error
	settlementGate chan struct{}
}

type channelJobs struct {
	blocked bool
	active  map[*channelJob]struct{}
	idle    chan struct{}
}

type channelJob struct{ cancel context.CancelFunc }

type Channel struct {
	ID         int64            `json:"id"`
	Type       notify.Type      `json:"type"`
	Name       string           `json:"name"`
	Enabled    bool             `json:"enabled"`
	Config     json.RawMessage  `json:"config"`
	SecretSet  map[string]bool  `json:"secret_set"`
	LastTestAt *int64           `json:"last_test_at"`
	LastTestOK *bool            `json:"last_test_ok"`
	LastError  *locales.Message `json:"last_error"`
}

type ChannelPatch map[string]json.RawMessage

type ChannelDeleteResult struct {
	ID              int64   `json:"id"`
	AffectedTaskIDs []int64 `json:"affected_task_ids"`
}

type ChannelTestResult struct {
	ChannelID  int64            `json:"channel_id"`
	DeliveryID int64            `json:"delivery_id"`
	At         int64            `json:"at"`
	OK         bool             `json:"ok"`
	Error      *locales.Message `json:"error"`
}

type DeliveryItem struct {
	FindingID int64   `json:"finding_id"`
	TaskID    *int64  `json:"task_id"`
	Title     *string `json:"title"`
}

type channelIdentity struct {
	ID   int64
	UUID string
}

type channelRecord struct {
	Channel
	UUID      string
	SecretEnc []byte
}

type channelQueues struct {
	mu        sync.Mutex
	lanes     map[channelIdentity]*channelLane
	startedAt time.Time
	now       func() time.Time
	waitFor   func(context.Context, time.Duration) error
}

type channelLane struct {
	permit         chan struct{}
	lastFinishedAt time.Time
	lastGap        time.Duration
	users          int
}

type channelTurn struct {
	queues   *channelQueues
	identity channelIdentity
	lane     *channelLane
}

type intentKind string

type intentState string

const (
	intentRecommended  intentKind = "recommended"
	intentNew          intentKind = "new"
	intentPriceDrop    intentKind = "price_drop"
	intentPriceUpgrade intentKind = "price_upgrade"

	intentPending   intentState = "pending"
	intentSent      intentState = "sent"
	intentAbandoned intentState = "abandoned"
	intentCancelled intentState = "cancelled"
)

type intent struct {
	ID            int64
	FindingID     int64
	TaskID        int64
	Kind          intentKind
	State         intentState
	CreatedAt     int64
	UpdatedAt     int64
	Price         *int64
	PreviousPrice *int64
	FailureCount  int64
	FirstFailedAt *int64
	SentAt        *int64
	Reason        *locales.Message
}

// 这里只组合共用实体，通知层不再维护一套任务和发现字段。
type candidate struct {
	Intent           intent
	Finding          service.Finding
	Listing          service.Listing
	ItemSpecs        *service.TaskItemSpecs
	Seller           *service.SellerProfile
	SellerBlocked    bool
	ReanalysisOrigin bool
}

type messageGroup struct {
	Members []candidate
	Summary bool
}

type formatOptions struct {
	Language       locales.Language
	Location       *time.Location
	LinkTarget     string
	FindingURL     func(int64) string
	TaskResultsURL string
	PublicURL      string
}

type titleParts struct {
	TaskName    string
	Action      locales.Message
	Score       *int64
	SpecText    string
	AmountCents *int64
}

type firstDecision struct {
	Apply               bool
	Kind                *string
	SkipReason          *locales.Message
	SetRecommendedPrice bool
	RecommendedPrice    *int64
}

type channelOutcome struct {
	ChannelID    int64
	ChannelName  string
	IntentIDs    []int64
	Attempted    bool
	CalledSender bool
	StartedAt    int64
	EndedAt      int64
	Result       notify.Result
}
