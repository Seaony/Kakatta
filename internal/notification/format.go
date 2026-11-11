package notification

import (
	"encoding/json"
	"errors"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/notify"
	"github.com/Seaony/Kakatta/internal/service"
)

const notificationSampleImageURL = "https://raw.githubusercontent.com/Seaony/Kakatta/master/internal/analysis/images/assets/sample-photo.jpg"

var errFormatMessage = errors.New("invalid_message")

func groupMessages(task service.Task, candidates []candidate, maxPerRun int64) ([]messageGroup, error) {
	if maxPerRun < 1 || task.ID < 1 || task.JudgeMode != analysis.ModeAI && task.JudgeMode != analysis.ModeKeyword {
		return nil, errFormatMessage
	}
	values := append([]candidate(nil), candidates...)
	seen := make(map[int64]bool, len(values))
	for _, value := range values {
		if err := validateFormatCandidate(task, value); err != nil || seen[value.Finding.ID] {
			return nil, errFormatMessage
		}
		seen[value.Finding.ID] = true
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Finding.ID < values[j].Finding.ID })
	sort.SliceStable(values, func(i, j int) bool {
		a, b := values[i], values[j]
		if rankA, rankB := notificationRank(a.Intent.Kind), notificationRank(b.Intent.Kind); rankA != rankB {
			return rankA < rankB
		}
		switch a.Intent.Kind {
		case intentRecommended, intentPriceUpgrade:
			if task.JudgeMode == analysis.ModeAI {
				return greaterKnownInt(a.Finding.Score, b.Finding.Score)
			}
			return greaterKnownInt(a.Listing.PublishedAt, b.Listing.PublishedAt)
		case intentPriceDrop:
			return *a.Intent.PreviousPrice-*a.Intent.Price > *b.Intent.PreviousPrice-*b.Intent.Price
		}
		return false
	})
	groups := make([]messageGroup, 0, min(int64(len(values)), maxPerRun))
	individuals := len(values)
	if int64(individuals) > maxPerRun {
		individuals = int(maxPerRun - 1)
	}
	for _, value := range values[:individuals] {
		groups = append(groups, messageGroup{Members: []candidate{value}})
	}
	if individuals < len(values) {
		groups = append(groups, messageGroup{Members: values[individuals:], Summary: true})
	}
	return groups, nil
}

func validateFormatCandidate(task service.Task, value candidate) error {
	if value.Finding.ID < 1 || value.Finding.TaskID != task.ID || value.Intent.FindingID != value.Finding.ID || value.Intent.TaskID != task.ID || value.Listing.ItemID == "" || value.Listing.ItemID != value.Finding.ItemID {
		return errFormatMessage
	}
	if notificationRank(value.Intent.Kind) < 0 || value.Intent.Price != nil && *value.Intent.Price < 0 || value.Intent.PreviousPrice != nil && *value.Intent.PreviousPrice < 0 {
		return errFormatMessage
	}
	if value.Intent.Kind == intentPriceDrop || value.Intent.Kind == intentPriceUpgrade {
		if value.Intent.Price == nil || value.Intent.PreviousPrice == nil || *value.Intent.PreviousPrice <= *value.Intent.Price {
			return errFormatMessage
		}
	}
	if value.Finding.Score != nil && (*value.Finding.Score < 0 || *value.Finding.Score > 100) {
		return errFormatMessage
	}
	return nil
}

func notificationRank(kind intentKind) int {
	switch kind {
	case intentRecommended, intentPriceUpgrade:
		return 0
	case intentPriceDrop:
		return 1
	case intentNew:
		return 2
	}
	return -1
}

func greaterKnownInt(a, b *int64) bool {
	return a != nil && (b == nil || *a > *b)
}

type messageRenderer struct {
	language locales.Language
	err      error
}

func (r *messageRenderer) text(code string, args map[string]any) string {
	return r.message(locales.Message{Code: code, Args: args})
}

func (r *messageRenderer) message(message locales.Message) string {
	if r.err != nil {
		return ""
	}
	value, err := locales.Format(r.language, message)
	if err != nil {
		r.err = err
	}
	return value
}

func (r *messageRenderer) money(cents *int64) string {
	if cents == nil {
		return r.text("notification.price_unknown", nil)
	}
	return formatNotificationMoney(*cents)
}

