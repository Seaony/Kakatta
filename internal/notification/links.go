package notification

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/Seaony/Kakatta/internal/service"
)

func mobileItemLink(itemID string) string {
	if itemID == "" || itemID[0] == '0' {
		return ""
	}
	for _, digit := range itemID {
		if digit < '0' || digit > '9' {
			return ""
		}
	}
	// 商品编号保持十进制原文，不能转成浮点数再编码。
	return "https://pages.goofish.com/sharexy?loadingVisible=false&bft=item&bfs=idlepc.item&spm=a21ybx.item.0.0&bfp=" + url.QueryEscape(`{"id":`+itemID+`}`)
}

func selectedItemLink(target, desktop, mobile string) string {
	if target == "desktop" {
		if desktop != "" {
			return desktop
		}
		return mobile
	}
	if mobile != "" {
		return mobile
	}
	return desktop
}

func notificationSingleLine(value string) string {
	return strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(value))
}

func notificationFindingURL(publicURL string, taskID, findingID int64) (string, error) {
	if taskID < 1 || findingID < 1 {
		return "", errFormatMessage
	}
	return notificationPageURL(publicURL, "/results", url.Values{"task_id": {strconv.FormatInt(taskID, 10)}, "finding_id": {strconv.FormatInt(findingID, 10)}})
}

func notificationTaskResultsURL(publicURL string, taskID int64) (string, error) {
	if taskID < 1 {
		return "", errFormatMessage
	}
	return notificationPageURL(publicURL, "/results", url.Values{"task_id": {strconv.FormatInt(taskID, 10)}})
}

func systemEventLink(publicURL string, event service.SystemEvent) (string, error) {
	if event.Kind == nil {
		return "", errFormatMessage
	}
	path, idKey := "", ""
	switch *event.Kind {
	case service.EventAccountVerify, service.EventAccountDeny, service.EventAccountLogin:
		path, idKey = "/accounts", "account_id"
	case service.EventAIError:
		path, idKey = "/settings/ai", "provider_id"
	case service.EventAIBudget:
		path = "/settings/ai"
	case service.EventTaskFailurePause:
		path, idKey = "/logs", "task_id"
	case service.EventXianyuLayout:
		path = "/settings/about"
	case service.EventLoginFailures:
		path = "/settings/security"
	default:
		return "", errFormatMessage
	}
	query := url.Values{}
	if idKey != "" {
		if event.SubjectID == nil || *event.SubjectID < 1 {
			return "", errFormatMessage
		}
		query.Set(idKey, strconv.FormatInt(*event.SubjectID, 10))
	}
	return notificationPageURL(publicURL, path, query)
}

func notificationPageURL(publicURL, path string, query url.Values) (string, error) {
	if publicURL == "" {
		return "", nil
	}
	address, err := url.Parse(publicURL)
	if err != nil || (address.Scheme != "http" && address.Scheme != "https") || address.Hostname() == "" || address.User != nil || address.RawQuery != "" || address.Fragment != "" {
		return "", errFormatMessage
	}
	escapedPrefix := strings.TrimRight(address.EscapedPath(), "/")
	address.Path = strings.TrimRight(address.Path, "/") + path
	address.RawPath = escapedPrefix + path
	address.RawQuery = query.Encode()
	return address.String(), nil
}
