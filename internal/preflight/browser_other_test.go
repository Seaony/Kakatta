//go:build !linux

package preflight

import (
	"testing"

	"github.com/Seaony/Kakatta/internal/locales"
)

func TestBrowserPlatformRequirementUsesSelectedLanguage(t *testing.T) {
	for _, language := range []locales.Language{locales.ZhCN, locales.En, locales.Ja} {
		t.Run(string(language), func(t *testing.T) {
			checks := browserChecks(t.Context(), Options{Language: language})
			if len(checks) != 4 {
				t.Fatalf("平台限制未逐项返回：%+v", checks)
			}
			for _, check := range checks {
				if check.Passed || check.Values != nil || check.Detail == "" || check.Action == "" || check.Detail != text(language, "linux_required", nil) || check.Action != text(language, "container_remedy", nil) {
					t.Errorf("平台限制丢失或未翻译：%+v", check)
				}
			}
		})
	}
}
