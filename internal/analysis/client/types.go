// Package client 发送模型请求，处理兼容、服务商轮换和逐次用量记录。
package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
)

var (
	ErrNoProvider     = errors.New("analysis.client.no_provider")
	ErrDailyLimit     = errors.New("analysis.client.daily_limit")
	ErrInfrastructure = errors.New("analysis.client.infrastructure")
	ErrConfigChanged  = errors.New("analysis.client.config_changed")
)

type Kind string

const (
	TitleScreen Kind = "title_screen"
	Analysis    Kind = "analysis"
	Profile     Kind = "profile"
	Market      Kind = "market"
)

type JSONMode string

const (
	JSONSchema JSONMode = "schema"
	JSONObject JSONMode = "object"
	JSONText   JSONMode = "text"
)

type Reason = locales.Message

type Schema struct {
	Name string          `json:"name"`
	JSON json.RawMessage `json:"schema"`
}

type Request struct {
	Kind     Kind               `json:"kind"`
	TaskID   *int64             `json:"task_id"`
	System   string             `json:"system"`
	User     string             `json:"user"`
	Images   []string           `json:"images"`
	Schema   *Schema            `json:"schema"`
	Validate func(string) error `json:"-"`
}

type ProviderRef struct {
	ID          int64  `json:"id"`
	ConfigToken string `json:"-"`
}

type Provider struct {
	Ref         ProviderRef `json:"ref"`
	Name        string      `json:"name"`
	BaseURL     string      `json:"base_url"`
	APIKey      string      `json:"-"`
	VisionModel string      `json:"vision_model"`
	TextModel   string      `json:"text_model"`
	Enabled     bool        `json:"enabled"`
	SortIndex   int64       `json:"sort_index"`
	ErrorState  *Reason     `json:"error_state"`
}

type CompatKey struct {
	Provider ProviderRef `json:"provider"`
	BaseURL  string      `json:"base_url"`
	Model    string      `json:"model"`
}

type Compat struct {
	JSONMode        JSONMode `json:"json_mode"`
	SendTemperature bool     `json:"send_temperature"`
	MaxTokensParam  string   `json:"max_tokens_param"`
	MaxTokensFloor  *int64   `json:"max_tokens_floor"`
}

func DefaultCompat() Compat {
	return Compat{JSONMode: JSONSchema, SendTemperature: true, MaxTokensParam: "max_tokens"}
}

type ProviderChange struct {
	Kind   string  `json:"kind"`
	Reason *Reason `json:"reason"`
}

type Repository interface {
	Providers(context.Context) ([]Provider, error)
	Provider(context.Context, int64) (Provider, error)
	LoadCompat(context.Context, CompatKey) (Compat, error)
	SaveCompat(context.Context, CompatKey, Compat) (Compat, error)
	ChangeProvider(context.Context, ProviderRef, ProviderChange) error
}

type Usage struct {
	Present         bool   `json:"present"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	ReasoningTokens *int64 `json:"reasoning_tokens"`
}

type CallStart struct {
	Provider ProviderRef `json:"provider"`
	Model    string      `json:"model"`
	Purpose  string      `json:"purpose"`
	TaskID   *int64      `json:"task_id"`
	JSONMode JSONMode    `json:"json_mode"`
}

type CallEnd struct {
	OK           bool          `json:"ok"`
	HTTPStatus   int           `json:"http_status"`
	ErrorKind    string        `json:"error_kind"`
	Usage        Usage         `json:"usage"`
	FinishReason string        `json:"finish_reason"`
	Duration     time.Duration `json:"duration"`
}

type Ticket struct{ ID int64 }

type Accounting interface {
	Before(context.Context, CallStart) (Ticket, error)
	After(context.Context, Ticket, CallEnd) error
}

type Slots interface {
	Acquire(context.Context) (release func(), err error)
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Call struct {
	Start CallStart `json:"start"`
	End   CallEnd   `json:"end"`
}

type JSONDowngrade struct {
	From     JSONMode
	To       JSONMode
	Provider string
	Model    string
}

type Result struct {
	Text           string          `json:"text"`
	ProviderID     int64           `json:"provider_id"`
	Model          string          `json:"model"`
	JSONMode       JSONMode        `json:"json_mode"`
	Thinking       bool            `json:"thinking"`
	Duration       time.Duration   `json:"duration"`
	Calls          []Call          `json:"calls"`
	JSONDowngrades []JSONDowngrade `json:"-"`
}

type Failure struct {
	Last      Reason `json:"last"`
	Retryable bool   `json:"retryable"`
}

func (e *Failure) Error() string { return e.Last.Code }

type TestStep struct {
	Result Result `json:"result"`
	Err    error  `json:"-"`
}

type TestResult struct {
	Text   TestStep `json:"text"`
	Vision TestStep `json:"vision"`
}

type Dependencies struct {
	Repository Repository
	Accounting Accounting
	Slots      Slots
	HTTP       HTTPDoer
	Timeout    time.Duration
	CompatLog  func(context.Context, CompatKey, Compat, Compat) error
	Secrets    []string // 请求代理等额外凭据，只用于遮密
}
