package service

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
)

func findingMetadataSetup(t *testing.T) (findingsHarness, WorkRef) {
	t.Helper()
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeAI, false)
	work := h.finding(ref, "x", StageDetail, DecisionPending, h.now)
	h.exec(`UPDATE listings SET seller_id='seller',seller_registration_days=200 WHERE item_id='x'`)
	h.exec(`INSERT INTO seller_profiles(seller_id,recent_items_json,recent_rates_json,fetched_at)
		VALUES('seller',?, '[]', ?)`, `[{"title":"camera A","sale_status":"on_sale"},
		{"title":"camera B","sale_status":"on_sale"},{"title":"camera C","sale_status":"on_sale"}]`, h.now)
	return h, work
}

func saveMetadataConclusion(t *testing.T, h findingsHarness, work WorkRef, summary string, warnings []locales.Message) Finding {
	t.Helper()
	tx := h.tx()
	facts := analysis.Facts{Summary: summary}
	first, err := h.s.saveConclusionTx(t.Context(), tx, work, Conclusion{Facts: &facts, Warnings: warnings}, h.now)
	if err != nil || !first {
		t.Fatalf("真实分析结论保存失败：%v %v", first, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return h.read(work.FindingID)
}

func TestSellerAssessmentStoredOnSuccessAndReplacedOnSuccessfulReanalysis(t *testing.T) {
	h, work := findingMetadataSetup(t)
	first := saveMetadataConclusion(t, h, work, "第一次", nil)
	if first.SellerAssessment == nil || first.SellerAssessment.Type != analysis.SellerDealerSuspect {
		t.Fatalf("分析时画像没有保存：%+v", first.SellerAssessment)
	}
	h.exec(`UPDATE seller_profiles SET recent_items_json='[]' WHERE seller_id='seller'`)
	h.exec(`UPDATE findings SET decision='pending',stage='detail',processing_revision=2 WHERE id=?`, work.FindingID)
	work.ProcessingRevision++
	second := saveMetadataConclusion(t, h, work, "成功重分析", nil)
	if second.SellerAssessment == nil || second.SellerAssessment.Type != analysis.SellerPersonal {
		t.Fatalf("成功重分析没有替换画像：%+v", second.SellerAssessment)
	}
	if first.SellerAssessment.Type != analysis.SellerDealerSuspect {
		t.Fatal("新结论修改旧的读取快照")
	}
	var saved string
	if err := h.s.db.QueryRowContext(t.Context(), `SELECT seller_checks_json FROM findings WHERE id=?`, work.FindingID).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeSellerAssessment(saved)
	if err != nil || !reflect.DeepEqual(decoded, second.SellerAssessment) {
		t.Fatalf("卖家画像没有和结论一起持久化：%s %v", saved, err)
	}
}

func TestSellerAssessmentAndWarningsSurviveLayerSkipAndFailure(t *testing.T) {
	for _, action := range []string{"layer", "skipped", "failed"} {
		t.Run(action, func(t *testing.T) {
			h, work := findingMetadataSetup(t)
			warnings := []locales.Message{findingMessage("analysis.item_ignored", map[string]any{"item": "c1"})}
			before := saveMetadataConclusion(t, h, work, "旧结论", warnings)
			h.exec(`UPDATE seller_profiles SET recent_items_json='[]' WHERE seller_id='seller'`)
			h.exec(`UPDATE findings SET decision='pending',stage='detail',processing_revision=2 WHERE id=?`, work.FindingID)
			work.ProcessingRevision++
			if action == "layer" {
				h.exec(`UPDATE tasks SET exclude_keywords_json='["broken"]' WHERE id=?`, work.TaskID)
				h.exec(`UPDATE listings SET description='broken sensor' WHERE item_id='x'`)
			} else if action == "skipped" {
				h.exec(`UPDATE listings SET is_dismissed=1 WHERE item_id='x'`)
			}
			tx := h.tx()
			var err error
			switch action {
			case "layer":
				_, err = h.s.saveConclusionTx(t.Context(), tx, work, Conclusion{Layer: &analysis.LayerResult{Rejected: true, RejectKind: analysis.RejectExcluded}}, h.now)
			case "skipped":
				reason := findingMessage("findings.dismissed", nil)
				_, err = h.s.saveConclusionTx(t.Context(), tx, work, Conclusion{Skipped: &reason}, h.now)
			case "failed":
				err = saveFindingFailureTx(t.Context(), tx, work, StageDetail, findingMessage("analysis.client.network", nil), true, false, h.now)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			after := h.read(work.FindingID)
			if !reflect.DeepEqual(before.SellerAssessment, after.SellerAssessment) || !reflect.DeepEqual(before.AnalysisWarnings, after.AnalysisWarnings) {
				t.Fatalf("非成功分析替换历史依据：%+v => %+v", before, after)
			}
		})
	}
}

func TestSellerAssessmentAndWarningsShareConclusionRollback(t *testing.T) {
	h, work := findingMetadataSetup(t)
	before := saveMetadataConclusion(t, h, work, "旧结论", []locales.Message{findingMessage("analysis.item_ignored", map[string]any{"item": "c1"})})
	tx := h.tx()
	if _, err := tx.ExecContext(t.Context(), `UPDATE seller_profiles SET recent_items_json='[]' WHERE seller_id='seller'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), `UPDATE findings SET decision='pending',stage='detail',processing_revision=2 WHERE id=?`, work.FindingID); err != nil {
		t.Fatal(err)
	}
	work.ProcessingRevision++
	if _, err := h.s.saveConclusionTx(t.Context(), tx, work, Conclusion{Facts: &analysis.Facts{Summary: "未提交"}}, h.now+1); err != nil {
		t.Fatal(err)
	}
	during, err := loadFindingTx(t.Context(), tx, work.FindingID)
	if err != nil || during.SellerAssessment == nil || during.SellerAssessment.Type != analysis.SellerPersonal || len(during.AnalysisWarnings) != 0 {
		t.Fatalf("新事务未写入新依据：%+v %v", during, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after := h.read(work.FindingID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("画像/警告没有和结论一起回滚：%+v => %+v", before, after)
	}
}

func TestFindingScannerUsesStrictSellerAssessmentCodec(t *testing.T) {
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeAI, false)
	work := h.finding(ref, "x", StageDetail, DecisionPending, h.now)
	if finding := h.read(work.FindingID); finding.SellerAssessment != nil {
		t.Fatal("SQL NULL被猜成卖家画像")
	}
	for _, raw := range []string{"null", "{", "{}"} {
		h.exec(`UPDATE findings SET seller_checks_json=? WHERE id=?`, raw, work.FindingID)
		tx := h.tx()
		if _, err := loadFindingTx(t.Context(), tx, work.FindingID); err == nil {
			t.Fatalf("坏画像被当成没有：%s", raw)
		}
		tx.Rollback()
	}
}

func TestParseWarningsPersistSeparatelyFromModelFacts(t *testing.T) {
	h, work := findingMetadataSetup(t)
	profile := analysis.Profile{Risks: []analysis.ProfileRisk{{ID: "c1", Name: "划痕", Attitude: analysis.Mind, Rev: 1}}}
	encodedProfile, _ := json.Marshal(profile)
	h.exec(`UPDATE tasks SET profile_json=? WHERE id=?`, string(encodedProfile), work.TaskID)
	facts, warnings, err := analysis.ParseFacts([]byte(`{"specs":[{"name":"extra","value":"x"}],"checks":[
		{"key":"c1","status":"risk","source":"text","evidence":"not in original"},
		{"key":"c1","status":"ok","source":null,"evidence":null}],"adjust":{"value":0},"summary":"checked"}`),
		profile, analysis.EvidenceOriginals{Title: "camera"})
	if err != nil || len(warnings) != 3 {
		t.Fatalf("假回复没有产生预期程序警告：%+v %v", warnings, err)
	}
	tx := h.tx()
	if _, err := h.s.saveConclusionTx(t.Context(), tx, work, Conclusion{RequestProfile: profile, Facts: &facts, Warnings: warnings}, h.now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	finding := h.read(work.FindingID)
	if !reflect.DeepEqual(finding.AnalysisWarnings, warnings) || finding.Analysis == nil {
		t.Fatalf("解析警告丢失：%+v", finding)
	}
	var raw string
	if err := h.s.db.QueryRowContext(t.Context(), `SELECT analysis_json FROM findings WHERE id=?`, work.FindingID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stored map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &stored); err != nil || len(stored["parse_warnings"]) == 0 {
		t.Fatalf("未保存内部警告：%s %v", raw, err)
	}
	publicFacts, _ := json.Marshal(finding.Analysis)
	var plain map[string]json.RawMessage
	json.Unmarshal(publicFacts, &plain)
	if _, present := plain["parse_warnings"]; present {
		t.Fatal("内部元数据进入模型Facts结构")
	}
	for _, language := range []locales.Language{locales.ZhCN, locales.En} {
		for _, warning := range finding.AnalysisWarnings {
			if _, err := locales.Format(language, warning); err != nil {
				t.Fatal(err)
			}
		}
	}
	h.exec(`UPDATE findings SET decision='pending',stage='detail',processing_revision=2 WHERE id=?`, work.FindingID)
	work.ProcessingRevision++
	after := saveMetadataConclusion(t, h, work, "重新分析无警告", nil)
	if after.AnalysisWarnings == nil || len(after.AnalysisWarnings) != 0 {
		t.Fatalf("成功重分析没有清旧警告：%+v", after.AnalysisWarnings)
	}
}

func TestAnalysisWarningCodecReadsLegacyAndRejectsBadMetadata(t *testing.T) {
	facts, warnings, err := decodeFindingFacts(`{"summary":"legacy"}`)
	if err != nil || facts == nil || facts.Summary != "legacy" || warnings == nil || len(warnings) != 0 {
		t.Fatalf("旧事实没得到空警告数组：%+v %+v %v", facts, warnings, err)
	}
	for _, raw := range []string{
		`{"parse_warnings":null}`, `{"parse_warnings":{}}`, `{"parse_warnings":[null]}`,
		`{"parse_warnings":[{}]}`, `{"parse_warnings":[{"code":"unknown","args":{}}]}`,
		`{"parse_warnings":[{"code":"analysis.item_ignored","args":null}]}`,
		`{"parse_warnings":[{"code":"analysis.item_ignored","args":{"item":1}}]}`,
		`{"parse_warnings":[{"code":"analysis.item_ignored","args":{"item":"c1"},"extra":1}]}`,
		`{"parse_warnings":[{"code":"analysis.item_ignored","args":{"item":"c1","extra":"x"}}]}`,
	} {
		if _, _, err := decodeFindingFacts(raw); err == nil {
			t.Fatalf("坏警告被吞：%s", raw)
		}
	}
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeAI, false)
	work := h.finding(ref, "x", StageDetail, DecisionPending, h.now)
	h.exec(`UPDATE findings SET analysis_json='{"parse_warnings":null}' WHERE id=?`, work.FindingID)
	tx := h.tx()
	if _, err := loadFindingTx(t.Context(), tx, work.FindingID); err == nil {
		t.Fatal("scanner吞掉坏警告")
	}
	tx.Rollback()
}

func TestInvalidAnalysisWarningCannotPartiallySaveConclusion(t *testing.T) {
	h, work := findingMetadataSetup(t)
	before := saveMetadataConclusion(t, h, work, "旧结论", nil)
	h.exec(`UPDATE findings SET decision='pending',stage='detail',processing_revision=2 WHERE id=?`, work.FindingID)
	work.ProcessingRevision++
	tx := h.tx()
	_, err := h.s.saveConclusionTx(t.Context(), tx, work, Conclusion{Facts: &analysis.Facts{Summary: "不能保存"},
		Warnings: []locales.Message{findingMessage("unknown", nil)}}, h.now+1)
	if err == nil {
		t.Fatal("无效警告仍保存")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after := h.read(work.FindingID)
	if !reflect.DeepEqual(before.Analysis, after.Analysis) || !reflect.DeepEqual(before.SellerAssessment, after.SellerAssessment) || !reflect.DeepEqual(before.AnalysisWarnings, after.AnalysisWarnings) || after.Summary == nil || *after.Summary != "旧结论" {
		t.Fatalf("错误仍写入部分结论：%+v", after)
	}
}
