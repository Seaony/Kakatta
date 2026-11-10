package notification

import (
	"net/url"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/service"
)

func TestMobileItemLinkKeepsDecimalID(t *testing.T) {
	for _, id := range []string{"1", "9223372036854775808", "1234567890123456789012345678901234567890"} {
		link, err := url.Parse(mobileItemLink(id))
		if err != nil || link.Host != "pages.goofish.com" || link.Query().Get("bfp") != `{"id":`+id+`}` {
			t.Fatalf("编号没有按原文进入数字 JSON：%s %v", id, err)
		}
	}
	for _, id := range []string{"", "0", "01", "-1", "1.2", "1e4", "１２", "1\n", `1,"x":2`} {
		if mobileItemLink(id) != "" {
			t.Fatalf("非法编号拼进了手机链接：%q", id)
		}
	}
}

func TestSelectedItemLinkFallsBackBothWays(t *testing.T) {
	for _, test := range []struct{ target, desktop, mobile, want string }{
		{"desktop", "D", "M", "D"}, {"mobile", "D", "M", "M"},
		{"desktop", "", "M", "M"}, {"mobile", "D", "", "D"},
		{"mobile", "", "", ""},
	} {
		if got := selectedItemLink(test.target, test.desktop, test.mobile); got != test.want {
			t.Fatalf("链接回退错误：%+v，得到%s", test, got)
		}
	}
}

func TestNotificationSingleLine(t *testing.T) {
	if got := notificationSingleLine(" 标题\r\n还有\t内容 "); got != "标题  还有 内容" || strings.ContainsAny(got, "\r\n\t") {
		t.Fatalf("没有保持单行：%q", got)
	}
}

func TestNotificationPublicLinksKeepPathPrefixAndIDs(t *testing.T) {
	for _, publicURL := range []string{"https://kakatta.invalid", "https://kakatta.invalid/base/", "https://kakatta.invalid/base%20path/%E4%B8%AD/"} {
		base, _ := url.Parse(publicURL)
		finding, err := notificationFindingURL(publicURL, 7, 9223372036854775807)
		if err != nil {
			t.Fatal(err)
		}
		address, _ := url.Parse(finding)
		if address.Path != strings.TrimRight(base.Path, "/")+"/results" || address.Query().Get("task_id") != "7" || address.Query().Get("finding_id") != "9223372036854775807" {
			t.Fatalf("发现链接丢失路径或编号：%s", finding)
		}
		task, err := notificationTaskResultsURL(publicURL, 7)
		if err != nil {
			t.Fatal(err)
		}
		address, _ = url.Parse(task)
		if address.Path != strings.TrimRight(base.Path, "/")+"/results" || address.Query().Get("task_id") != "7" || address.Query().Has("finding_id") {
			t.Fatalf("任务结果链接错误：%s", task)
		}
	}
	if got, err := notificationFindingURL("", 7, 1); err != nil || got != "" {
		t.Fatalf("没有公开地址仍生成站内链接：%q %v", got, err)
	}
}

func TestSystemEventLinksUseFrozenPages(t *testing.T) {
	for _, test := range []struct {
		kind      service.SystemEventKind
		path, key string
	}{
		{service.EventAccountVerify, "/accounts", "account_id"},
		{service.EventAccountDeny, "/accounts", "account_id"},
		{service.EventAccountLogin, "/accounts", "account_id"},
		{service.EventAIError, "/settings/ai", "provider_id"},
		{service.EventAIBudget, "/settings/ai", ""},
		{service.EventTaskFailurePause, "/logs", "task_id"},
		{service.EventXianyuLayout, "/settings/about", ""},
		{service.EventLoginFailures, "/settings/security", ""},
	} {
		event := service.SystemEvent{Kind: &test.kind, SubjectID: formatPtr(int64(17))}
		link, err := systemEventLink("https://kakatta.invalid/base/", event)
		if err != nil {
			t.Fatal(err)
		}
		address, _ := url.Parse(link)
		if address.Path != "/base"+test.path || test.key != "" && address.Query().Get(test.key) != "17" || test.key == "" && address.RawQuery != "" {
			t.Fatalf("系统事件路由错误：%s %s", test.kind, link)
		}
		if link, err := systemEventLink("", event); err != nil || link != "" {
			t.Fatalf("无公开地址仍带系统链接：%q %v", link, err)
		}
	}
}

func TestNotificationPublicLinksRejectInvalidInput(t *testing.T) {
	for _, publicURL := range []string{"/base", "ftp://kakatta.invalid/base", "https://user:secret@kakatta.invalid", "https://kakatta.invalid/?token=secret", "https://kakatta.invalid/#anchor", "https://", "https://kakatta.invalid/%xx"} {
		if _, err := notificationTaskResultsURL(publicURL, 7); err == nil {
			t.Fatalf("非法公开地址被接受：%q", publicURL)
		}
	}
	if _, err := notificationFindingURL("", 0, 1); err == nil {
		t.Fatal("零任务编号被接受")
	}
	if _, err := notificationFindingURL("", 1, 0); err == nil {
		t.Fatal("零发现编号被接受")
	}
	if _, err := notificationTaskResultsURL("", -1); err == nil {
		t.Fatal("负任务编号被接受")
	}
	if _, err := systemEventLink("", service.SystemEvent{}); err == nil {
		t.Fatal("缺事件类型被接受")
	}
	kind := service.EventAccountVerify
	if _, err := systemEventLink("", service.SystemEvent{Kind: &kind}); err == nil {
		t.Fatal("缺账号编号被接受")
	}
}