func formatNotificationMoney(cents int64) string {
	amount := new(big.Int).SetInt64(cents)
	negative := amount.Sign() < 0
	amount.Abs(amount)
	whole, fraction := new(big.Int), new(big.Int)
	whole.QuoRem(amount, big.NewInt(100), fraction)
	digits := whole.String()
	for position := len(digits) - 3; position > 0; position -= 3 {
		digits = digits[:position] + "," + digits[position:]
	}
	if fraction.Sign() != 0 {
		digits += "." + strconv.FormatInt(100+fraction.Int64(), 10)[1:]
	}
	if negative {
		digits = "-" + digits
	}
	return "¥" + digits
}

func formatGroup(task service.Task, group messageGroup, channel notify.Type, options formatOptions) (notify.Message, error) {
	if task.ID < 1 || task.JudgeMode != analysis.ModeAI && task.JudgeMode != analysis.ModeKeyword || len(group.Members) == 0 || !group.Summary && len(group.Members) != 1 {
		return notify.Message{}, errFormatMessage
	}
	seen := make(map[int64]bool, len(group.Members))
	for _, value := range group.Members {
		if validateFormatCandidate(task, value) != nil || seen[value.Finding.ID] {
			return notify.Message{}, errFormatMessage
		}
		seen[value.Finding.ID] = true
	}
	r := &messageRenderer{language: options.Language}
	var message notify.Message
	var parts titleParts
	if group.Summary {
		message, parts = r.summary(task, group, options)
	} else {
		message, parts = r.single(task, group.Members[0], options)
	}
	if r.err != nil {
		return notify.Message{}, r.err
	}
	if err := fitNotificationTitle(r, &message, parts, channel); err != nil {
		return notify.Message{}, err
	}
	return message, r.err
}

func (r *messageRenderer) single(task service.Task, value candidate, options formatOptions) (notify.Message, titleParts) {
	finding := value.Finding
	message := notify.Message{Task: task.Name, ItemID: value.Listing.ItemID,
		ItemTitle:   notify.TruncateRunes(notificationSingleLine(value.Listing.Title), 40),
		DesktopLink: value.Listing.DesktopURL, MobileLink: mobileItemLink(value.Listing.ItemID),
		Price: r.money(value.Intent.Price), LinkLabel: r.text("notification.open_xianyu", nil), Footer: r.text("notification.footer", nil),
		Urgent: finding.Grade != nil && *finding.Grade == analysis.GradeSteal}
	message.Link = selectedItemLink(options.LinkTarget, message.DesktopLink, message.MobileLink)
	if options.FindingURL != nil {
		message.KakattaURL = options.FindingURL(finding.ID)
	}
	if value.Listing.ThumbnailURL != nil {
		message.ImageURL = *value.Listing.ThumbnailURL
	}
	if finding.Score != nil {
		message.Score = strconv.FormatInt(*finding.Score, 10)
	}
	if finding.Grade != nil {
		message.Grade = r.text("analysis.grade."+string(*finding.Grade), nil)
	}
	if finding.FairPrice != nil {
		message.FairPrice = r.money(finding.FairPrice)
	}
	spec, fullConfig := r.configuration(task, value.ItemSpecs)
	message.Config = fullConfig
	parts := titleParts{TaskName: notificationSingleLine(task.Name), AmountCents: value.Intent.Price}
	var firstLine, summary string
	switch value.Intent.Kind {
	case intentRecommended, intentPriceUpgrade:
		message.Kind = "recommended"
		summary = findingSummary(finding)
		if task.JudgeMode == analysis.ModeKeyword {
			parts.Action = locales.Message{Code: "notification.action_match"}
			firstLine = r.findingReason(finding)
		} else {
			parts.SpecText, parts.Score = spec, finding.Score
			parts.Action = locales.Message{Code: "notification.action_unpriced"}
			if finding.Grade != nil {
				parts.Action = locales.Message{Code: "analysis.grade." + string(*finding.Grade)}
			}
			if value.Intent.Kind == intentPriceUpgrade {
				parts.Action = locales.Message{Code: "notification.action_upgrade", Args: map[string]any{"grade": r.message(parts.Action)}}
			}
			firstLine = r.recommendationReason(task, value)
		}
		if value.Intent.Kind == intentPriceUpgrade {
			firstLine = notificationJoin(" · ", r.text("notification.price_from_to", map[string]any{"previous": r.money(value.Intent.PreviousPrice), "current": message.Price}), firstLine)
		}
	case intentNew:
		message.Kind = "all_new"
		parts.Action = locales.Message{Code: "notification.action_new"}
		reason := r.findingReason(finding)
		if finding.RejectKind != nil && *finding.RejectKind == analysis.RejectUnmatched {
			reason = r.text("notification.unmatched", map[string]any{"reason": reason})
		}
		firstLine = notificationJoin(" · ", r.gradeScore(finding), reason)
	case intentPriceDrop:
		message.Kind = "price_drop"
		parts.Action = locales.Message{Code: "notification.action_drop"}
		drop := *value.Intent.PreviousPrice - *value.Intent.Price
		parts.AmountCents = &drop
		firstLine = notificationJoin(" · ", r.text("notification.price_arrow", map[string]any{"previous": r.money(value.Intent.PreviousPrice), "current": message.Price}), r.gradeScore(finding))
	}
	message.Body = notificationJoin("\n", firstLine, summary)
	message.Reason, _, _ = strings.Cut(message.Body, "\n")
	return message, parts
}

