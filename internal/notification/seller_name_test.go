package notification

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/notify"
)

func TestMissingSellerNicknameDoesNotLeaveBlankNotificationContent(t *testing.T) {
	for _, language := range []locales.Language{locales.ZhCN, locales.En, locales.Ja} {
		for _, mode := range []analysis.Mode{analysis.ModeAI, analysis.ModeKeyword} {
			for _, kind := range []intentKind{intentRecommended, intentNew, intentPriceDrop, intentPriceUpgrade} {
				for _, summary := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/summary=%v", language, mode, kind, summary), func(t *testing.T) {
						task := formatTask(mode)
						value := formatCandidate(1, kind, formatPtr(int64(84)))
						group := messageGroup{Members: []candidate{value}, Summary: summary}
						if summary {
							group.Members = append(group.Members, formatCandidate(2, kind, formatPtr(int64(84))))
						}
						message, err := formatGroup(task, group, formatChannel(notify.Webhook), formatOpts(language))
						if err != nil || strings.TrimSpace(message.Title) == "" || strings.TrimSpace(message.Body) == "" || strings.TrimSpace(message.Reason) == "" {
							t.Fatal("卖家编号和昵称都不可得时通知不能留白", message, err)
						}
						if !summary && mode == analysis.ModeAI && (kind == intentRecommended || kind == intentPriceUpgrade) {
							missing, err := locales.Format(language, locales.Message{Code: "notification.seller_unavailable"})
							if err != nil || !strings.Contains(message.Body, missing) {
								t.Fatal("推荐通知没有沿用卖家资料不可得提示", message.Body, err)
							}
						}
						for _, nickname := range []string{"", " \t", "真实卖家"} {
							group.Members[0].Listing.SellerNick = &nickname
							actual, err := formatGroup(task, group, formatChannel(notify.Webhook), formatOpts(language))
							if err != nil || !reflect.DeepEqual(actual, message) {
								t.Fatal("现有通知不展示昵称，缺失昵称不能改变正文格式", actual, err)
							}
							if *group.Members[0].Listing.SellerNick != nickname || group.Members[0].Listing.SellerID != nil {
								t.Fatal("展示层不能编造或回写卖家昵称、编号")
							}
						}
					})
				}
			}
		}
	}
}
