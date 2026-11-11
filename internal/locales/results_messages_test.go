package locales

import "testing"

func TestReanalysisPreviewOnlyMessage(t *testing.T) {
	for _, tc := range []struct {
		lang Language
		want string
	}{
		{ZhCN, "预览只估算当前需要的详情访问次数，不会预扣额度；实际访问详情时才计入用量。"},
		{En, "Preview only estimates the detail visits currently needed and does not reserve quota. Usage is counted when detail pages are actually visited."},
		{Ja, "プレビューは現時点で必要な詳細アクセス回数を見積もるだけで、利用可能な回数を事前に差し引きません。実際に詳細へアクセスした時点で使用回数に数えます。"},
	} {
		got, err := Format(tc.lang, Message{Code: "results.reanalyze.preview_only", Args: map[string]any{}})
		if err != nil || got != tc.want {
			t.Errorf("重分析预览提示%s：%q，期望%q，%v", tc.lang, got, tc.want, err)
		}
	}
}

func TestTaskReanalysisConfirmationMessagesHaveBothLanguages(t *testing.T) {
	for _, tc := range []struct {
		count, cached, detail int
		zh, en, ja            string
	}{
		{1, 1, 0, "将重新分析这 1 个受影响商品：1 个可使用缓存详情，0 个需要重新打开详情。确认后分批入队，由正常运行处理。", "Reanalyze this 1 affected listing. Cached details: 1; new detail visits: 0. After confirmation, the listing will be queued for a normal run.", "影響を受ける1件の商品を再分析します。1件はキャッシュ済みの詳細を使用でき、0件は詳細ページを開き直す必要があります。確認後に分割して待機リストへ追加し、通常の実行で処理します。"},
		{215, 200, 15, "将重新分析这 215 个受影响商品：200 个可使用缓存详情，15 个需要重新打开详情。确认后分批入队，由正常运行处理。", "Reanalyze these 215 affected listings. Cached details: 200; new detail visits: 15. After confirmation, the listings will be queued in batches for normal runs.", "影響を受ける215件の商品を再分析します。200件はキャッシュ済みの詳細を使用でき、15件は詳細ページを開き直す必要があります。確認後に分割して待機リストへ追加し、通常の実行で処理します。"},
		{215, 214, 1, "将重新分析这 215 个受影响商品：214 个可使用缓存详情，1 个需要重新打开详情。确认后分批入队，由正常运行处理。", "Reanalyze these 215 affected listings. Cached details: 214; new detail visits: 1. After confirmation, the listings will be queued in batches for normal runs.", "影響を受ける215件の商品を再分析します。214件はキャッシュ済みの詳細を使用でき、1件は詳細ページを開き直す必要があります。確認後に分割して待機リストへ追加し、通常の実行で処理します。"},
	} {
		message := Message{Code: "tasks.effects.reanalysis_confirm", Args: map[string]any{"count": tc.count, "cached": tc.cached, "detail": tc.detail}}
		for _, lang := range []Language{ZhCN, En, Ja} {
			want := tc.zh
			if lang == En {
				want = tc.en
			} else if lang == Ja {
				want = tc.ja
			}
			got, err := Format(lang, message)
			if err != nil || got != want {
				t.Errorf("任务重分析确认%s，count=%d、detail=%d：%q，期望%q，%v", lang, tc.count, tc.detail, got, want, err)
			}
		}
	}
}

func TestAllFailedReanalysisMessagesHaveBothLanguages(t *testing.T) {
	for _, tc := range []struct {
		message Message
		zh      string
		en      string
		ja      string
	}{
		{Message{Code: "results.reanalyze.confirm_all", Args: map[string]any{"count": 1, "cached": 1, "detail": 0}}, "将重新分析当前筛选内的 1 条失败结果。其中 1 条可使用缓存详情，0 条需要重新打开详情。入队后由正常运行处理。", "Reanalyze 1 failed result in this filter. Cached details are available for 1, and new detail visits are needed for 0. Normal runs will process the result after it is queued.", "現在のフィルターにある分析失敗の結果 1 件を再分析します。このうち 1 件はキャッシュした詳細を使用でき、0 件は詳細を開き直す必要があります。順番待ちに追加された後、通常のタスク実行時に処理します。"},
		{Message{Code: "results.reanalyze.confirm_all", Args: map[string]any{"count": 200, "cached": 185, "detail": 15}}, "将重新分析当前筛选内的 200 条失败结果。其中 185 条可使用缓存详情，15 条需要重新打开详情。入队后由正常运行处理。", "Reanalyze 200 failed results in this filter. Cached details are available for 185, and new detail visits are needed for 15. Normal runs will process the results after they are queued.", "現在のフィルターにある分析失敗の結果 200 件を再分析します。このうち 185 件はキャッシュした詳細を使用でき、15 件は詳細を開き直す必要があります。順番待ちに追加された後、通常のタスク実行時に処理します。"},
		{Message{Code: "results.reanalyze.confirm_limited", Args: map[string]any{"total": 215, "count": 200, "cached": 185, "detail": 15}}, "当前筛选共 215 条失败结果，这次处理前 200 条，剩下的再点一次。其中 185 条可使用缓存详情，15 条需要重新打开详情。入队后由正常运行处理。", "There are 215 failed results in this filter. Reanalyze the first 200 now and click again for the rest. In this batch, cached details are available for 185, and new detail visits are needed for 15. Normal runs will process the results after they are queued.", "現在のフィルターには分析失敗の結果が全 215 件あります。今回は先頭の 200 件を処理します。残りはもう一度クリックしてください。このうち 185 件はキャッシュした詳細を使用でき、15 件は詳細を開き直す必要があります。順番待ちに追加された後、通常のタスク実行時に処理します。"},
		{Message{Code: "results.reanalyze.progress", Args: map[string]any{"confirmed": 200, "total": 215, "queued": 198}}, "已确认 200/215 条结果，其中 198 条已排队", "Confirmed results: 200/215. Queued: 198.", "200/215 件の結果を確認しました。このうち 198 件が順番待ちです"},
		{Message{Code: "results.reanalyze.stopped", Args: map[string]any{"confirmed": 200, "queued": 198, "uncertain": 15, "remaining": 0}}, "操作已停止：已确认 200 条结果，其中 198 条已排队；15 条请求结果未确认，0 条尚未提交。未自动重发。", "Stopped. Confirmed results: 200; queued: 198. Request outcomes unknown: 15; not submitted: 0. No automatic retries.", "処理を停止しました。結果を確認できた 200 件のうち 198 件が順番待ちです。15 件はリクエストの結果が未確認、0 件は未送信です。自動再送はしていません。"},
	} {
		for _, lang := range []Language{ZhCN, En, Ja} {
			t.Run(tc.message.Code+"/"+string(lang), func(t *testing.T) {
				want := tc.zh
				if lang == En {
					want = tc.en
				} else if lang == Ja {
					want = tc.ja
				}
				got, err := Format(lang, tc.message)
				if err != nil || got != want {
					t.Fatalf("批量重分析消息：%q，期望%q，%v", got, want, err)
				}
			})
		}
	}
}
