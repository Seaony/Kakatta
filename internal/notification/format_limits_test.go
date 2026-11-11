package notification

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/notify"
	"github.com/Seaony/Kakatta/internal/service"
)

func formatEvidenceFixture(count, nameLength int, quote string) (service.Task, candidate) {
	task := formatTask(analysis.ModeAI)
	value := formatCandidate(1, intentRecommended, formatPtr(int64(84)))
	value.Finding.Analysis = &analysis.Facts{}
	for index := 1; index <= count; index++ {
		id, name := fmt.Sprintf("m%d", index), fmt.Sprintf("条件%03d", index)+strings.Repeat("字", nameLength)
		task.Profile.Musts = append(task.Profile.Musts, analysis.Requirement{ID: id, Text: name, Rev: 1})
		value.Finding.Analysis.Musts = append(value.Finding.Analysis.Musts, analysis.RequirementFact{ID: id, Status: analysis.ReqMet, Source: formatPtr("text"), Evidence: formatPtr(quote), Rev: 1})
	}
	return task, value
}

func TestRecommendationFormatLevelsPreserveEvidenceAndDeclaredLimits(t *testing.T) {
	for _, language := range []locales.Language{locales.ZhCN, locales.En, locales.Ja} {
		for _, publicURL := range []bool{true, false} {
			for _, test := range []struct {
				name, quote      string
				channel          notify.Type
				count, nameWidth int
				level            int
				longSpec         bool
			}{
				{"无需精简", "无ID锁", notify.Bark, 1, 0, 0, false},
				{"一条1200字先截摘录", strings.Repeat("卖家原文", 300), notify.Telegram, 1, 0, 1, false},
				{"先裁长规格标题", "无ID锁", notify.ServerChan, 1, 0, 2, true},
				{"转为纯文字并保留图片链接", strings.Repeat("证", 60), notify.Telegram, 18, 0, 3, false},
				{"按HTML转义长度逆序折叠摘录", strings.Repeat("<&", 30), notify.Telegram, 24, 0, 4, false},
				{"企业微信按UTF8正文折叠", strings.Repeat("证", 60), notify.WeCom, 12, 0, 4, false},
				{"所有关键结论超限才改概要", "正常", notify.Telegram, 80, 60, 5, false},
			} {
				t.Run(fmt.Sprintf("%s/public=%t/%s", language, publicURL, test.name), func(t *testing.T) {
					task, value := formatEvidenceFixture(test.count, test.nameWidth, test.quote)
					if test.longSpec {
						spec := strings.Repeat("型号字", 100)
						task.Profile.Specs = []analysis.ProfileSpec{{Name: "型号", Options: []string{spec}, Values: []string{spec}}}
						value.ItemSpecs = &service.TaskItemSpecs{ItemSpecs: analysis.ItemSpecs{Specs: []analysis.SpecValue{{Name: "型号", Value: &spec}}}}
					}
					before, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					options := formatOpts(language)
					if !publicURL {
						options.FindingURL = nil
					}
					config := formatChannel(test.channel)
					message, err := formatGroup(task, messageGroup{Members: []candidate{value}}, config, options)
					if err != nil || message.FormatLevel != test.level || !notify.MessageFits(config, message) || !utf8.ValidString(message.Body) {
						t.Fatalf("通知未按批准次序精简或超限：level=%d expected=%d err=%v body=%q", message.FormatLevel, test.level, err, message.Body)
					}
					if test.channel == notify.Telegram && test.level >= 3 || test.channel == notify.WeCom && test.level >= 3 {
						if !message.TextOnly || message.ImageURL != "" || !strings.Contains(message.Body, *value.Listing.ThumbnailURL) {
							t.Fatal("纯文字降级丢掉原图链接，或仍要求发图")
						}
					}
					quoteLabel, foldedPattern, omittedPattern := "原文：", "另有 %d 条证据见详情", "另有 %d 项未显示"
					if language == locales.En {
						quoteLabel, foldedPattern, omittedPattern = "Original text: ", "%d additional evidence excerpts in details", "%d additional checks not shown"
					} else if language == locales.Ja {
						quoteLabel, foldedPattern, omittedPattern = "原文：", "ほか %d 件の根拠は詳細で確認", "ほか %d 項目は非表示"
					}
					if test.level == 1 {
						want := string([]rune(test.quote)[:59]) + "…"
						if !strings.Contains(message.Body, want) || strings.Contains(message.Body, test.quote) || utf8.RuneCountInString(want) != 60 {
							t.Fatal("摘录没有限制为包含省略号的60字")
						}
					}
					if test.level < 5 {
						for _, requirement := range task.Profile.Musts {
							if strings.Count(message.Body, requirement.Text) != 1 {
								t.Fatalf("非概要阶段丢掉或重复了关键结论：%s", requirement.Text)
							}
						}
					} else {
						if strings.Contains(message.Body, task.Profile.Musts[0].Text) || !strings.Contains(message.Body, fmt.Sprintf(omittedPattern, test.count)) || !strings.Contains(message.Body, message.Price) || !strings.Contains(message.Body, message.Grade) {
							t.Fatal("概要没有声明全部省略数量，或丢掉价格和档位")
						}
					}
					if test.level == 4 {
						quoted := strings.Count(message.Body, quoteLabel)
						if quoted == 0 || quoted >= test.count || !strings.Contains(message.Body, fmt.Sprintf(foldedPattern, test.count-quoted)) {
							t.Fatalf("折叠数和显示引用数不相符：quotes=%d body=%q", quoted, message.Body)
						}
						lines := strings.Split(message.Body, "\n")
						for index, requirement := range task.Profile.Musts {
							for _, line := range lines {
								if strings.Contains(line, requirement.Text) && strings.Contains(line, quoteLabel) != (index < quoted) {
									t.Fatal("没有从最后一条开始逆序折叠")
								}
							}
						}
					}
					if test.level >= 4 {
						if publicURL && !strings.Contains(message.Body, message.KakattaURL) {
							t.Fatal("省略内容后没有详情链接")
						}
						if !publicURL && !strings.Contains(message.Body, map[locales.Language]string{locales.ZhCN: "在 Kakatta 结果页查看", locales.En: "View this item on the Kakatta results page", locales.Ja: "Kakattaの結果ページで確認"}[language]) {
							t.Fatal("未配置外部地址时没有本地查看提示")
						}
					}
					after, err := json.Marshal(value)
					if err != nil || string(before) != string(after) {
						t.Fatal("通知精简改写了原始事实或引用")
					}
				})
			}
		}
	}
}

