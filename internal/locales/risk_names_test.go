package locales

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestFormatLocalizesOnlyIdentifiedRiskNames(t *testing.T) {
	for _, tc := range []struct{ code, zh, en, ja string }{
		{"analysis.risk_confirmed", "发现ID 锁，按你的要求不建议", "Confirmed ID lock; your settings exclude it", "IDロック が確認されたため、設定に従いおすすめしません"},
		{"analysis.risk_penalty", "发现ID 锁，按你的设置扣分", "Confirmed ID lock; a deduction applies under your settings", "IDロック が確認されたため、設定に従い減点します"},
		{"analysis.risk_uncertain", "ID 锁还需确认", "ID lock needs confirmation", "IDロック の確認が必要です"},
		{"analysis.risk_unknown", "ID 锁没有写明，先问清楚", "ID lock was not disclosed; ask the seller first", "IDロック の記載がありません。まず出品者に確認してください"},
	} {
		message := Message{Code: tc.code, Args: map[string]any{"id": "id_lock", "name": "模型原文 {attitude}", "attitude": "confirm"}}
		before, _ := json.Marshal(message)
		for _, lang := range []Language{En, ZhCN, Ja} {
			want := tc.en
			if lang == ZhCN {
				want = tc.zh
			} else if lang == Ja {
				want = tc.ja
			}
			if got, err := Format(lang, message); err != nil || got != want {
				t.Errorf("风险名称%s/%s：%q，期望%q，%v", tc.code, lang, got, want, err)
			}
		}
		after, _ := json.Marshal(message)
		if !bytes.Equal(before, after) {
			t.Fatalf("本地化修改了持久原因参数：%s → %s", before, after)
		}
	}
	for _, tc := range []struct{ id, name string }{
		{"carrier_lock", "Carrier lock"}, {"console_banned", "Banned console"}, {"cosmetic_damage", "Cosmetic damage"},
	} {
		got, err := Format(En, Message{Code: "analysis.risk_uncertain", Args: map[string]any{"id": tc.id, "name": "中文原名", "attitude": "confirm"}})
		if err != nil || got != tc.name+" needs confirmation" {
			t.Errorf("c开头内置编号被当成自定义：%s，%q，%v", tc.id, got, err)
		}
	}
}

func TestFormatPreservesCustomNamesAndOtherReasons(t *testing.T) {
	for _, id := range []any{"c1", "c29", "unknown", "", nil, int64(1)} {
		for _, code := range []string{"analysis.risk_confirmed", "analysis.risk_penalty", "analysis.risk_uncertain", "analysis.risk_unknown"} {
			for _, name := range []string{"ID 锁", "自定义 {name}"} {
				got, err := Format(En, Message{Code: code, Args: map[string]any{"id": id, "name": name, "attitude": "confirm"}})
				if err != nil || !strings.Contains(got, name) {
					t.Errorf("自定义或未知编号被按名字翻译：%s/%v，%q，%v", code, id, got, err)
				}
			}
		}
	}
	for _, code := range []string{
		"analysis.bonus_met", "analysis.must_unmet", "analysis.spec_mismatch", "analysis.spec_unmatched",
		"analysis.fair_relaxed", "analysis.risk_softened", "analysis.question_about",
	} {
		message := Message{Code: code, Args: map[string]any{"id": "id_lock", "name": "ID 锁 {value}", "value": "原值", "count": 7, "days": 7}}
		got, err := Format(En, message)
		if err != nil || !strings.Contains(got, "ID 锁 {value}") {
			t.Errorf("其它原因被泛化翻译或递归插值：%s，%q，%v", code, got, err)
		}
	}
}

func TestFormatRiskNamesCanShareMessageAcrossLanguages(t *testing.T) {
	message := Message{Code: "analysis.risk_unknown", Args: map[string]any{"id": "id_lock", "name": "ID 锁", "attitude": "confirm"}}
	before, _ := json.Marshal(message)
	var wait sync.WaitGroup
	for i := range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			lang, want := En, "ID lock was not disclosed; ask the seller first"
			if i%3 == 0 {
				lang, want = ZhCN, "ID 锁没有写明，先问清楚"
			} else if i%3 == 1 {
				lang, want = Ja, "IDロック の記載がありません。まず出品者に確認してください"
			}
			if got, err := Format(lang, message); err != nil || got != want {
				t.Errorf("共享原因在不同语言间污染：%s，%q，%v", lang, got, err)
			}
		}()
	}
	wait.Wait()
	after, _ := json.Marshal(message)
	if !bytes.Equal(before, after) {
		t.Fatalf("并发本地化修改原参数：%s → %s", before, after)
	}
}
