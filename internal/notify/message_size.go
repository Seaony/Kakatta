package notify

import "unicode/utf8"

// MessageFits 只检查已定的长度限制，不裁内容，也不代替渠道配置校验。
func MessageFits(config ChannelConfig, message Message) bool {
	switch config.Type {
	case Telegram:
		limit := TelegramTextLimit
		if message.ImageURL != "" && !message.TextOnly {
			limit = TelegramCaptionLimit
		}
		return TelegramContentUnits(message, TruncateRunes(message.ItemTitle, 40)) <= limit
	case WeCom:
		return wecomPayloadFits(buildWeComPayload(message))
	case Feishu:
		var secrets feishuSecrets
		if decodeObject(config.Secrets, &secrets, "secrets") != nil {
			return false
		}
		// 秒级时间戳覆盖 int64 的完整位数，签名仍按实际构造编码。
		body, err := buildFeishuBody(message, secrets.Secret, "-9223372036854775808")
		return err == nil && len(body) <= FeishuBodyBytes
	case ServerChan:
		return utf8.RuneCountInString(message.Title) <= ServerChanTitleRunes
	case Bark, Ntfy, Gotify, DingTalk, PushPlus, Webhook, Email:
		return true
	default:
		return false
	}
}
