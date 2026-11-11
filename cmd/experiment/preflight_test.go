package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestExperimentCheckUsesSystemJapaneseAndSkipsBrowserAfterFailedCheck(t *testing.T) {
	t.Setenv("LANG", "ja_JP.UTF-8")
	o := options{Data: t.TempDir(), Account: 1, Fixture: true}
	// Local 可供日志记录时间，但环境检查要求用户明确选择时区。
	s := experimentSettings{Timezone: "Local"}
	var output bytes.Buffer
	err := runSelfCheck(o, s, &output)
	if err == nil || !strings.Contains(err.Error(), "環境チェックに失敗しました") ||
		!strings.Contains(output.String(), "タイムゾーンを明示的に指定してください") ||
		!strings.Contains(output.String(), "環境チェック") || !strings.Contains(output.String(), "未実行：まず") ||
		strings.Contains(output.String(), "preflight.") {
		t.Fatal("实验自检没有使用系统日语或未保留前置失败", output.String(), err)
	}
}
