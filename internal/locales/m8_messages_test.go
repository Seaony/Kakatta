package locales

import "testing"

func TestM8ActualMessagesHaveBothLanguages(t *testing.T) {
	for _, tc := range []struct {
		code, zh, en, ja string
	}{
		{"tasks.transfer_unavailable", "任务导入导出服务暂不可用", "Task transfer is unavailable.", "タスクの読み込み・書き出しサービスを現在利用できません。"},
		{"tasks.import_invalid_file", "文件不是有效的任务配置文件", "The file is not a valid task configuration file.", "有効なタスク設定ファイルではありません。"},
		{"tasks.import_unsupported_version", "不支持这个任务配置文件版本", "This task file version is not supported.", "このバージョンのタスク設定ファイルには対応していません。"},
		{"tasks.import_cancelled", "导入已停止，已保存的任务仍然保留", "Import stopped. Saved tasks are preserved.", "読み込みを停止しました。保存済みのタスクは保持しています。"},
		{"tasks.import_unknown_field", "已去掉不认识的字段", "Removed an unknown field.", "不明なフィールドを除去しました。"},
		{"tasks.import_name_defaulted", "名称为空，已使用搜索关键词", "Used the search keyword because the name was empty.", "名前が空のため、検索キーワードを使用しました。"},
		{"tasks.import_name_truncated", "名称超过 30 字，已截断", "Truncated the name to 30 characters.", "名前が30文字を超えるため、切り詰めました。"},
		{"tasks.import_pages_defaulted", "翻页数小于 1，已改为 1", "Changed the page count to 1 because it was below 1.", "ページ数が1未満のため、1に変更しました。"},
		{"tasks.import_budget_removed", "预算下限高于上限，已清除预算", "Removed the budget because its minimum exceeded its maximum.", "予算の下限が上限より高いため、予算を解除しました。"},
		{"tasks.import_ai_to_keyword", "清单中既没有规格，也没有必须满足项，已改为关键词判断", "Switched to keyword matching because the checklist had no specifications or requirements.", "条件リストに仕様も必須条件もないため、キーワード判定に切り替えました。"},
		{"tasks.import_policy_defaulted", "推送档位无效，已使用当前判断方式的默认档位", "Used the default notification policy for the current judgment method.", "通知の基準が無効なため、現在の判定方法の既定値を使用しました。"},
		{"tasks.import_channel_unmatched", "本机没有类型和名称都匹配的渠道，已去掉", "Removed a channel with no matching type and name on this installation.", "このインスタンスに種類と名前が両方一致する通知先がないため、除去しました。"},
		{"tasks.import_duplicate_channel", "已去掉重复渠道", "Removed a duplicate channel.", "重複する通知先を除去しました。"},
		{"tasks.import_profile_source", "来源缺失或不认识，已按补充项处理", "Treated a missing or unknown source as a suggested item.", "出典がないか不明なため、補足項目として扱いました。"},
		{"tasks.import_profile_empty_spec_removed", "规格没有名称，已去掉", "Removed a specification without a name.", "名前のない仕様を除去しました。"},
		{"tasks.import_profile_unknown_risk_removed", "检查项既不是自定义项也不在字典中，已去掉", "Removed a check that was neither custom nor in the dictionary.", "独自の確認項目でも辞書内の項目でもないため、除去しました。"},
		{"tasks.import_profile_duplicate_risk_removed", "字典检查项重复，已保留第一条", "Kept the first occurrence of a repeated dictionary check.", "辞書の確認項目が重複しているため、最初の項目を保持しました。"},
		{"tasks.import_profile_empty_risk_removed", "自定义检查项没有名称，已去掉", "Removed a custom check without a name.", "名前のない独自の確認項目を除去しました。"},
		{"tasks.import_profile_kind_removed", "已去掉不认识的商品类型", "Removed an unknown item kind.", "不明な商品の種類を除去しました。"},
		{"tasks.import_profile_id", "编号缺失、重复或格式不合法，已重新分配", "Assigned a new identifier because it was missing, duplicated, or malformed.", "IDがないか、重複しているか、形式が無効なため、割り当て直しました。"},
		{"tasks.import_profile_revision", "条目版本缺失或为 0，已补为 1", "Set the item revision to 1 because it was missing or zero.", "項目のバージョンがないか0のため、1に設定しました。"},
		{"tasks.import_profile_must_moved", "补充的必须满足已移为加分项", "Moved a suggested requirement to bonuses.", "提案された必須条件を加点項目に移しました。"},
		{"tasks.import_profile_attitude", "检查态度无效或来源不可信，已改为推荐态度", "Changed an invalid or untrusted check attitude to its recommended setting.", "確認項目の扱いが無効か出典を信頼できないため、推奨の扱いに変更しました。"},
		{"tasks.import_profile_next_no", "已更新清单的后续编号起点", "Updated the starting identifier for new checklist items.", "条件リストの新しい項目に使うIDの開始値を更新しました。"},
		{"tasks.import_override_removed", "等待或频率覆盖值明显无效，已去掉", "Removed an invalid timing or frequency override.", "待機時間または実行頻度の上書き値が無効なため、除去しました。"},
		{"settings.number_required", "请输入数字", "Enter a number.", "数値を入力してください。"},
		{"settings.whole_cents_required", "金额必须能精确换算成整数分", "The amount must convert exactly to whole cents.", "金額は通貨の補助単位「分」の整数に正確に換算できる値を指定してください。"},
		{"update.check_failed", "检查更新失败，稍后会自动重试。", "Could not check for updates. The server will try again later.", "アップデートを確認できませんでした。後ほど自動で再試行します。"},
	} {
		for _, lang := range []Language{ZhCN, En, Ja} {
			want := tc.zh
			if lang == En {
				want = tc.en
			} else if lang == Ja {
				want = tc.ja
			}
			got, err := Format(lang, Message{Code: tc.code, Args: map[string]any{}})
			if err != nil || got != want {
				t.Fatalf("消息%s（%s）：%q，期望%q，%v", tc.code, lang, got, want, err)
			}
		}
	}
}