func (r *messageRenderer) configuration(task service.Task, item *service.TaskItemSpecs) (string, string) {
	if task.JudgeMode != analysis.ModeAI {
		return "", ""
	}
	if len(task.Profile.Specs) == 0 {
		return "", r.text("notification.no_configuration", nil)
	}
	if item == nil {
		return "", ""
	}
	resolved := analysis.ResolveSpecs(task.Profile, item.Specs, item.BrandNew, item.Source)
	if resolved.ConfigKey == nil {
		return "", ""
	}
	values := make([]string, 0, len(task.Profile.Specs))
	for _, spec := range task.Profile.Specs {
		for _, value := range resolved.Specs {
			if value.Name == spec.Name && value.Value != nil {
				values = append(values, notificationSingleLine(*value.Value))
				break
			}
		}
	}
	return strings.Join(values, " "), strings.Join(values, " / ")
}

func (r *messageRenderer) gradeScore(finding service.Finding) string {
	if finding.Grade == nil {
		return ""
	}
	grade := r.text("analysis.grade."+string(*finding.Grade), nil)
	if finding.Score != nil {
		return r.text("notification.action_score", map[string]any{"action": grade, "score": *finding.Score})
	}
	return grade
}

func (r *messageRenderer) findingReason(finding service.Finding) string {
	if finding.Reason != nil {
		return r.message(*finding.Reason)
	}
	if finding.TitleScreenReason != nil {
		return *finding.TitleScreenReason
	}
	return ""
}

func findingSummary(finding service.Finding) string {
	if finding.Summary != nil {
		return *finding.Summary
	}
	if finding.Analysis != nil {
		return finding.Analysis.Summary
	}
	return ""
}

func (r *messageRenderer) recommendationReason(task service.Task, value candidate) string {
	parts := []string{}
	finding := value.Finding
	if value.Intent.Price != nil && finding.FairPrice != nil && *finding.FairPrice > 0 {
		parts = append(parts, r.priceComparison(*value.Intent.Price, *finding.FairPrice))
	} else if finding.FairSample != nil {
		parts = append(parts, r.text("notification.no_comparable_sample", map[string]any{"count": *finding.FairSample}))
	} else {
		parts = append(parts, r.text("analysis.no_fair_price", nil))
	}
	seller := notificationSeller(task, value)
	sellerCode := "notification.seller_" + string(seller.Type)
	if seller.Type == analysis.SellerUnknown {
		sellerCode = "notification.seller_unavailable"
	}
	sellerText := r.text(sellerCode, nil)
	for _, check := range seller.Checks {
		if check.Key == "seller_new" && check.Status == analysis.CheckRisk {
			sellerText = r.text("notification.seller_with_new_account", map[string]any{"seller": sellerText})
		}
	}
	parts = append(parts, sellerText)
	item := analysis.ItemSpecs{}
	if value.ItemSpecs != nil {
		item = value.ItemSpecs.ItemSpecs
	}
	facts := analysis.Facts{}
	if finding.Analysis != nil {
		facts = *finding.Analysis
	}
	// 复用评分层的有效版本与手动覆盖，待确认数量不套界面五句上限。
	questions := analysis.Score(analysis.ScoreInput{Profile: task.Profile, Facts: facts, Item: item, Seller: seller, Overrides: finding.UserOverrides}).Questions
	if len(questions) > 0 {
		names := make([]string, 0, min(2, len(questions)))
		for _, question := range questions[:min(2, len(questions))] {
			for _, risk := range analysis.ResolveRisks(task.Profile) {
				if risk.ID == question.Key {
					name := risk.Name
					for _, definition := range analysis.RiskDictionary() {
						if definition.ID == risk.ID {
							name = r.text("analysis.risk_name."+risk.ID, nil)
							break
						}
					}
					names = append(names, name)
					break
				}
			}
		}
		nameText := strings.Join(names, "")
		if len(names) == 2 {
			nameText = r.text("notification.check_names", map[string]any{"first": names[0], "second": names[1]})
		}
		parts = append(parts, r.text("notification.pending_checks", map[string]any{"count": len(questions), "names": nameText}))
	}
	return notificationJoin(" · ", parts...)
}

