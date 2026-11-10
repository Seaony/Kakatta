package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/notify"
	"github.com/Seaony/Kakatta/internal/service"
)

func formatPtr[T any](value T) *T { return &value }

func formatTask(mode analysis.Mode) service.Task {
	return service.Task{ID: 7, TaskFields: service.TaskFields{Name: "相机", JudgeMode: mode, Keyword: "相机"}}
}

func formatCandidate(id int64, kind intentKind, score *int64) candidate {
	return candidate{
		Intent:  intent{FindingID: id, TaskID: 7, Kind: kind, Price: formatPtr(int64(100000)), PreviousPrice: formatPtr(int64(130000))},
		Finding: service.Finding{ID: id, TaskID: 7, ItemID: fmt.Sprint(id), Decision: analysis.DecisionScored, Score: score, Grade: formatPtr(analysis.GradeGood), FairPrice: formatPtr(int64(130000)), Summary: formatPtr("模型{count}原话\n第二行")},
		Listing: service.Listing{ItemID: fmt.Sprint(id), Title: "商品标题", DesktopURL: "https://www.goofish.com/item?id=" + fmt.Sprint(id), Price: formatPtr(int64(999999)), ThumbnailURL: formatPtr("https://images.invalid/item.jpg")},
	}
}

func formatOpts(language locales.Language) formatOptions {
	return formatOptions{Language: language, Location: time.FixedZone("test", 8*3600), LinkTarget: "desktop", FindingURL: func(id int64) string { return fmt.Sprintf("https://kakatta.invalid/prefix/results/%d", id) }, TaskResultsURL: "https://kakatta.invalid/prefix/results?task_id=7"}
}

func TestGroupMessagesStableRanksAndSnapshotDrop(t *testing.T) {
	values := []candidate{formatCandidate(9, intentNew, nil), formatCandidate(7, intentPriceDrop, nil), formatCandidate(4, intentRecommended, nil), formatCandidate(3, intentRecommended, formatPtr(int64(84))), formatCandidate(2, intentPriceUpgrade, formatPtr(int64(91))), formatCandidate(1, intentRecommended, formatPtr(int64(84))), formatCandidate(8, intentPriceDrop, nil)}
	values[1].Intent.PreviousPrice = formatPtr(int64(110000))
	values[6].Listing.LastDropAmount = formatPtr(int64(1))
	before := append([]candidate(nil), values...)
	groups, err := groupMessages(formatTask(analysis.ModeAI), values, 20)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, group := range groups {
		ids = append(ids, group.Members[0].Finding.ID)
	}
	if !reflect.DeepEqual(ids, []int64{2, 1, 3, 4, 8, 7, 9}) || !reflect.DeepEqual(values, before) {
		t.Fatalf("排序或输入被改写：%v", ids)
	}
	values = []candidate{formatCandidate(3, intentRecommended, nil), formatCandidate(2, intentRecommended, nil), formatCandidate(1, intentRecommended, nil)}
	values[0].Listing.PublishedAt, values[1].Listing.PublishedAt = formatPtr(int64(100)), formatPtr(int64(100))
	groups, err = groupMessages(formatTask(analysis.ModeKeyword), values, 3)
	if err != nil || groups[0].Members[0].Finding.ID != 2 || groups[1].Members[0].Finding.ID != 3 || groups[2].Members[0].Finding.ID != 1 {
		t.Fatalf("关键词日期排序错误：%v %v", groups, err)
	}
}

func TestGroupMessagesLimitsAndNoLostMembers(t *testing.T) {
	for _, maxPerRun := range []int64{1, 5} {
		for _, count := range []int{0, 1, int(maxPerRun), int(maxPerRun) + 1, 13} {
			values := make([]candidate, count)
			for index := range values {
				values[index] = formatCandidate(int64(index+1), intentRecommended, nil)
			}
			groups, err := groupMessages(formatTask(analysis.ModeAI), values, maxPerRun)
			if err != nil || int64(len(groups)) > maxPerRun {
				t.Fatalf("超出消息上限：max=%d count=%d %v", maxPerRun, count, err)
			}
			members := 0
			for _, group := range groups {
				members += len(group.Members)
			}
			if members != count || count == 1 && groups[0].Summary || count > int(maxPerRun) && !groups[len(groups)-1].Summary {
				t.Fatalf("分组漏成员或错误摘要：max=%d count=%d groups=%v", maxPerRun, count, groups)
			}
		}
	}
}

