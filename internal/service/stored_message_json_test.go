package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
)

const storedPrecisionMessage = `{"code":"historical.precision","args":{"integer":9007199254740993,"huge":1e1000,"decimal":0.10000000000000000000000001,"nested":{"values":[9223372036854775808,null,{"future_key":1e1000}]},"future_arg":true}}`

func storedPrecisionHarness(t *testing.T) (findingsHarness, WorkRef) {
	t.Helper()
	h := newFindingsHarness(t)
	h.s.initRuntime()
	t.Cleanup(func() { h.s.cancel(errServiceClosed) })
	ref := h.task(analysis.ModeKeyword, false)
	return h, h.finding(ref, "precision", StageDone, analysis.DecisionMatched, h.now)
}

func assertStoredNumber(t *testing.T, value any, want string) {
	t.Helper()
	number, ok := value.(json.Number)
	if !ok || number.String() != want {
		t.Fatalf("存储数字被改变：%T %v，预期%s", value, value, want)
	}
}

func assertStoredPrecisionMessage(t *testing.T, message *locales.Message) {
	t.Helper()
	if message == nil || message.Code != "historical.precision" {
		t.Fatal("历史消息没有原样读出", message)
	}
	assertStoredNumber(t, message.Args["integer"], "9007199254740993")
	assertStoredNumber(t, message.Args["huge"], "1e1000")
	assertStoredNumber(t, message.Args["decimal"], "0.10000000000000000000000001")
	nested, ok := message.Args["nested"].(map[string]any)
	if !ok {
		t.Fatal("嵌套对象丢失")
	}
	values, ok := nested["values"].([]any)
	if !ok || len(values) != 3 || values[1] != nil {
		t.Fatal("嵌套数组或空值改变", nested)
	}
	assertStoredNumber(t, values[0], "9223372036854775808")
	future, ok := values[2].(map[string]any)
	if !ok {
		t.Fatal("未知参数对象丢失")
	}
	assertStoredNumber(t, future["future_key"], "1e1000")
	if message.Args["future_arg"] != true {
		t.Fatal("未知参数键被丢弃")
	}
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"9007199254740993", "1e1000", "0.10000000000000000000000001", "9223372036854775808"} {
		if !strings.Contains(string(raw), token) {
			t.Fatal("响应序列化改变数字", string(raw))
		}
	}
}

func TestStoredMessageNumbersFindingAndNotificationRead(t *testing.T) {
	for _, test := range []struct {
		name, column, raw string
		messages          func(*testing.T, Finding) []*locales.Message
	}{
		{"reason", "reason", storedPrecisionMessage, func(_ *testing.T, f Finding) []*locales.Message { return []*locales.Message{f.Reason} }},
		{"fair_basis", "fair_basis", storedPrecisionMessage, func(_ *testing.T, f Finding) []*locales.Message { return []*locales.Message{f.FairBasis} }},
		{"notify_skip_reason", "notify_skip_reason", storedPrecisionMessage, func(_ *testing.T, f Finding) []*locales.Message { return []*locales.Message{f.NotifySkipReason} }},
		{"breakdown", "score_breakdown_json", `{"base":{"score":41.25,"d":0.125,"fair_price":1234,"sample":3,"basis":` + storedPrecisionMessage + `},"items":[{"id":"old","value":2,"reason":` + storedPrecisionMessage + `}],"caps":[{"limit":89,"reason":` + storedPrecisionMessage + `}],"gates":[` + storedPrecisionMessage + `],"historical_extension":{"raw":1e1000}}`, func(t *testing.T, f Finding) []*locales.Message {
			if f.ScoreBreakdown == nil {
				t.Fatal("分项未读出")
			}
			b := f.ScoreBreakdown
			if b.Base == nil || b.Base.Score != 41.25 || b.Base.FairPrice != 1234 || b.Base.Sample != 3 || b.Items[0].Value != 2 || b.Caps[0].Limit != 89 {
				t.Fatal("有类型的业务数字改变", b)
			}
			return []*locales.Message{&b.Base.Basis, &b.Items[0].Reason, &b.Caps[0].Reason, &b.Gates[0]}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, work := storedPrecisionHarness(t)
			h.exec(`UPDATE findings SET `+test.column+`=? WHERE id=?`, test.raw, work.FindingID)
			h.exec(`PRAGMA query_only=ON`)
			view, err := h.s.GetFinding(t.Context(), work.FindingID, FindingQuery{})
			if err != nil {
				t.Fatal("结果页读取失败", err)
			}
			for _, message := range test.messages(t, view.Finding) {
				assertStoredPrecisionMessage(t, message)
			}
			tx := h.tx()
			defer tx.Rollback()
			candidate, err := NotificationFindingTx(t.Context(), tx, work.FindingID)
			if err != nil {
				t.Fatal("通知读取失败", err)
			}
			for _, message := range test.messages(t, candidate.Finding) {
				assertStoredPrecisionMessage(t, message)
			}
		})
	}
}

func TestStoredMessageNumbersSellerAssessmentRead(t *testing.T) {
	h, work := storedPrecisionHarness(t)
	assessment := sellerResultForFinding(Listing{}, nil, nil, "camera")
	assessment.Reasons = []locales.Message{{Code: "historical.precision", Args: map[string]any{
		"integer": json.Number("9007199254740993"), "huge": json.Number("1e1000"), "decimal": json.Number("0.10000000000000000000000001"),
		"nested": map[string]any{"values": []any{json.Number("9223372036854775808"), nil, map[string]any{"future_key": json.Number("1e1000")}}}, "future_arg": true,
	}}}
	raw, err := sellerAssessmentJSON(assessment)
	if err != nil {
		t.Fatal(err)
	}
	h.exec(`UPDATE findings SET seller_checks_json=? WHERE id=?`, raw, work.FindingID)
	h.exec(`PRAGMA query_only=ON`)
	view, err := h.s.GetFinding(t.Context(), work.FindingID, FindingQuery{})
	if err != nil {
		t.Fatal("卖家历史判断读取失败", err)
	}
	if view.SellerAssessment == nil || len(view.SellerAssessment.Reasons) != 1 {
		t.Fatal("卖家原因丢失")
	}
	assertStoredPrecisionMessage(t, &view.SellerAssessment.Reasons[0])
}

