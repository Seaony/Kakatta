package notification

import (
	"strings"

	"github.com/Seaony/Kakatta/internal/notify"
)

type criticalEvidence struct {
	Name, Status, Quote string
}

type recommendationContent struct {
	FirstLine, Summary, Compact string
	Evidence                    []criticalEvidence
	ImageLink                   string
}

func (r *messageRenderer) recommendationBody(content recommendationContent, quoteCount int, compact bool, detailsURL string) string {
	parts := []string{content.FirstLine}
	if compact {
		parts = []string{content.Compact, r.text("notification.compact_summary", nil)}
		if len(content.Evidence) > 0 {
			parts = append(parts, r.text("notification.omitted_checks", map[string]any{"count": len(content.Evidence)}))
		}
	} else {
		for index, evidence := range content.Evidence {
			args := map[string]any{"name": evidence.Name, "status": evidence.Status}
			key := "notification.check_conclusion"
			if index < quoteCount {
				key, args["evidence"] = "notification.check_evidence", evidence.Quote
			}
			parts = append(parts, r.text(key, args))
		}
		if quoteCount < len(content.Evidence) {
			parts = append(parts, r.text("notification.folded_evidence", map[string]any{"count": len(content.Evidence) - quoteCount}))
		}
	}
	if compact || quoteCount < len(content.Evidence) {
		if detailsURL != "" {
			parts = append(parts, detailsURL)
		} else {
			parts = append(parts, r.text("notification.view_results", nil))
		}
	}
	if !compact {
		parts = append(parts, content.Summary)
	}
	if content.ImageLink != "" {
		parts = append(parts, r.text("notification.image_link", map[string]any{"url": content.ImageLink}))
	}
	return notificationJoin("\n", parts...)
}

func fitRecommendation(r *messageRenderer, message *notify.Message, parts titleParts, channel notify.ChannelConfig, content recommendationContent) error {
	for index, evidence := range content.Evidence {
		if runes := []rune(evidence.Quote); len(runes) > 60 {
			content.Evidence[index].Quote = string(runes[:59]) + "…"
			message.FormatLevel = 1
		}
	}
	quotes := len(content.Evidence)
	setBody := func(compact bool) {
		message.Body = r.recommendationBody(content, quotes, compact, message.KakattaURL)
		message.Reason, _, _ = strings.Cut(message.Body, "\n")
	}
	setBody(false)
	message.Title = r.title(parts, false)
	if r.err != nil {
		return r.err
	}
	if notify.MessageFits(channel, *message) {
		return nil
	}

	// 先沿用标题的裁剪顺序，正文不跟着丢字。
	originalTitle, originalItem := message.Title, message.ItemTitle
	if channel.Type == notify.Telegram {
		// 只有标题本身连纯文字上限都放不下时，才裁规格和任务名。
		titleOnly := *message
		titleOnly.Body, titleOnly.ItemTitle, titleOnly.ImageURL, titleOnly.TextOnly = "", "", "", true
		if !notify.MessageFits(channel, titleOnly) {
			text := *message
			text.ImageURL, text.TextOnly = "", true
			_ = fitNotificationTitle(r, &text, parts, channel.Type)
			message.Title, message.ItemTitle = text.Title, text.ItemTitle
		} else {
			for !notify.MessageFits(channel, *message) && message.ItemTitle != "" {
				runes := []rune(message.ItemTitle)
				message.ItemTitle = string(runes[:len(runes)-1])
			}
		}
	} else {
		_ = fitNotificationTitle(r, message, parts, channel.Type)
	}
	if originalTitle != message.Title || originalItem != message.ItemTitle {
		message.FormatLevel = max(message.FormatLevel, 2)
	}
	if r.err != nil {
		return r.err
	}
	if notify.MessageFits(channel, *message) {
		return nil
	}

	if channel.Type == notify.Telegram && message.ImageURL != "" || channel.Type == notify.WeCom && message.Link != "" {
		content.ImageLink, message.ImageURL, message.TextOnly = message.ImageURL, "", true
		message.FormatLevel = 3
		setBody(false)
		if notify.MessageFits(channel, *message) {
			return nil
		}
	}

	for quotes > 0 {
		quotes--
		message.FormatLevel = 4
		setBody(false)
		if r.err != nil {
			return r.err
		}
		if notify.MessageFits(channel, *message) {
			return nil
		}
	}
	message.FormatLevel = 5
	setBody(true)
	if r.err != nil {
		return r.err
	}
	if !notify.MessageFits(channel, *message) {
		return errFormatMessage
	}
	return nil
}