func TestGroupMessagesRejectsInconsistentProjection(t *testing.T) {
	valid := formatCandidate(1, intentRecommended, nil)
	for _, mutate := range []func(*candidate){
		func(value *candidate) { value.Finding.TaskID = 8 },
		func(value *candidate) { value.Intent.FindingID = 2 },
		func(value *candidate) { value.Listing.ItemID = "2" },
		func(value *candidate) { value.Intent.Kind = "promoted" },
		func(value *candidate) { value.Intent.Price = formatPtr(int64(-1)) },
		func(value *candidate) {
			value.Intent.Kind, value.Intent.PreviousPrice = intentPriceDrop, formatPtr(int64(100000))
		},
	} {
		value := valid
		mutate(&value)
		if _, err := groupMessages(formatTask(analysis.ModeAI), []candidate{value}, 5); err == nil {
			t.Fatal("错误投影没有拒绝")
		}
	}
	if _, err := groupMessages(formatTask(analysis.ModeAI), []candidate{valid, valid}, 5); err == nil {
		t.Fatal("重复发现被静默合并")
	}
	if _, err := groupMessages(formatTask(analysis.ModeAI), nil, 0); err == nil {
		t.Fatal("零上限没有拒绝")
	}
}

func TestFormatMessageKindsAndLocalizedCore(t *testing.T) {
	for _, language := range []locales.Language{locales.ZhCN, locales.En} {
		for _, test := range []struct {
			kind intentKind
			mode analysis.Mode
			want string
		}{
			{intentRecommended, analysis.ModeAI, "recommended"}, {intentPriceUpgrade, analysis.ModeAI, "recommended"},
			{intentRecommended, analysis.ModeKeyword, "recommended"}, {intentNew, analysis.ModeAI, "all_new"},
			{intentPriceDrop, analysis.ModeAI, "price_drop"},
		} {
			value := formatCandidate(1, test.kind, formatPtr(int64(84)))
			value.Finding.Reason = &locales.Message{Code: "analysis.keyword_matched", Args: map[string]any{"count": 1, "words": "{count}原文"}}
			message, err := formatGroup(formatTask(test.mode), messageGroup{Members: []candidate{value}}, notify.Bark, formatOpts(language))
			if err != nil {
				t.Fatal(language, test.kind, err)
			}
			if message.Kind != test.want || message.Price != "¥1,000" || message.Score != "84" || message.Task != "相机" || message.ItemID != "1" || message.LinkLabel == "" || message.Footer == "" || message.KakattaURL == "" || message.Reason == "" {
				t.Fatalf("消息字段缺失：%+v", message)
			}
			if !strings.HasSuffix(message.Body, "模型{count}原话\n第二行") || message.Reason != strings.Split(message.Body, "\n")[0] {
				t.Fatalf("原话被改写或reason不是首行：%+v", message)
			}
			if test.kind == intentPriceDrop && (!strings.Contains(message.Title, "¥300") || strings.Contains(message.Title, "¥1,000") || !strings.Contains(message.Body, "¥1,300 → ¥1,000")) {
				t.Fatalf("降幅与现价混淆：%+v", message)
			}
		}
		message, err := testMessage(language)
		if err != nil || message.Kind != "test" || message.Urgent || message.ImageURL == "" || message.Link != "https://www.goofish.com/" || message.ItemID != "" {
			t.Fatalf("测试通知错误：%+v %v", message, err)
		}
	}
}

func TestFormatSummaryCountsHiddenUrgentAndLinks(t *testing.T) {
	for _, language := range []locales.Language{locales.ZhCN, locales.En} {
		members := make([]candidate, 13)
		for index := range members {
			members[index] = formatCandidate(int64(index+1), intentPriceUpgrade, formatPtr(int64(84)))
			members[index].Listing.Title = "标题\n不能增加摘要行" + strings.Repeat("文", 50)
		}
		members[12].Finding.Grade = formatPtr(analysis.GradeSteal)
		group := messageGroup{Members: members, Summary: true}
		message, err := formatGroup(formatTask(analysis.ModeAI), group, notify.Bark, formatOpts(language))
		if err != nil || !strings.Contains(message.Title, "13") || !message.Urgent || message.Kind != "summary" || len(strings.Split(message.Body, "\n")) != 10 {
			t.Fatalf("摘要计数、行数或隐藏捡漏丢失：%+v %v", message, err)
		}
		if message.ItemID != "" || message.ItemTitle != "" || message.Price != "" || message.ImageURL != "" || message.Config != "" || message.Score != "" || message.Grade != "" || message.Link != formatOpts(language).TaskResultsURL {
			t.Fatalf("摘要伪造单品字段：%+v", message)
		}
		if language == locales.En && message.LinkLabel != "Open in Kakatta" {
			t.Fatal(message.LinkLabel)
		}
		members[11].Intent.Kind = intentNew
		mixed, err := formatGroup(formatTask(analysis.ModeAI), group, notify.Bark, formatOpts(language))
		if err != nil || mixed.Title == message.Title {
			t.Fatalf("混合摘要仍显示推荐：%+v %v", mixed, err)
		}
		options := formatOpts(language)
		options.TaskResultsURL = ""
		fallback, err := formatGroup(formatTask(analysis.ModeAI), group, notify.Bark, options)
		if err != nil || fallback.Link != members[0].Listing.DesktopURL || fallback.KakattaURL != "" {
			t.Fatalf("摘要没有使用首商品回退：%+v %v", fallback, err)
		}
	}
}