func TestStoredMessageNumbersDeliveryRead(t *testing.T) {
	h, work := storedPrecisionHarness(t)
	result := h.exec(`INSERT INTO deliveries(at,kind,error) VALUES(?,'recommended',?)`, h.now, storedPrecisionMessage)
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	h.exec(`INSERT INTO delivery_items(delivery_id,finding_id) VALUES(?,?)`, id, work.FindingID)
	h.exec(`PRAGMA query_only=ON`)
	view, err := h.s.GetFinding(t.Context(), work.FindingID, FindingQuery{})
	if err != nil {
		t.Fatal("详情投递记录读取失败", err)
	}
	if len(view.Deliveries) != 1 {
		t.Fatal("投递记录丢失")
	}
	assertStoredPrecisionMessage(t, view.Deliveries[0].Error)
}

func TestStoredMessageNumbersMarketSnapshotRead(t *testing.T) {
	h, work := storedPrecisionHarness(t)
	raw := `{"groups":[{"config_key":"old","fair":{"price":1234,"sample":3,"days":30,"basis":` + storedPrecisionMessage + `}}],"warnings":[` + storedPrecisionMessage + `],"historical_extension":{"raw":1e1000}}`
	h.exec(`INSERT INTO market_insights(task_id,generated_at,stats_json) VALUES(?,?,?)`, work.TaskID, h.now, raw)
	h.exec(`PRAGMA query_only=ON`)
	view, err := h.s.GetMarket(t.Context(), work.TaskID)
	if err != nil {
		t.Fatal("行情快照读取失败", err)
	}
	if view.Insight == nil || view.Insight.Stats == nil {
		t.Fatal("历史行情快照丢失")
	}
	stats := view.Insight.Stats
	if stats.Groups[0].Fair.Cents == nil || *stats.Groups[0].Fair.Cents != 1234 || stats.Groups[0].Fair.Sample != 3 {
		t.Fatal("行情业务数字改变")
	}
	assertStoredPrecisionMessage(t, &stats.Groups[0].Fair.Basis)
	assertStoredPrecisionMessage(t, &stats.Warnings[0])
}

func TestStoredMessageNumbersOverviewReaders(t *testing.T) {
	for _, source := range []string{"account", "event"} {
		t.Run(source, func(t *testing.T) {
			h, _ := storedPrecisionHarness(t)
			if source == "account" {
				h.exec(`INSERT INTO accounts(id,name,state,state_reason) VALUES(1,'账号','signed_out',?)`, storedPrecisionMessage)
			} else {
				h.exec(`INSERT INTO system_events(kind,subject_id,message,created_at) VALUES('login_failures',0,?,?)`, storedPrecisionMessage, h.now)
			}
			h.exec(`PRAGMA query_only=ON`)
			tx := h.tx()
			if source == "account" {
				accounts, err := overviewAccountsTx(t.Context(), tx)
				if err != nil {
					t.Fatal("概览账号原因读取失败", err)
				}
				assertStoredPrecisionMessage(t, accounts[1].reason)
			} else {
				events, err := overviewEventsTx(t.Context(), tx)
				if err != nil {
					t.Fatal("概览事件读取失败", err)
				}
				if len(events) != 1 {
					t.Fatal("概览事件丢失")
				}
				assertStoredPrecisionMessage(t, events[0].Message)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			view, err := h.s.GetOverview(t.Context(), "today")
			if err != nil {
				t.Fatal("概览页读取失败", err)
			}
			if source == "event" {
				assertStoredPrecisionMessage(t, view.Exceptions.UnresolvedEvents[0].Message)
			}
		})
	}
}

func TestStoredMessageNumbersSystemEventRead(t *testing.T) {
	h, _ := storedPrecisionHarness(t)
	h.exec(`INSERT INTO system_events(kind,subject_id,message,created_at) VALUES('login_failures',0,?,?)`, storedPrecisionMessage, h.now)
	h.exec(`PRAGMA query_only=ON`)
	page, err := h.s.ListSystemEvents(t.Context(), SystemEventQuery{})
	if err != nil {
		t.Fatal("事件列表读取失败", err)
	}
	if len(page.Items) != 1 {
		t.Fatal("事件丢失")
	}
	assertStoredPrecisionMessage(t, page.Items[0].Message)
}

func TestStoredJSONNumberDecoderCompatibility(t *testing.T) {
	raw := strings.TrimSuffix(storedPrecisionMessage, "}") + `,"future_envelope":{"raw":1e1000}}`
	var message locales.Message
	if err := decodeJSONNumber(raw, &message); err != nil {
		t.Fatal("历史字段被错误拒绝", err)
	}
	assertStoredPrecisionMessage(t, &message)
	if err := logJSON(raw, &message); err == nil {
		t.Fatal("日志原有的未知字段拒绝被放宽")
	}
	for _, raw := range []string{storedPrecisionMessage + " {}", `{"code":"broken"`, string([]byte{'"', 0xff, '"'})} {
		for _, decode := range []func(string, any) error{decodeJSONNumber, logJSON} {
			if err := decode(raw, &message); err == nil {
				t.Fatal("非法JSON或多文档被接受")
			}
		}
	}
}
