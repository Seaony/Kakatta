package notify

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"
	"unicode/utf8"
)

func New(deps Dependencies) *Sender {
	client := deps.HTTP
	if client == nil {
		client = &http.Client{}
	}
	copyClient := *client
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if base, ok := transport.(*http.Transport); ok {
		copyTransport := base.Clone()
		// 旧连接丢响应时，标准库可能自动重发 GET 或带幂等头的 POST。
		copyTransport.DisableKeepAlives = true
		copyClient.Transport = copyTransport
	}
	// 重定向会隐式多发一次，还可能把带密钥的地址带到别处。
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if deps.DialContext == nil {
		deps.DialContext = (&net.Dialer{}).DialContext
	}
	if deps.TLSConfig == nil {
		deps.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Endpoints.PushPlus == "" {
		deps.Endpoints.PushPlus = "https://www.pushplus.plus/send"
	}
	if deps.Endpoints.ServerChanTurbo == "" {
		deps.Endpoints.ServerChanTurbo = "https://sctapi.ftqq.com"
	}
	if deps.Endpoints.ServerChan3 == nil {
		deps.Endpoints.ServerChan3 = func(shard string) string { return "https://" + shard + ".push.ft07.com/send" }
	}
	return &Sender{http: &copyClient, dial: deps.DialContext, tlsConfig: deps.TLSConfig.Clone(), now: deps.Now, endpoints: deps.Endpoints}
}

func Validate(config ChannelConfig) error {
	switch config.Type {
	case Bark:
		return validateBark(config)
	case Telegram:
		return validateTelegram(config)
	case Ntfy:
		return validateNtfy(config)
	case Gotify:
		return validateGotify(config)
	case WeCom:
		return validateWeCom(config)
	case DingTalk:
		return validateDingTalk(config)
	case Feishu:
		return validateFeishu(config)
	case PushPlus:
		return validatePushPlus(config)
	case ServerChan:
		return validateServerChan(config)
	case Webhook:
		return validateWebhook(config)
	case Email:
		return validateEmail(config)
	default:
		return configError("type")
	}
}

func (s *Sender) Send(ctx context.Context, config ChannelConfig, message Message) Result {
	if ctx.Err() != nil {
		return failureFor(ctx.Err())
	}
	if err := Validate(config); err != nil {
		return failure("invalid_config")
	}
	if !validMessage(message) {
		return failure("invalid_message")
	}
	switch config.Type {
	case Bark:
		return s.sendBark(ctx, config, message)
	case Telegram:
		return s.sendTelegram(ctx, config, message)
	case Ntfy:
		return s.sendNtfy(ctx, config, message)
	case Gotify:
		return s.sendGotify(ctx, config, message)
	case WeCom:
		return s.sendWeCom(ctx, config, message)
	case DingTalk:
		return s.sendDingTalk(ctx, config, message)
	case Feishu:
		return s.sendFeishu(ctx, config, message)
	case PushPlus:
		return s.sendPushPlus(ctx, config, message)
	case ServerChan:
		return s.sendServerChan(ctx, config, message)
	case Webhook:
		return s.sendWebhook(ctx, config, message)
	case Email:
		return s.sendEmail(ctx, config, message)
	default:
		return failure("invalid_config")
	}
}

func validMessage(m Message) bool {
	for _, value := range []string{m.Title, m.ItemTitle, m.Body, m.LinkLabel, m.Footer, m.Link, m.DesktopLink, m.MobileLink, m.KakattaURL, m.ImageURL, m.Price, m.Reason, m.Score, m.Grade, m.Config, m.FairPrice, m.ItemID, m.Task, m.Kind} {
		if !utf8.ValidString(value) {
			return false
		}
	}
	return true
}