func TestNotificationMoneyKeepsEveryCent(t *testing.T) {
	for _, test := range []struct {
		cents int64
		want  string
	}{{0, "¥0"}, {5, "¥0.05"}, {50, "¥0.50"}, {685000, "¥6,850"}, {685005, "¥6,850.05"}, {685050, "¥6,850.50"}, {math.MaxInt64, "¥92,233,720,368,547,758.07"}, {math.MinInt64, "¥-92,233,720,368,547,758.08"}} {
		if got := formatNotificationMoney(test.cents); got != test.want {
			t.Fatalf("%d=%s，预期%s", test.cents, got, test.want)
		}
	}
	for _, language := range []locales.Language{locales.ZhCN, locales.En} {
		r := messageRenderer{language: language}
		if r.money(nil) == r.money(formatPtr(int64(0))) || r.err != nil {
			t.Fatalf("未知价格被当作零：%v", r.err)
		}
		if equal, low, high := r.priceComparison(100, 100), r.priceComparison(9999, 10000), r.priceComparison(10001, 10000); equal == low || low == high || r.err != nil {
			t.Fatal("相等与不到1%未区分", r.err)
		}
	}
}

func TestFormatConfigurationUnknownZeroAndLiteralStar(t *testing.T) {
	task := formatTask(analysis.ModeAI)
	r := messageRenderer{language: locales.En}
	if title, config := r.configuration(task, nil); title != "" || config != "No configuration grouping" {
		t.Fatalf("零维规格不正确：%q %q", title, config)
	}
	task.Profile.Specs = []analysis.ProfileSpec{{Name: "型号", Options: []string{"*", "mini"}, Values: []string{"*", "mini"}}}
	if title, config := r.configuration(task, &service.TaskItemSpecs{}); title != "" || config != "" {
		t.Fatalf("未知规格被冒充零维：%q %q", title, config)
	}
	item := &service.TaskItemSpecs{ItemSpecs: analysis.ItemSpecs{Specs: []analysis.SpecValue{{Name: "型号", Value: formatPtr("*")}}}}
	if title, config := r.configuration(task, item); title != "*" || config != "*" {
		t.Fatalf("普通星号值被吞掉：%q %q", title, config)
	}
}

func TestFormatPendingChecksCompleteAndNamesLocalized(t *testing.T) {
	task := formatTask(analysis.ModeAI)
	task.Profile.Risks = []analysis.ProfileRisk{{ID: "carrier_lock", Attitude: analysis.Confirm, Rev: 1}, {ID: "c1", Name: "自定义{count}", Attitude: analysis.Confirm, Rev: 1}}
	for index := 2; index < 7; index++ {
		task.Profile.Risks = append(task.Profile.Risks, analysis.ProfileRisk{ID: fmt.Sprintf("c%d", index), Name: fmt.Sprintf("检查%d", index), Attitude: analysis.Mind, Rev: 1})
	}
	value := formatCandidate(1, intentRecommended, formatPtr(int64(84)))
	message, err := formatGroup(task, messageGroup{Members: []candidate{value}}, notify.Bark, formatOpts(locales.En))
	if err != nil || !strings.Contains(message.Reason, "7 checks") || !strings.Contains(message.Reason, "Carrier lock") || !strings.Contains(message.Reason, "自定义{count}") || strings.Contains(message.Reason, "检查2") || strings.Contains(message.Reason, "、") {
		t.Fatalf("待确认总数或前两项错误：%+v %v", message, err)
	}
	if comparison, seller, pending := strings.Index(message.Reason, "below"), strings.Index(message.Reason, "Seller information"), strings.Index(message.Reason, "checks"); comparison < 0 || seller <= comparison || pending <= seller {
		t.Fatalf("首行片段顺序错误：%s", message.Reason)
	}
}