func TestRecommendationFeishuSignedPayloadSendsOnceAfterFolding(t *testing.T) {
	for _, test := range []struct {
		count, nameWidth, level int
		quote                   string
	}{
		{70, 0, 4, strings.Repeat("<", 60)},
		{300, 30, 5, "正常"},
	} {
		t.Run(fmt.Sprint(test.level), func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, err := io.ReadAll(r.Body)
				var payload map[string]any
				if err != nil || len(body) > notify.FeishuBodyBytes || json.Unmarshal(body, &payload) != nil || payload["sign"] == "" || payload["timestamp"] == "" {
					t.Error("实际签名请求超限或缺少签名字段")
				}
				fmt.Fprint(w, `{"code":0}`)
			}))
			defer server.Close()
			config := notify.ChannelConfig{Type: notify.Feishu, Secrets: json.RawMessage(`{"url":"` + server.URL + `","secret":"local-only"}`)}
			task, value := formatEvidenceFixture(test.count, test.nameWidth, test.quote)
			before, _ := json.Marshal(value.Finding.Analysis)
			message, err := formatGroup(task, messageGroup{Members: []candidate{value}}, config, formatOpts(locales.En))
			if err != nil || message.FormatLevel != test.level || !notify.MessageFits(config, message) {
				t.Fatalf("飞书真实编码与签名开销没有进入格式判定：level=%d err=%v", message.FormatLevel, err)
			}
			if result := notify.New(notify.Dependencies{HTTP: server.Client()}).Send(t.Context(), config, message); !result.OK || calls.Load() != 1 {
				t.Fatalf("精简通知没有恰好发送一次：%+v calls=%d", result, calls.Load())
			}
			after, _ := json.Marshal(value.Finding.Analysis)
			if string(before) != string(after) {
				t.Fatal("发送或精简覆盖了事实库里的完整引用")
			}
		})
	}
}

func TestRecommendationTelegramLongBodyKeepsTaskAndSpecificationTitle(t *testing.T) {
	for _, evidence := range []bool{false, true} {
		t.Run(fmt.Sprint(evidence), func(t *testing.T) {
			task, value := formatEvidenceFixture(24, 0, strings.Repeat("<&", 30))
			task.Name = "原任务名称"
			spec := "原规格型号"
			task.Profile.Specs = []analysis.ProfileSpec{{Name: "型号", Options: []string{spec}, Values: []string{spec}}}
			value.ItemSpecs = &service.TaskItemSpecs{ItemSpecs: analysis.ItemSpecs{Specs: []analysis.SpecValue{{Name: "型号", Value: &spec}}}}
			value.Listing.ThumbnailURL = nil
			if !evidence {
				task.Profile.Musts = nil
				value.Finding.Analysis = nil
				value.Finding.Summary = formatPtr(strings.Repeat("长自由摘要", 1500))
			}
			message, err := formatGroup(task, messageGroup{Members: []candidate{value}}, formatChannel(notify.Telegram), formatOpts(locales.ZhCN))
			if err != nil || !strings.Contains(message.Title, "[原任务名称]") || !strings.Contains(message.Title, spec) || message.FormatLevel < 4 || !notify.MessageFits(formatChannel(notify.Telegram), message) {
				t.Fatalf("正文过长连带删除了正常的任务或规格标题：title=%q level=%d err=%v", message.Title, message.FormatLevel, err)
			}
		})
	}
}

func TestRecommendationTelegramCaptionFallsBackBeforeTrimmingValidTextTitle(t *testing.T) {
	task, value := formatEvidenceFixture(1, 0, "无ID锁")
	task.Name = "原任务名称"
	spec := strings.Repeat("规格", 600)
	task.Profile.Specs = []analysis.ProfileSpec{{Name: "型号", Options: []string{spec}, Values: []string{spec}}}
	value.ItemSpecs = &service.TaskItemSpecs{ItemSpecs: analysis.ItemSpecs{Specs: []analysis.SpecValue{{Name: "型号", Value: &spec}}}}
	message, err := formatGroup(task, messageGroup{Members: []candidate{value}}, formatChannel(notify.Telegram), formatOpts(locales.ZhCN))
	if err != nil || message.FormatLevel != 3 || !message.TextOnly || !strings.Contains(message.Title, "[原任务名称]") || !strings.Contains(message.Title, spec) || !notify.MessageFits(formatChannel(notify.Telegram), message) {
		t.Fatalf("标题可放进纯文时却按图片说明裁掉了规格：level=%d title_runes=%d err=%v", message.FormatLevel, len([]rune(message.Title)), err)
	}
}
