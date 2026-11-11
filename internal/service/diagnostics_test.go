package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/xianyu"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func TestSearchParseDiagnosticsUseExactFailedResponse(t *testing.T) {
	for _, test := range []struct {
		name                string
		later, writeFailure bool
	}{
		{"首屏半截JSON", false, false}, {"后页缺列表", true, false}, {"后页取证写失败", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			broken := &browser.Response{URL: "http://127.0.0.1/h5/mtop.local?id=7", Body: []byte(`{"data":`)}
			if test.later {
				broken.Body = []byte(`{"ret":["SUCCESS"],"data":{}}`)
			}
			first := broken
			if test.later {
				first = searchPageResponse("1")
			}
			accepted, warned, reported := 0, 0, 0
			err := collectSearchPages(t.Context(), first, 3,
				func(context.Context) (*browser.Response, bool, error) { return broken, true, nil },
				func(_ int64, _ []xianyu.SearchItem) { accepted++ },
				func(_ int64, _ error) { warned++ },
				func(_ context.Context, step string, cause error, response *browser.Response) error {
					reported++
					if response != broken || step == "" || cause == nil {
						t.Fatal("取证没有收到准确的失败响应与所在步骤")
					}
					if test.writeFailure {
						return errors.Join(cause, web.ErrDiagnostics)
					}
					return cause
				})
			if reported != 1 {
				t.Fatalf("没有为解析失败明确取证：%d", reported)
			}
			if test.writeFailure {
				if !errors.Is(err, web.ErrDiagnostics) || accepted != 1 || warned != 0 {
					t.Fatalf("诊断写失败被当成普通后页失败吞掉：%v，%d，%d", err, accepted, warned)
				}
			} else if test.later {
				if err != nil || accepted != 1 || warned != 1 {
					t.Fatalf("取证改掉了已完成页面的保留：%v，%d，%d", err, accepted, warned)
				}
			} else if err == nil || accepted != 0 || warned != 0 {
				t.Fatalf("首屏解析失败被放过：%v", err)
			}
		})
	}
}

func TestBrowserFailureStepKeepsSafeReasonAndFailsStatus(t *testing.T) {
	message, status := browserRunMessage("步骤失败", map[string]any{
		"步骤": "查找搜索框", "分类": "element", "原因": "访问 http://user:fixture-password@127.0.0.1/?id=7&token=fixture-token 失败",
		"网址": "http://127.0.0.1/item?id=7&token=fixture-token", "信号": "本地测试信号",
	})
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"fixture-password", "fixture-token"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatal("运行步骤包含敏感测试值")
		}
	}
	if status != StepFail || message.Code != "run.browser_failed" || message.Args["step"] != "查找搜索框" || message.Args["分类"] != "element" {
		t.Fatalf("运行步骤丢掉失败或定位信息：%+v，%s", message, status)
	}
}
