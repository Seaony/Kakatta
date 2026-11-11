package notification

import (
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
)

func TestFormatSystemEnvironmentDenyIncludesAdvice(t *testing.T) {
	event := service.SystemEvent{
		Kind: formatPtr(service.EventAccountDeny),
		Message: &locales.Message{Code: "accounts.environment_denied", Args: map[string]any{
			"name": "账号", "hours": int64(24), "environment": true,
			"until": time.Date(2026, 11, 11, 6, 32, 0, 0, time.UTC).UnixMilli(),
		}},
	}
	for _, test := range []struct {
		language locales.Language
		body     string
	}{
		{locales.ZhCN, "这个账号暂停 24 小时，到 14:32 自动恢复。 闲鱼可能认出了浏览器环境，请检查 WebGL、Chrome 沙盒和代理设置。"},
		{locales.En, "This account is paused for 24 hours and will resume automatically at 14:32. Xianyu may have recognized the browser environment. Check WebGL, the Chrome sandbox, and proxy settings."},
	} {
		t.Run(string(test.language), func(t *testing.T) {
			message, err := formatSystem(event, formatOpts(test.language), "", nil)
			if err != nil || message.Body != test.body {
				t.Fatalf("环境拒绝通知遗漏或改错了建议：%q，期望%q，%v", message.Body, test.body, err)
			}
		})
	}
}

func TestFormatSystemExpiredLoginIncludesActualTransfers(t *testing.T) {
	event := service.SystemEvent{
		Kind: formatPtr(service.EventAccountLogin),
		Message: &locales.Message{Code: "accounts.login_expired", Args: map[string]any{
			"name": "失效账号", "count": int64(3), "reassigned_to": "备用甲、备用乙",
		}},
	}
	for _, test := range []struct {
		language locales.Language
		body     string
	}{
		{locales.ZhCN, "请打开 Kakatta 重新扫码登录。 3 个任务已换到 备用甲、备用乙。"},
		{locales.En, "Open Kakatta and scan the QR code to sign in again. 3 tasks have moved to 备用甲、备用乙."},
	} {
		t.Run(string(test.language), func(t *testing.T) {
			message, err := formatSystem(event, formatOpts(test.language), "", nil)
			if err != nil || message.Body != test.body {
				t.Fatalf("登录失效通知遗漏或改错了转移数量和账号：%q，期望%q，%v", message.Body, test.body, err)
			}
		})
	}
}