func TestFormatSavedSellerAssessmentIgnoresCurrentCache(t *testing.T) {
	s, db, ref := planFixture(t)
	planFinding(t, db, 1, analysis.DecisionScored, formatPtr(int64(10000)), formatPtr(int64(84)))
	profile := analysis.Profile{Risks: []analysis.ProfileRisk{
		{ID: "seller_dealer", Attitude: analysis.Confirm, Rev: 1},
		{ID: "carrier_lock", Attitude: analysis.Confirm, Rev: 1},
	}}
	snapshot := analysis.SellerResult{Type: analysis.SellerDealerSuspect,
		Checks: []analysis.EffectiveCheck{
			{Key: "seller_dealer", Status: analysis.CheckSuspect, Source: analysis.EvidenceProgram, Evidence: formatPtr("同时在售三件相机")},
			{Key: "seller_new", Status: analysis.CheckRisk, Source: analysis.EvidenceProgram},
			{Key: "seller_bad_reviews", Status: analysis.CheckRisk, Source: analysis.EvidenceProgram, Evidence: formatPtr("售后描述待核对")},
			{Key: "seller_partial_refund", Status: analysis.CheckOK, Source: analysis.EvidenceProgram},
		}, Reasons: []locales.Message{{Code: "analysis.seller_dealer_items", Args: map[string]any{"count": 3}}}}
	profileJSON, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	planExec(t, db, `UPDATE tasks SET profile_json=? WHERE id=1`, string(profileJSON))
	planExec(t, db, `UPDATE listings SET seller_id='saved-seller' WHERE item_id='item-1'`)
	planExec(t, db, `UPDATE findings SET seller_checks_json=?,analysis_json='{}' WHERE id=1`, string(snapshotJSON))
	previous := map[locales.Language]notify.Message{}
	for _, cache := range []string{"available", "cleared", "conflicting"} {
		t.Run(cache, func(t *testing.T) {
			planExec(t, db, `DELETE FROM seller_profiles`)
			planExec(t, db, `UPDATE listings SET seller_registration_days=7 WHERE item_id='item-1'`)
			if cache != "cleared" {
				items := `[{"title":"camera A","sale_status":"on_sale"},{"title":"camera B","sale_status":"on_sale"},{"title":"camera C","sale_status":"on_sale"}]`
				if cache == "conflicting" {
					items = `[]`
					planExec(t, db, `UPDATE listings SET seller_registration_days=365 WHERE item_id='item-1'`)
				}
				planExec(t, db, `INSERT INTO seller_profiles(seller_id,recent_items_json,recent_rates_json,fetched_at) VALUES('saved-seller',?,'[]',1)`, items)
			}
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			task, err := service.NotificationTaskTx(t.Context(), tx, ref.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			value, err := loadNotificationCandidateTx(t.Context(), tx, 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			value.Intent = intent{FindingID: 1, TaskID: 1, Kind: intentRecommended, Price: value.Listing.Price}
			if value.Finding.SellerAssessment == nil {
				t.Fatal("没有读到真实持久卖家评估")
			}
			before, err := json.Marshal(value.Finding.SellerAssessment)
			if err != nil {
				t.Fatal(err)
			}
			if got := notificationSeller(task, value); !reflect.DeepEqual(got, *value.Finding.SellerAssessment) {
				t.Fatalf("当前缓存覆盖了已保存评估：%+v", got)
			}
			for _, language := range []locales.Language{locales.ZhCN, locales.En} {
				message, err := formatGroup(task, messageGroup{Members: []candidate{value}}, notify.Bark, formatOpts(language))
				if err != nil {
					t.Fatal(err)
				}
				portrait, pending := "疑似商家、新号", "2 项待确认"
				if language == locales.En {
					portrait, pending = "Possibly a dealer, new account", "2 checks need confirmation"
				}
				if !strings.Contains(message.Reason, portrait) || !strings.Contains(message.Reason, pending) {
					t.Fatalf("通知画像或待确认数量偏离持久结论：%s", message.Reason)
				}
				if expected, ok := previous[language]; ok && !reflect.DeepEqual(message, expected) {
					t.Fatalf("缓存变化改写通知：%+v != %+v", message, expected)
				}
				previous[language] = message
			}
			after, err := json.Marshal(value.Finding.SellerAssessment)
			if err != nil || string(before) != string(after) {
				t.Fatal("格式化改写了内存中的持久评估", err)
			}
		})
	}
	var saved string
	if err := s.db.QueryRowContext(t.Context(), `SELECT seller_checks_json FROM findings WHERE id=1`).Scan(&saved); err != nil || saved != string(snapshotJSON) {
		t.Fatalf("格式化改写了数据库快照：%s %v", saved, err)
	}
}

func TestNotificationSellerKeepsLegacyCacheFallback(t *testing.T) {
	for _, test := range []struct {
		name   string
		seller *service.SellerProfile
		days   *int64
		want   analysis.SellerType
	}{
		{"unavailable", nil, nil, analysis.SellerUnknown},
		{"personal", &service.SellerProfile{RecentRates: []service.SellerRecentRate{}}, formatPtr(int64(365)), analysis.SellerPersonal},
		{"dealer", &service.SellerProfile{RecentItems: []service.SellerRecentItem{
			{Title: "相机 A", SaleStatus: formatPtr("on_sale")}, {Title: "相机 B", SaleStatus: formatPtr("on_sale")}, {Title: "相机 C", SaleStatus: formatPtr("on_sale")},
		}}, formatPtr(int64(7)), analysis.SellerDealerSuspect},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := formatCandidate(1, intentRecommended, formatPtr(int64(84)))
			value.Seller, value.Listing.SellerRegistrationDays = test.seller, test.days
			got := notificationSeller(formatTask(analysis.ModeAI), value)
			if got.Type != test.want || value.Finding.SellerAssessment != nil {
				t.Fatalf("旧记录回退路径改变或伪造快照：%+v", got)
			}
		})
	}
}

