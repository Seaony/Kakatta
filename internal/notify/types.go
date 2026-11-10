// Package notify 按渠道协议发送一条已组装好的消息，不负责排队或重试。
package notify

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"time"
)

type Type string

const (
	Bark       Type = "bark"
	Telegram   Type = "telegram"
	Ntfy       Type = "ntfy"
	Gotify     Type = "gotify"
	WeCom      Type = "wecom"
	DingTalk   Type = "dingtalk"
	Feishu     Type = "feishu"
	PushPlus   Type = "pushplus"
	ServerChan Type = "serverchan"
	Webhook    Type = "webhook"
	Email      Type = "email"
)

type ChannelConfig struct {
	Type    Type            `json:"type"`
	Name    string          `json:"name"`
	Config  json.RawMessage `json:"-"`
	Secrets json.RawMessage `json:"-"`
}

func (ChannelConfig) String() string { return "通知渠道配置" }

type Message struct {
	Title, ItemTitle, Body, LinkLabel, Footer           string
	Link, DesktopLink, MobileLink, KakattaURL, ImageURL string
	Price, Reason, Score, Grade, Config, FairPrice      string
	ItemID, Task, Kind                                  string
	Urgent                                              bool
}

type Result struct {
	OK           bool   `json:"ok"`
	ErrorCode    string `json:"error_code"`
	HTTPStatus   int    `json:"http_status"`
	ProviderCode string `json:"provider_code"`
}

type Dependencies struct {
	HTTP        *http.Client
	DialContext func(context.Context, string, string) (net.Conn, error)
	TLSConfig   *tls.Config
	Now         func() time.Time
	Endpoints   Endpoints
}

type Endpoints struct {
	PushPlus        string
	ServerChanTurbo string
	ServerChan3     func(shard string) string
}

type Sender struct {
	http      *http.Client
	dial      func(context.Context, string, string) (net.Conn, error)
	tlsConfig *tls.Config
	now       func() time.Time
	endpoints Endpoints
}

type ConfigError struct{ Field string }

func (e *ConfigError) Error() string { return "invalid_config: " + e.Field }

const (
	TelegramCaptionLimit  = 1024
	TelegramTextLimit     = 4096
	WeComTitleBytes       = 128
	WeComDescriptionBytes = 512
	WeComTextBytes        = 2048
	FeishuBodyBytes       = 20 * 1024
	ServerChanTitleRunes  = 32
)