func notificationSeller(task service.Task, value candidate) analysis.SellerResult {
	if value.Finding.SellerAssessment != nil {
		return *value.Finding.SellerAssessment
	}
	input := analysis.SellerInput{Available: value.Seller != nil}
	if days := value.Listing.SellerRegistrationDays; days != nil && *days > 0 && uint64(*days) <= uint64(^uint(0)>>1) {
		converted := int(*days)
		input.RegistrationDays = &converted
	}
	if value.Seller != nil {
		input.RatesAvailable = value.Seller.RecentRates != nil
		for _, item := range value.Seller.RecentItems {
			input.RecentItems = append(input.RecentItems, analysis.SellerItem{Title: item.Title, OnSale: item.SaleStatus != nil && *item.SaleStatus == "on_sale"})
		}
		for _, rate := range value.Seller.RecentRates {
			input.RecentRates = append(input.RecentRates, analysis.SellerRate{AsSeller: rate.Role != nil && *rate.Role == "seller", Negative: rate.Kind != nil && (*rate.Kind == "neutral" || *rate.Kind == "bad")})
		}
	}
	var signals []analysis.SellerSignal
	if value.Finding.Analysis != nil {
		signals = value.Finding.Analysis.SellerSignals
	}
	return analysis.JudgeSeller(input, signals, task.Keyword)
}

func (r *messageRenderer) priceComparison(price, fair int64) string {
	if price == fair {
		return r.text("notification.price_equal", nil)
	}
	difference := new(big.Int).Sub(big.NewInt(price), big.NewInt(fair))
	direction := "higher"
	if difference.Sign() < 0 {
		direction = "lower"
	}
	difference.Abs(difference).Mul(difference, big.NewInt(100))
	// 整数计算四舍五入，极大金额也不经过浮点数。
	difference.Add(difference, big.NewInt(fair/2)).Quo(difference, big.NewInt(fair))
	if difference.Sign() == 0 {
		return r.text("notification.price_"+direction+"_under_one_percent", nil)
	}
	return r.text("notification.price_"+direction, map[string]any{"percent": difference.String()})
}

func (r *messageRenderer) summary(task service.Task, group messageGroup, options formatOptions) (notify.Message, titleParts) {
	allRecommended := true
	message := notify.Message{Task: task.Name, Kind: "summary", Footer: r.text("notification.footer", nil)}
	lines := make([]string, 0, min(10, len(group.Members)))
	for index, value := range group.Members {
		allRecommended = allRecommended && (value.Intent.Kind == intentRecommended || value.Intent.Kind == intentPriceUpgrade)
		message.Urgent = message.Urgent || value.Finding.Grade != nil && *value.Finding.Grade == analysis.GradeSteal
		if index < 10 {
			lines = append(lines, notificationJoin(" · ", r.gradeScore(value.Finding), notify.TruncateRunes(notificationSingleLine(value.Listing.Title), 40)))
		}
	}
	action := "notification.summary_alerts"
	if allRecommended {
		action = "notification.summary_recommendations"
	}
	parts := titleParts{TaskName: notificationSingleLine(task.Name), Action: locales.Message{Code: action, Args: map[string]any{"count": len(group.Members)}}}
	message.Body = strings.Join(lines, "\n")
	message.Reason, _, _ = strings.Cut(message.Body, "\n")
	message.Link, message.KakattaURL = options.TaskResultsURL, options.TaskResultsURL
	message.LinkLabel = r.text("notification.open_kakatta", nil)
	if message.Link == "" {
		first := group.Members[0]
		message.Link = selectedItemLink(options.LinkTarget, first.Listing.DesktopURL, mobileItemLink(first.Listing.ItemID))
		message.LinkLabel = r.text("notification.open_xianyu", nil)
	}
	return message, parts
}