func TestFormatAllNewUnmatchedPrefixAndNoComparable(t *testing.T) {
	value := formatCandidate(1, intentNew, nil)
	value.Finding.Grade = nil
	value.Finding.RejectKind = formatPtr(analysis.RejectUnmatched)
	value.Finding.Reason = &locales.Message{Code: "analysis.spec_mismatch", Args: map[string]any{"name": "型号", "value": "mini{count}"}}
	message, err := formatGroup(formatTask(analysis.ModeAI), messageGroup{Members: []candidate{value}}, notify.Bark, formatOpts(locales.En))
	if err != nil || !strings.Contains(message.Reason, "Does not meet your requirements") || !strings.Contains(message.Body, "mini{count}") || strings.Contains(message.Body, "No comparable price") {
		t.Fatalf("不符合需求没有准确表示：%+v %v", message, err)
	}
	value.Intent.Kind, value.Finding.RejectKind, value.Finding.FairPrice, value.Finding.FairSample = intentRecommended, nil, nil, formatPtr(int64(3))
	message, err = formatGroup(formatTask(analysis.ModeAI), messageGroup{Members: []candidate{value}}, notify.Bark, formatOpts(locales.En))
	if err != nil || !strings.Contains(message.Title, "No comparable price") || !strings.Contains(message.Body, "3 comparable listings") || message.FairPrice != "" {
		t.Fatalf("无法比价样本错误：%+v %v", message, err)
	}
}

func TestFormatTitleShrinksSpecBeforeOtherFields(t *testing.T) {
	for _, channel := range []notify.Type{notify.ServerChan, notify.WeCom, notify.Telegram} {
		t.Run(string(channel), func(t *testing.T) {
			task := formatTask(analysis.ModeAI)
			task.Name = "相机"
			longSpec := strings.Repeat("规格😀<&", 100)
			task.Profile.Specs = []analysis.ProfileSpec{{Name: "型号", Options: []string{longSpec}, Values: []string{longSpec}}}
			value := formatCandidate(1, intentRecommended, formatPtr(int64(84)))
			value.Finding.Summary = formatPtr("保留完整理由<&😀{count}")
			value.ItemSpecs = &service.TaskItemSpecs{ItemSpecs: analysis.ItemSpecs{Specs: []analysis.SpecValue{{Name: "型号", Value: &longSpec}}}}
			message, err := formatGroup(task, messageGroup{Members: []candidate{value}}, channel, formatOpts(locales.ZhCN))
			if err != nil || !strings.HasPrefix(message.Title, "[相机]") || message.ItemTitle != value.Listing.Title || message.Config != longSpec || !strings.Contains(message.Body, *value.Finding.Summary) || !strings.Contains(message.Title, "¥1,000") || !utf8.ValidString(message.Title) {
				t.Fatalf("缩标题改掉核心数据：%+v %v", message, err)
			}
			if channel == notify.ServerChan && utf8.RuneCountInString(message.Title) > notify.ServerChanTitleRunes || channel == notify.WeCom && len(message.Title+" · "+message.ItemTitle) > notify.WeComTitleBytes || channel == notify.Telegram && notify.TelegramContentUnits(message) > notify.TelegramCaptionLimit {
				t.Fatal("标题超出实际协议限量")
			}
		})
	}
	value := formatCandidate(1, intentRecommended, formatPtr(int64(84)))
	value.Finding.Summary = formatPtr(strings.Repeat("不能丢的正文", 1000))
	if _, err := formatGroup(formatTask(analysis.ModeAI), messageGroup{Members: []candidate{value}}, notify.Telegram, formatOpts(locales.ZhCN)); err != errFormatMessage {
		t.Fatalf("不可缩正文超限没有明确失败：%v", err)
	}
}

