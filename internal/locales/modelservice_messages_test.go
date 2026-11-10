package locales

import "testing"

func TestModelserviceMessagesHaveBothLanguages(t *testing.T) {
	for _, tc := range []struct {
		message Message
		zh      string
		en      string
	}{
		{Message{Code: "ai.provider_invalid", Args: map[string]any{"field": "api_key"}}, "AI 服务商设置无效，请检查填写的内容", "The AI provider settings are invalid. Check the supplied values"},
		{Message{Code: "ai.provider_not_found", Args: map[string]any{}}, "这个 AI 服务商已不存在，请重新选择", "This AI provider no longer exists. Choose another provider"},
		{Message{Code: "ai.url_normalized", Args: map[string]any{}}, "已移除接口地址末尾的 /chat/completions", "Removed the trailing /chat/completions from the endpoint"},
		{Message{Code: "ai.provider_error", Args: map[string]any{"provider": "家用模型 {reason}", "reason": Message{Code: "analysis.client.auth", Args: map[string]any{"status": 401, "detail": ""}}}}, "AI 接口出错：家用模型 {reason}", "AI provider error: 家用模型 {reason}"},
	} {
		for _, lang := range []Language{ZhCN, En} {
			t.Run(tc.message.Code+"/"+string(lang), func(t *testing.T) {
				want := tc.zh
				if lang == En {
					want = tc.en
				}
				got, err := Format(lang, tc.message)
				if err != nil || got != want {
					t.Fatalf("服务商消息：%q，期望%q，%v", got, want, err)
				}
			})
		}
	}
}