func (r *messageRenderer) title(parts titleParts, summary bool) string {
	task := ""
	if parts.TaskName != "" {
		task = r.text("notification.task_prefix", map[string]any{"task": parts.TaskName})
	}
	action := r.message(parts.Action)
	if parts.Score != nil {
		action = r.text("notification.action_score", map[string]any{"action": action, "score": *parts.Score})
	}
	args := map[string]any{"task": task, "action": action}
	code := "notification.title_action"
	if !summary {
		args["price"] = r.money(parts.AmountCents)
		code = "notification.title_basic"
		if parts.SpecText != "" {
			code, args["spec"] = "notification.title_spec", parts.SpecText
		}
	}
	return r.text(code, args)
}

func fitNotificationTitle(r *messageRenderer, message *notify.Message, parts titleParts, channel notify.Type) error {
	message.Title = r.title(parts, message.Kind == "summary")
	if r.err != nil {
		return r.err
	}
	if channel == notify.Telegram && message.ImageURL != "" {
		// 图片说明发送时只裁商品标题，先确认其余内容放得下。
		if notify.TelegramContentUnits(*message, "") > notify.TelegramCaptionLimit {
			return errFormatMessage
		}
		return nil
	}
	fits := func() bool {
		switch channel {
		case notify.ServerChan:
			return utf8.RuneCountInString(message.Title) <= notify.ServerChanTitleRunes
		case notify.WeCom:
			return message.Link == "" || len(notificationJoin(" · ", message.Title, message.ItemTitle)) <= notify.WeComTitleBytes
		case notify.Telegram:
			return notify.TelegramContentUnits(*message, message.ItemTitle) <= notify.TelegramTextLimit
		}
		return true
	}
	fields := []*string{&parts.SpecText, &parts.TaskName, &message.ItemTitle}
	if channel == notify.WeCom {
		fields[1], fields[2] = fields[2], fields[1]
	}
	for _, field := range fields {
		for !fits() && *field != "" {
			*field = shortenNotificationText(*field)
			message.Title = r.title(parts, message.Kind == "summary")
		}
	}
	if r.err != nil {
		return r.err
	}
	if !fits() {
		return errFormatMessage
	}
	return nil
}

func shortenNotificationText(value string) string {
	runes := []rune(value)
	if len(runes) <= 1 {
		return ""
	}
	return string(runes[:len(runes)-2]) + "…"
}

func notificationJoin(separator string, values ...string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, separator)
}