func TestFormatTelegramUsesRealHTMLAndSendsOnceLocally(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		var payload struct {
			Caption string `json:"caption"`
		}
		if json.NewDecoder(request.Body).Decode(&payload) != nil || notify.UTF16Len(payload.Caption) > notify.TelegramCaptionLimit || !strings.Contains(payload.Caption, "保留&lt;&amp;😀{count}") || !strings.Contains(payload.Caption, "¥1,000") || !strings.Contains(payload.Caption, "goofish.com/item?id=1") {
			t.Error("真实HTML没有保留理由、价格、链接或超限", payload.Caption)
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer server.Close()
	task := formatTask(analysis.ModeAI)
	spec := strings.Repeat("规格<&😀", 120)
	task.Profile.Specs = []analysis.ProfileSpec{{Name: "型号", Options: []string{spec}, Values: []string{spec}}}
	value := formatCandidate(1, intentRecommended, formatPtr(int64(84)))
	value.Finding.Summary = formatPtr("保留<&😀{count}")
	value.ItemSpecs = &service.TaskItemSpecs{ItemSpecs: analysis.ItemSpecs{Specs: []analysis.SpecValue{{Name: "型号", Value: &spec}}}}
	message, err := formatGroup(task, messageGroup{Members: []candidate{value}}, notify.Telegram, formatOpts(locales.ZhCN))
	if err != nil {
		t.Fatal(err)
	}
	sender := notify.New(notify.Dependencies{HTTP: server.Client()})
	config := notify.ChannelConfig{Type: notify.Telegram, Config: json.RawMessage(`{"chat_id":"1","api_url":"` + server.URL + `"}`), Secrets: json.RawMessage(`{"token":"local"}`)}
	if result := sender.Send(context.Background(), config, message); !result.OK || calls.Load() != 1 {
		t.Fatalf("真实适配器没有成功发一次：%+v calls=%d", result, calls.Load())
	}
}

func TestFormatSystemAllKindsAndPersistentFacts(t *testing.T) {
	deadline := time.Date(2026, 11, 11, 6, 32, 0, 0, time.UTC).UnixMilli()
	reason := locales.Message{Code: "analysis.question_custom", Args: map[string]any{"question": "原话{count}<>&\n下一行"}}
	cases := []struct {
		kind service.SystemEventKind
		args map[string]any
	}{
		{service.EventAccountVerify, map[string]any{"name": "账号", "count": int64(2)}},
		{service.EventAccountDeny, map[string]any{"name": "账号", "hours": int64(24), "until": deadline, "environment": true}},
		{service.EventAccountLogin, map[string]any{"name": "账号", "count": int64(2), "reassigned_to": "另一个账号"}},
		{service.EventAIError, map[string]any{"provider": "服务商", "reason": reason}},
		{service.EventAIBudget, map[string]any{"tokens": int64(12345)}},
		{service.EventTaskFailurePause, map[string]any{"task": "任务", "count": int64(3), "minutes": int64(30), "reason": reason}},
		{service.EventXianyuLayout, map[string]any{"reason": reason}},
		{service.EventLoginFailures, map[string]any{"count": int64(20), "ip": "127.0.0.1"}},
	}
	for _, language := range []locales.Language{locales.ZhCN, locales.En} {
		for _, test := range cases {
			t.Run(string(language)+"/"+string(test.kind), func(t *testing.T) {
				event := service.SystemEvent{Kind: &test.kind, Message: &locales.Message{Code: "persisted.event", Args: test.args}}
				before, _ := json.Marshal(event)
				message, err := formatSystem(event, formatOpts(language), "https://kakatta.invalid/target", &deadline)
				if err != nil || message.Kind != "system" || !message.Urgent || message.ImageURL != "" || message.ItemID != "" || message.ItemTitle != "" || message.Link != "https://kakatta.invalid/target" || message.KakattaURL != message.Link || message.LinkLabel == "" || message.Footer == "" {
					t.Fatalf("系统通知字段错误：%+v %v", message, err)
				}
				after, _ := json.Marshal(event)
				if string(before) != string(after) {
					t.Fatal("格式化改写了持久事实")
				}
				if (test.kind == service.EventAccountVerify || test.kind == service.EventAccountDeny) && !strings.Contains(message.Body, "14:32") {
					t.Fatal("截止时间没有按显式时区显示", message.Body)
				}
				if test.kind == service.EventAIError || test.kind == service.EventTaskFailurePause || test.kind == service.EventXianyuLayout {
					if !strings.Contains(message.Body, "原话{count}<>&\n下一行") {
						t.Fatal("嵌套原因递归插值或被裁掉", message.Body)
					}
				}
				// 持久记录反序列化后也必须得到同一消息。
				var decoded service.SystemEvent
				if json.Unmarshal(before, &decoded) != nil {
					t.Fatal("事件不能还原")
				}
				decodedMessage, err := formatSystem(decoded, formatOpts(language), message.Link, &deadline)
				if err != nil || decodedMessage != message {
					t.Fatalf("持久事实还原后消息不一致：%+v %v", decodedMessage, err)
				}
			})
		}
	}
}

func TestFormatSystemOptionalSentencesAndSingulars(t *testing.T) {
	options := formatOpts(locales.En)
	deadline := int64(1234567890000)
	kind := service.EventAccountVerify
	event := service.SystemEvent{Kind: &kind, Message: &locales.Message{Args: map[string]any{"name": "账号", "count": int64(1)}}}
	without, err := formatSystem(event, options, "", nil)
	with, withErr := formatSystem(event, options, "", &deadline)
	if err != nil || withErr != nil || without.Body == with.Body || !strings.Contains(without.Body, "1 task") || strings.Contains(without.Body, "1 tasks") || strings.Contains(without.Body, "browser") || without.Link != "" {
		t.Fatalf("资源保留或单数错误：%+v %+v %v %v", without, with, err, withErr)
	}
	kind = service.EventAccountDeny
	event.Message.Args = map[string]any{"name": "账号", "hours": int64(1), "until": deadline, "environment": false}
	message, err := formatSystem(event, options, "", nil)
	if err != nil || !strings.Contains(message.Body, "1 hour") || strings.Contains(message.Body, "WebGL") {
		t.Fatalf("拒绝正文可选句或单数错误：%+v %v", message, err)
	}
	kind = service.EventAccountLogin
	event.Message.Args = map[string]any{"name": "账号", "count": int64(0)}
	message, err = formatSystem(event, options, "", nil)
	if err != nil || strings.Contains(message.Body, "moved") {
		t.Fatalf("零换号任务被虚构：%+v %v", message, err)
	}
	kind = service.EventTaskFailurePause
	event.Message.Args = map[string]any{"task": "任务", "count": int64(3), "minutes": int64(1), "reason": locales.Message{Code: "analysis.keyword_miss"}}
	message, err = formatSystem(event, options, "", nil)
	if err != nil || !strings.Contains(message.Title, "3 times") || !strings.Contains(message.Body, "1 minute.") || strings.Contains(message.Body, "1 minutes") {
		t.Fatalf("失败次数与暂停时长复数共用了count：%+v %v", message, err)
	}
}

func TestFormatErrorsReturnNoPartialMessage(t *testing.T) {
	value := formatCandidate(1, intentRecommended, nil)
	for _, group := range []messageGroup{{}, {Members: []candidate{value, value}}, {Members: []candidate{value, value}, Summary: true}} {
		if message, err := formatGroup(formatTask(analysis.ModeAI), group, notify.Bark, formatOpts(locales.En)); err == nil || message != (notify.Message{}) {
			t.Fatalf("错误分组返回部分内容：%+v %v", message, err)
		}
	}
	if message, err := formatGroup(formatTask(analysis.ModeAI), messageGroup{Members: []candidate{value}}, notify.Bark, formatOpts("xx")); err == nil || message != (notify.Message{}) {
		t.Fatalf("未知语言被默默接受：%+v %v", message, err)
	}
	kind := service.EventAccountDeny
	for _, args := range []map[string]any{{}, {"until": "future"}, {"until": int64(123), "environment": "true"}} {
		event := service.SystemEvent{Kind: &kind, Message: &locales.Message{Args: args}}
		if _, err := formatSystem(event, formatOpts(locales.En), "", nil); err == nil {
			t.Fatal("损坏的系统事实被接受")
		}
	}
}

func TestFormatLimitedChannelsKeepCoreOnWire(t *testing.T) {
	for _, kind := range []notify.Type{notify.WeCom, notify.ServerChan} {
		t.Run(string(kind), func(t *testing.T) {
			var calls atomic.Int64
			var sentTitle string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				var payload map[string]json.RawMessage
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if kind == notify.WeCom {
					var news struct {
						Articles []struct{ Title, Description, URL string }
					}
					if err := json.Unmarshal(payload["news"], &news); err != nil || len(news.Articles) != 1 {
						t.Error("图文消息不正确", err)
					} else {
						sentTitle = news.Articles[0].Title
						if len(sentTitle) > notify.WeComTitleBytes || !strings.Contains(news.Articles[0].Description, "完整理由") || news.Articles[0].URL == "" {
							t.Error("企业微信丢了理由或链接")
						}
					}
					fmt.Fprint(w, `{"errcode":0}`)
				} else {
					var body string
					json.Unmarshal(payload["title"], &sentTitle)
					json.Unmarshal(payload["desp"], &body)
					if utf8.RuneCountInString(sentTitle) > notify.ServerChanTitleRunes || !strings.Contains(body, "完整理由") || !strings.Contains(body, "goofish") {
						t.Error("Server酱丢了理由或链接")
					}
					fmt.Fprint(w, `{"code":0}`)
				}
			}))
			defer server.Close()
			task := formatTask(analysis.ModeAI)
			spec := strings.Repeat("长规格😀", 100)
			task.Profile.Specs = []analysis.ProfileSpec{{Name: "型号", Options: []string{spec}, Values: []string{spec}}}
			value := formatCandidate(1, intentRecommended, formatPtr(int64(84)))
			value.Finding.Summary = formatPtr("完整理由")
			value.ItemSpecs = &service.TaskItemSpecs{ItemSpecs: analysis.ItemSpecs{Specs: []analysis.SpecValue{{Name: "型号", Value: &spec}}}}
			message, err := formatGroup(task, messageGroup{Members: []candidate{value}}, kind, formatOpts(locales.ZhCN))
			if err != nil {
				t.Fatal(err)
			}
			sender := notify.New(notify.Dependencies{HTTP: server.Client(), Endpoints: notify.Endpoints{ServerChanTurbo: server.URL}})
			config := notify.ChannelConfig{Type: kind, Config: json.RawMessage(`{}`), Secrets: json.RawMessage(`{"url":"` + server.URL + `"}`)}
			if kind == notify.ServerChan {
				config.Secrets = json.RawMessage(`{"send_key":"local"}`)
			}
			if result := sender.Send(t.Context(), config, message); !result.OK || calls.Load() != 1 || !strings.Contains(sentTitle, "¥1,000") {
				t.Fatalf("核心标题被真实适配器截掉：%+v %q calls=%d", result, sentTitle, calls.Load())
			}
		})
	}
}

func TestFormatFeishuOversizeUsesAdapterFailureWithoutOutbound(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"code":0}`)
	}))
	defer server.Close()
	value := formatCandidate(1, intentRecommended, formatPtr(int64(84)))
	value.Finding.Summary = formatPtr(strings.Repeat("完整理由不能截断", 3000))
	message, err := formatGroup(formatTask(analysis.ModeAI), messageGroup{Members: []candidate{value}}, notify.Feishu, formatOpts(locales.ZhCN))
	if err != nil || !strings.HasSuffix(message.Body, *value.Finding.Summary) {
		t.Fatal("业务层截掉了完整正文", err)
	}
	config := notify.ChannelConfig{Type: notify.Feishu, Config: json.RawMessage(`{}`), Secrets: json.RawMessage(`{"url":"` + server.URL + `","secret":"local"}`)}
	result := notify.New(notify.Dependencies{HTTP: server.Client()}).Send(t.Context(), config, message)
	if result.OK || result.ErrorCode != "invalid_message" || calls.Load() != 0 {
		t.Fatalf("超限飞书消息仍出站：%+v calls=%d", result, calls.Load())
	}
}
