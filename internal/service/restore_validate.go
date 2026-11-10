package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/settings"
)

// ValidateStored 只检查迁移后的候选库，不启动服务或改变任何历史记录。
func ValidateStored(ctx context.Context, db *sql.DB, cipher *security.Cipher, env settings.Environment) error {
	if db == nil || cipher == nil {
		return errors.New("候选核心校验缺少数据库或密钥")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	snapshot, err := appsettings.ValidateStoredTx(ctx, tx, env)
	if err != nil {
		return err
	}
	checks := []struct {
		name  string
		check func() error
	}{
		{"额度时钟", func() error {
			if _, err := readQuotaClockTx(ctx, tx); err != nil {
				return storedInvalid("meta", appsettings.ClockKey, "value")
			}
			return nil
		}},
		{"任务", func() error { return validateStoredTasks(ctx, tx, snapshot) }},
		{"账号代理", func() error { return validateStoredAccounts(ctx, tx, cipher) }},
		{"商品", func() error { return validateStoredListings(ctx, tx) }},
		{"发现", func() error { return validateStoredFindings(ctx, tx) }},
		{"日志行情", func() error { return validateStoredLogs(ctx, tx) }},
		{"内部记录", func() error { return validateStoredMeta(ctx, tx) }},
	}
	for _, check := range checks {
		if err := check.check(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("候选库%s校验失败：%w", check.name, err)
		}
	}
	return ctx.Err()
}

func storedInvalid(table, key, field string) error {
	return fmt.Errorf("%s 记录 %s 的 %s 无效", table, key, field)
}

func storedRows(ctx context.Context, tx *sql.Tx, query string, visit func(*sql.Rows) error) error {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := visit(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func storedIDs(ctx context.Context, tx *sql.Tx, query string) ([]int64, error) {
	var ids []int64
	err := storedRows(ctx, tx, query, func(rows *sql.Rows) error {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
		return nil
	})
	return ids, err
}

func storedKeys(ctx context.Context, tx *sql.Tx, query string) ([]string, error) {
	var keys []string
	err := storedRows(ctx, tx, query, func(rows *sql.Rows) error {
		var key string
		if err := rows.Scan(&key); err != nil {
			return err
		}
		keys = append(keys, key)
		return nil
	})
	return keys, err
}

func storedJSON(raw sql.NullString, prefix byte, target any) error {
	if !raw.Valid {
		return nil
	}
	trimmed := strings.TrimSpace(raw.String)
	if trimmed == "" || trimmed[0] != prefix || !utf8.ValidString(raw.String) {
		return errors.New("保存的对象或数组格式无效")
	}
	// 未识别的历史字段和消息参数只保留，不赋予新的业务含义。
	decoder := json.NewDecoder(strings.NewReader(raw.String))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("保存的记录包含多份数据")
	}
	return nil
}

func storedStrings(raw sql.NullString) error {
	var values []*string
	if err := storedJSON(raw, '[', &values); err != nil {
		return err
	}
	for _, value := range values {
		if value == nil {
			return errors.New("文字数组包含空值")
		}
	}
	return nil
}

func validateStoredTasks(ctx context.Context, tx *sql.Tx, global settings.Snapshot) error {
	ids, err := storedIDs(ctx, tx, `SELECT id FROM tasks ORDER BY id`)
	if err != nil {
		return err
	}
	for _, id := range ids {
		key := strconv.FormatInt(id, 10)
		// 消息参数单独用 UseNumber 检查，配置读取不经过浮点数转换。
		columns := strings.Replace(taskColumns, ",last_error,", ",NULL,", 1)
		task, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM tasks WHERE id=?`, id))
		if err != nil {
			return storedInvalid("tasks", key, "json")
		}
		if err := loadTaskChannelsTx(ctx, tx, &task); err != nil {
			return err
		}
		if err := validateTaskReferencesTx(ctx, tx, task.TaskFields); err != nil {
			return storedInvalid("tasks", key, "references")
		}
		raw, err := json.Marshal(task.CrawlOverrides)
		if err != nil {
			return storedInvalid("tasks", key, "crawl_overrides_json")
		}
		if _, _, err := settings.ResolveTask(global, task.IntervalMinutes, raw); err != nil {
			return storedInvalid("tasks", key, "crawl_overrides_json")
		}
		if err := validateStoredProfile(task.Profile); err != nil {
			return storedInvalid("tasks", key, "profile_json")
		}
		if task.JudgeMode == analysis.ModeAI && len(task.Profile.Specs) == 0 && len(task.Profile.Musts) == 0 {
			return storedInvalid("tasks", key, "profile_json")
		}
		if _, err := loadTaskProfileWatermarkTx(ctx, tx, task); err != nil {
			return storedInvalid("tasks", key, "profile_next_no")
		}
		if task.SearchGeneration < 1 || task.ConsecutiveFailures < 0 || task.MaxPages < 1 || !taskNotifyPolicyValid(task.JudgeMode, task.NotifyPolicy) {
			return storedInvalid("tasks", key, "state")
		}
		for _, value := range []*int64{task.MinPrice, task.MaxPrice, task.BudgetMin, task.BudgetMax} {
			if value != nil && (*value < 0 || *value > limits.MaxSafeInteger) {
				return storedInvalid("tasks", key, "price")
			}
		}
		if task.MinPrice != nil && task.MaxPrice != nil && *task.MinPrice > *task.MaxPrice {
			return storedInvalid("tasks", key, "price_range")
		}
		if task.BudgetMin != nil && task.BudgetMax != nil && *task.BudgetMin > *task.BudgetMax {
			return storedInvalid("tasks", key, "budget")
		}
		var message sql.NullString
		var overrides, profile, matches, excludes string
		if err := tx.QueryRowContext(ctx, `SELECT last_error,crawl_overrides_json,profile_json,match_keywords_json,exclude_keywords_json FROM tasks WHERE id=?`, id).Scan(&message, &overrides, &profile, &matches, &excludes); err != nil {
			return err
		}
		for _, raw := range []string{overrides, profile, matches, excludes} {
			if !utf8.ValidString(raw) {
				return storedInvalid("tasks", key, "json")
			}
		}
		for _, raw := range []string{matches, excludes} {
			if err := storedStrings(sql.NullString{String: raw, Valid: true}); err != nil {
				return storedInvalid("tasks", key, "keywords_json")
			}
		}
		if _, err := logMessage(message); err != nil {
			return storedInvalid("tasks", key, "last_error")
		}
	}
	return nil
}

func storedRevision(rev int64) bool { return rev >= 0 && rev <= limits.MaxSafeInteger }
func storedCheck(status analysis.CheckStatus) bool {
	return status == analysis.CheckOK || status == analysis.CheckRisk || status == analysis.CheckSuspect || status == analysis.CheckUnknown
}
func storedSource(source *string) bool {
	return source == nil || *source == "text" || *source == "image"
}
func storedKind(kind analysis.Kind) bool {
	switch kind {
	case analysis.KindSale, analysis.KindWanted, analysis.KindAccessory, analysis.KindRecycle, analysis.KindRental, analysis.KindRepair, analysis.KindBox, analysis.KindMulti, analysis.KindBroken, analysis.KindOther:
		return true
	}
	return false
}

func validateStoredProfile(profile analysis.Profile) error {
	bad := errors.New("清单格式无效")
	if profile.NextNo < 0 || int64(profile.NextNo) > limits.MaxSafeInteger {
		return bad
	}
	ids := map[string]bool{}
	reserve := func(id string, prefix byte) bool {
		_, valid := taskProfileID(id, prefix)
		if !valid || ids[id] {
			return false
		}
		ids[id] = true
		return true
	}
	for i, requirements := range [][]analysis.Requirement{profile.Musts, profile.Bonuses} {
		prefix := byte('m')
		if i == 1 {
			prefix = 'b'
		}
		for _, r := range requirements {
			if !reserve(r.ID, prefix) || strings.TrimSpace(r.Text) == "" || !storedRevision(r.Rev) || (r.Source != analysis.SourceUser && r.Source != analysis.SourceAI) {
				return bad
			}
		}
	}
	dictionary := map[string]bool{}
	for _, r := range analysis.RiskDictionary() {
		dictionary[r.ID] = true
	}
	for _, r := range profile.Risks {
		if !storedRevision(r.Rev) || (r.Attitude != analysis.Confirm && r.Attitude != analysis.Reject && r.Attitude != analysis.Mind && r.Attitude != analysis.Ignore) || (r.Source != analysis.SourceUser && r.Source != analysis.SourceAI && r.Source != analysis.SourceSystem) {
			return bad
		}
		if dictionary[r.ID] {
			if ids[r.ID] {
				return bad
			}
			ids[r.ID] = true
		} else if !reserve(r.ID, 'c') || strings.TrimSpace(r.Name) == "" {
			return bad
		}
	}
	names := map[string]bool{}
	for _, spec := range profile.Specs {
		if strings.TrimSpace(spec.Name) == "" || names[spec.Name] || (spec.Source != analysis.SourceUser && spec.Source != analysis.SourceAI) {
			return bad
		}
		names[spec.Name] = true
		for _, option := range spec.Options {
			if strings.TrimSpace(option) == "" {
				return bad
			}
		}
	}
	for _, kind := range profile.AllowedKinds {
		if !storedKind(kind) {
			return bad
		}
	}
	return nil
}

func validateStoredAccounts(ctx context.Context, tx *sql.Tx, cipher *security.Cipher) error {
	ids, err := storedIDs(ctx, tx, `SELECT id FROM proxies ORDER BY id`)
	if err != nil {
		return err
	}
	proxies := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if _, err := loadProxyURLTx(ctx, tx, cipher, id); err != nil {
			return storedInvalid("proxies", strconv.FormatInt(id, 10), "url_enc")
		}
		proxies[id] = true
	}
	return storedRows(ctx, tx, `SELECT id,proxy_id,verify_level,next_gap_seconds,state_reason,state,cooldown_until FROM accounts`, func(rows *sql.Rows) error {
		var id, level int64
		var proxy, gap, deadline *int64
		var raw sql.NullString
		var state AccountState
		if err := rows.Scan(&id, &proxy, &level, &gap, &raw, &state, &deadline); err != nil {
			return err
		}
		key := strconv.FormatInt(id, 10)
		if id < 1 || id > maxAccountID || level < 0 || gap != nil && *gap < 0 {
			return storedInvalid("accounts", key, "state")
		}
		if (state == AccountCooling || state == AccountDenied) && deadline == nil {
			return storedInvalid("accounts", key, "cooldown_until")
		}
		if proxy != nil && !proxies[*proxy] {
			return storedInvalid("accounts", key, "proxy_id")
		}
		if _, err := logMessage(raw); err != nil {
			return storedInvalid("accounts", key, "state_reason")
		}
		return nil
	})
}

func validateStoredListings(ctx context.Context, tx *sql.Tx) error {
	if err := storedRows(ctx, tx, `SELECT item_id,price,image_urls_json,tags_json FROM listings`, func(rows *sql.Rows) error {
		var key string
		var price *int64
		var images, tags sql.NullString
		if err := rows.Scan(&key, &price, &images, &tags); err != nil {
			return err
		}
		if strings.TrimSpace(key) == "" || price != nil && *price < 0 {
			return storedInvalid("listings", key, "item_id/price")
		}
		for _, raw := range []sql.NullString{images, tags} {
			if err := storedStrings(raw); err != nil {
				return storedInvalid("listings", key, "json")
			}
		}
		return nil
	}); err != nil {
		return err
	}
	var badPriceID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM price_points WHERE price<0 LIMIT 1`).Scan(&badPriceID); err == nil {
		return storedInvalid("price_points", strconv.FormatInt(badPriceID, 10), "price")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// 观察记录会保住商品；永久去重和保留规格不增加商品外键。
	var missingItem string
	if err := tx.QueryRowContext(ctx, `SELECT item_id FROM task_sightings s WHERE NOT EXISTS(SELECT 1 FROM listings l WHERE l.item_id=s.item_id) LIMIT 1`).Scan(&missingItem); err == nil {
		return storedInvalid("task_sightings", missingItem, "item_id")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	keys, err := storedKeys(ctx, tx, `SELECT seller_id FROM seller_profiles ORDER BY seller_id`)
	if err != nil {
		return err
	}
	for _, key := range keys {
		seller, err := loadSellerTx(ctx, tx, key)
		if err != nil || seller == nil {
			return storedInvalid("seller_profiles", key, "json")
		}
		var items, rates sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT recent_items_json,recent_rates_json FROM seller_profiles WHERE seller_id=?`, key).Scan(&items, &rates); err != nil {
			return err
		}
		if err := storedJSON(items, '[', &[]SellerRecentItem{}); err != nil {
			return storedInvalid("seller_profiles", key, "recent_items_json")
		}
		if err := storedJSON(rates, '[', &[]SellerRecentRate{}); err != nil {
			return storedInvalid("seller_profiles", key, "recent_rates_json")
		}
		for _, rate := range []*float64{seller.SellerGoodRate, seller.BuyerGoodRate} {
			if rate != nil && (*rate < 0 || *rate > 1) {
				return storedInvalid("seller_profiles", key, "good_rate")
			}
		}
		for _, item := range seller.RecentItems {
			if item.SaleStatus != nil && *item.SaleStatus != "on_sale" && *item.SaleStatus != "sold" {
				return storedInvalid("seller_profiles", key, "recent_items_json")
			}
		}
		for _, rate := range seller.RecentRates {
			if rate.Role != nil && *rate.Role != "seller" && *rate.Role != "buyer" || rate.Kind != nil && *rate.Kind != "good" && *rate.Kind != "neutral" && *rate.Kind != "bad" {
				return storedInvalid("seller_profiles", key, "recent_rates_json")
			}
		}
	}
	if err := storedRows(ctx, tx, `SELECT task_id,item_id,specs_json FROM task_item_specs`, func(rows *sql.Rows) error {
		var taskID int64
		var itemID string
		var raw sql.NullString
		var specs []analysis.SpecValue
		if err := rows.Scan(&taskID, &itemID, &raw); err != nil {
			return err
		}
		if err := storedJSON(raw, '[', &specs); err != nil {
			return storedInvalid("task_item_specs", strconv.FormatInt(taskID, 10), "specs_json")
		}
		for _, spec := range specs {
			if strings.TrimSpace(spec.Name) == "" {
				return storedInvalid("task_item_specs", strconv.FormatInt(taskID, 10), "specs_json")
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return storedRows(ctx, tx, `SELECT task_id,item_id,day FROM task_sightings`, func(rows *sql.Rows) error {
		var id int64
		var item, day string
		if err := rows.Scan(&id, &item, &day); err != nil {
			return err
		}
		if _, err := time.Parse("2006-01-02", day); err != nil || item == "" {
			return storedInvalid("task_sightings", strconv.FormatInt(id, 10), "day/item_id")
		}
		return nil
	})
}

func validateStoredFacts(facts *analysis.Facts) error {
	if facts == nil {
		return errors.New("事实不是对象")
	}
	for _, set := range [][]analysis.RequirementFact{facts.Musts, facts.Bonuses} {
		for _, fact := range set {
			if fact.ID == "" || !storedRevision(fact.Rev) || !storedSource(fact.Source) || (fact.Status != analysis.ReqMet && fact.Status != analysis.ReqUnmet && fact.Status != analysis.ReqUnknown) {
				return errors.New("需求事实格式无效")
			}
		}
	}
	for _, fact := range facts.Checks {
		if fact.Key == "" || !storedRevision(fact.Rev) || !storedSource(fact.Source) || !storedCheck(fact.Status) {
			return errors.New("风险事实格式无效")
		}
	}
	for _, spec := range facts.Specs {
		if strings.TrimSpace(spec.Name) == "" {
			return errors.New("规格事实格式无效")
		}
	}
	for _, signal := range facts.SellerSignals {
		switch signal.Key {
		case "desc_mismatch", "after_sale", "slow_ship", "partial_refund", "dealer_words":
		default:
			return errors.New("卖家信号格式无效")
		}
	}
	for _, extra := range facts.ExtraFindings {
		if strings.TrimSpace(extra.Name) == "" || (extra.Source != "text" && extra.Source != "image") {
			return errors.New("清单外发现格式无效")
		}
	}
	return nil
}

func validateStoredFindings(ctx context.Context, tx *sql.Tx) error {
	return storedRows(ctx, tx, `SELECT id,reason,analysis_json,user_overrides_json,score_breakdown_json,fair_basis,matched_keywords_json,notify_skip_reason,seller_checks_json,attempts,search_generation FROM findings`, func(rows *sql.Rows) error {
		var id, attempts, generation int64
		var reason, facts, overrides, breakdown, basis, keywords, skip, seller sql.NullString
		if err := rows.Scan(&id, &reason, &facts, &overrides, &breakdown, &basis, &keywords, &skip, &seller, &attempts, &generation); err != nil {
			return err
		}
		key := strconv.FormatInt(id, 10)
		if attempts < 0 || generation < 1 {
			return storedInvalid("findings", key, "state")
		}
		for _, field := range []struct {
			name string
			raw  sql.NullString
		}{{"reason", reason}, {"fair_basis", basis}, {"notify_skip_reason", skip}} {
			if _, err := logMessage(field.raw); err != nil {
				return storedInvalid("findings", key, field.name)
			}
		}
		if facts.Valid {
			value, _, err := decodeFindingFacts(facts.String)
			if err != nil || validateStoredFacts(value) != nil {
				return storedInvalid("findings", key, "analysis_json")
			}
		}
		var value analysis.UserOverrides
		if err := storedJSON(overrides, '{', &value); err != nil {
			return storedInvalid("findings", key, "user_overrides_json")
		}
		for id, risk := range value.Risks {
			if id == "" || !storedRevision(risk.Rev) || !storedCheck(risk.Status) {
				return storedInvalid("findings", key, "user_overrides_json")
			}
		}
		var score analysis.ScoreBreakdown
		if err := storedJSON(breakdown, '{', &score); err != nil {
			return storedInvalid("findings", key, "score_breakdown_json")
		}
		for _, item := range score.Items {
			if item.Reason.Code == "" {
				return storedInvalid("findings", key, "score_breakdown_json")
			}
		}
		for _, cap := range score.Caps {
			if cap.Reason.Code == "" {
				return storedInvalid("findings", key, "score_breakdown_json")
			}
		}
		for _, gate := range score.Gates {
			if gate.Code == "" {
				return storedInvalid("findings", key, "score_breakdown_json")
			}
		}
		if err := storedStrings(keywords); err != nil {
			return storedInvalid("findings", key, "matched_keywords_json")
		}
		if seller.Valid {
			if _, err := decodeSellerAssessment(seller.String); err != nil {
				return storedInvalid("findings", key, "seller_checks_json")
			}
		}
		return nil
	})
}

func validateStoredLogs(ctx context.Context, tx *sql.Tx) error {
	if err := storedRows(ctx, tx, `SELECT `+runColumns+` FROM run_records r`, func(rows *sql.Rows) error {
		_, err := scanRun(rows)
		if err != nil {
			return errors.New("run_records 的记录格式无效")
		}
		return nil
	}); err != nil {
		return err
	}
	if err := storedRows(ctx, tx, `SELECT run_id,seq,message,detail FROM run_steps`, func(rows *sql.Rows) error {
		var id, seq int64
		var message, detail sql.NullString
		if err := rows.Scan(&id, &seq, &message, &detail); err != nil {
			return err
		}
		if seq < 1 {
			return storedInvalid("run_steps", strconv.FormatInt(id, 10), "seq")
		}
		for _, raw := range []sql.NullString{message, detail} {
			if _, err := logMessage(raw); err != nil {
				return storedInvalid("run_steps", strconv.FormatInt(id, 10), "message/detail")
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := storedRows(ctx, tx, `SELECT id,message FROM system_events`, func(rows *sql.Rows) error {
		var id int64
		var raw sql.NullString
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		if _, err := logMessage(raw); err != nil {
			return storedInvalid("system_events", strconv.FormatInt(id, 10), "message")
		}
		return nil
	}); err != nil {
		return err
	}
	return storedRows(ctx, tx, `SELECT id,stats_json FROM market_insights`, func(rows *sql.Rows) error {
		var id int64
		var raw sql.NullString
		var stats analysis.MarketResult
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		if err := storedJSON(raw, '{', &stats); err != nil {
			return storedInvalid("market_insights", strconv.FormatInt(id, 10), "stats_json")
		}
		for _, warning := range stats.Warnings {
			if warning.Code == "" {
				return storedInvalid("market_insights", strconv.FormatInt(id, 10), "stats_json")
			}
		}
		return nil
	})
}

func validateStoredMeta(ctx context.Context, tx *sql.Tx) error {
	markers, err := listSessionMarkersTx(ctx, tx)
	if err != nil {
		return storedInvalid("meta", "runtime.session", "value")
	}
	for id, marker := range markers {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE id=?)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return storedInvalid("meta", strconv.FormatInt(id, 10), "runtime.session")
		}
		if marker.RunID != nil {
			var accountID *int64
			if err := tx.QueryRowContext(ctx, `SELECT account_id FROM run_records WHERE id=?`, *marker.RunID).Scan(&accountID); err != nil || accountID == nil || *accountID != id {
				return storedInvalid("meta", strconv.FormatInt(id, 10), "runtime.session.run_id")
			}
		}
	}
	type record struct{ key, value string }
	var records []record
	if err := storedRows(ctx, tx, `SELECT key,value FROM meta WHERE key GLOB 'task.latest_search.*' OR key GLOB 'task.profile_next_no.*' OR key GLOB 'account.cleanup.*' OR key GLOB 'account.logged_out.*' OR key='stats.started_at' OR key IN ('stats.coverage.findings','stats.coverage.run_records','stats.coverage.run_skip_events','stats.coverage.task_sightings','stats.coverage.ai_calls','stats.coverage.deliveries','stats.coverage.notification_intents')`, func(rows *sql.Rows) error {
		var r record
		if err := rows.Scan(&r.key, &r.value); err != nil {
			return err
		}
		records = append(records, r)
		return nil
	}); err != nil {
		return err
	}
	for _, r := range records {
		if strings.HasPrefix(r.key, "stats.") {
			value, err := strconv.ParseInt(r.value, 10, 64)
			if err != nil || value <= 0 || strconv.FormatInt(value, 10) != r.value {
				return storedInvalid("meta", r.key, "value")
			}
			continue
		}
		prefix := ""
		for _, candidate := range []string{latestSearchPrefix, taskProfileWatermarkPrefix, accountCleanupPrefix, accountLoggedOutPrefix} {
			if strings.HasPrefix(r.key, candidate) {
				prefix = candidate
				break
			}
		}
		rawID := strings.TrimPrefix(r.key, prefix)
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || id < 1 || strconv.FormatInt(id, 10) != rawID {
			return storedInvalid("meta", r.key, "key")
		}
		if prefix == accountCleanupPrefix || prefix == accountLoggedOutPrefix {
			if id > maxAccountID || r.value != "1" {
				return storedInvalid("meta", r.key, "value")
			}
			if prefix == accountLoggedOutPrefix {
				var exists bool
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE id=?)`, id).Scan(&exists); err != nil {
					return err
				}
				if !exists {
					return storedInvalid("meta", r.key, "account")
				}
			}
			continue
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=?)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return storedInvalid("meta", r.key, "task")
		}
		if prefix == latestSearchPrefix {
			if _, err := loadLatestSearchItemIDsTx(ctx, tx, id, 0); err != nil {
				return storedInvalid("meta", r.key, "value")
			}
		} else {
			value, err := strconv.Atoi(r.value)
			if err != nil || value < 1 || strconv.Itoa(value) != r.value {
				return storedInvalid("meta", r.key, "value")
			}
		}
	}
	return nil
}