func formatSystem(event service.SystemEvent, options formatOptions, link string, browserHeldUntil *int64) (notify.Message, error) {
	if event.Kind == nil || event.Message == nil || options.Location == nil {
		return notify.Message{}, errFormatMessage
	}
	r := &messageRenderer{language: options.Language}
	args := make(map[string]any, len(event.Message.Args))
	for key, value := range event.Message.Args {
		args[key] = value
	}
	kind := string(*event.Kind)
	var textKeys, numberKeys []string
	switch *event.Kind {
	case service.EventAccountVerify:
		textKeys, numberKeys = []string{"name"}, []string{"count"}
	case service.EventAccountDeny:
		textKeys, numberKeys = []string{"name"}, []string{"hours"}
		until, ok := formatIntArg(args["until"])
		if !ok || until < 1 {
			return notify.Message{}, errFormatMessage
		}
		args["until"] = time.UnixMilli(until).In(options.Location).Format("15:04")
	case service.EventAccountLogin:
		textKeys, numberKeys = []string{"name"}, []string{"count"}
	case service.EventAIError, service.EventTaskFailurePause, service.EventXianyuLayout:
		if *event.Kind == service.EventAIError {
			textKeys = []string{"provider"}
		} else if *event.Kind == service.EventTaskFailurePause {
			textKeys, numberKeys = []string{"task"}, []string{"count", "minutes"}
		}
		reason, err := formatNestedMessage(args["reason"])
		if err != nil {
			return notify.Message{}, err
		}
		args["reason"] = r.message(reason)
	case service.EventAIBudget:
		numberKeys = []string{"tokens"}
	case service.EventLoginFailures:
		textKeys, numberKeys = []string{"ip"}, []string{"count"}
	default:
		return notify.Message{}, errFormatMessage
	}
	for _, key := range textKeys {
		if text, ok := args[key].(string); !ok || strings.TrimSpace(text) == "" {
			return notify.Message{}, errFormatMessage
		}
	}
	for _, key := range numberKeys {
		number, ok := formatIntArg(args[key])
		if !ok || number < 0 {
			return notify.Message{}, errFormatMessage
		}
		args[key] = number
	}
	titleArgs := make(map[string]any, len(args))
	bodyArgs := make(map[string]any, len(args))
	for key, value := range args {
		titleArgs[key], bodyArgs[key] = value, value
	}
	for _, key := range []string{"name", "task", "provider"} {
		if value, ok := titleArgs[key].(string); ok {
			titleArgs[key] = notificationSingleLine(value)
		}
	}
	switch *event.Kind {
	case service.EventAccountDeny:
		bodyArgs["count"] = args["hours"]
	case service.EventAIBudget:
		bodyArgs["count"] = args["tokens"]
	case service.EventTaskFailurePause:
		bodyArgs["count"] = args["minutes"]
	}
	message := notify.Message{Kind: "system", Urgent: true, Link: link, KakattaURL: link,
		Title: r.text("notification.system."+kind+".title", titleArgs), Body: r.text("notification.system."+kind+".body", bodyArgs),
		LinkLabel: r.text("notification.open_kakatta", nil), Footer: r.text("notification.footer", nil)}
	if *event.Kind == service.EventAccountVerify && browserHeldUntil != nil {
		if *browserHeldUntil < 1 {
			return notify.Message{}, errFormatMessage
		}
		message.Body = notificationJoin(" ", message.Body, r.text("notification.system.browser_held", map[string]any{"until": time.UnixMilli(*browserHeldUntil).In(options.Location).Format("15:04")}))
	}
	if *event.Kind == service.EventAccountDeny {
		environment, ok := args["environment"].(bool)
		if !ok {
			return notify.Message{}, errFormatMessage
		}
		if environment {
			message.Body = notificationJoin(" ", message.Body, r.text("notification.system.environment", nil))
		}
	}
	if *event.Kind == service.EventAccountLogin {
		count, ok := formatIntArg(args["count"])
		if !ok || count < 0 {
			return notify.Message{}, errFormatMessage
		}
		if count > 0 {
			message.Body = notificationJoin(" ", message.Body, r.text("notification.system.reassigned", args))
		}
	}
	message.Reason, _, _ = strings.Cut(message.Body, "\n")
	if r.err != nil {
		return notify.Message{}, r.err
	}
	return message, nil
}

func formatNestedMessage(value any) (locales.Message, error) {
	if message, ok := value.(locales.Message); ok && message.Code != "" {
		return message, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return locales.Message{}, errFormatMessage
	}
	message, err := decodeNotificationMessage(encoded)
	if err != nil || message == nil || message.Code == "" {
		return locales.Message{}, errFormatMessage
	}
	return *message, nil
}

func formatIntArg(value any) (int64, bool) {
	switch number := value.(type) {
	case int64:
		return number, true
	case int:
		return int64(number), true
	case json.Number:
		parsed, err := number.Int64()
		return parsed, err == nil
	case float64:
		if number >= 0 && number < 1<<63 && number == float64(int64(number)) {
			return int64(number), true
		}
	}
	return 0, false
}

func testMessage(language locales.Language) (notify.Message, error) {
	r := &messageRenderer{language: language}
	message := notify.Message{Kind: "test", Title: r.text("notification.test_title", nil), Body: r.text("notification.test_body", nil),
		Link: "https://www.goofish.com/", DesktopLink: "https://www.goofish.com/", MobileLink: "https://www.goofish.com/",
		ImageURL:  notificationSampleImageURL,
		LinkLabel: r.text("notification.open_xianyu", nil), Footer: r.text("notification.footer", nil)}
	message.Reason = message.Body
	if r.err != nil {
		return notify.Message{}, r.err
	}
	return message, nil
}
