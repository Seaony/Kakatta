package web

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/xianyu"
)

func TestDiagnosticResponseOnlyLeavesRunnerOnParseFailure(t *testing.T) {
	r := &Runner{Log: func(string, map[string]any) {}}
	var got []Diagnostic
	r.Diagnose = func(_ context.Context, d Diagnostic) error { got = append(got, d); return nil }
	response := &browser.Response{URL: "https://h5api.m.goofish.com/h5/mtop.test/?token=private&id=42", Body: []byte(`{"ret":["SUCCESS"],"data":`)}
	failure := errors.New("响应格式不对")
	if err := r.ReportFailure(t.Context(), "搜索解析", failure, response); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != "parse" || string(got[0].Response) != string(response.Body) || strings.Contains(got[0].URL, "private") {
		t.Fatalf("没有保留准确的解析证据或网址泄露：%+v", got)
	}
	response.Body[0] = 'x'
	if got[0].Response[0] != '{' {
		t.Fatal("证据与浏览器响应共享了可变切片")
	}
	risk := &RiskError{Kind: ErrDenied, Where: "搜索结果", Signal: "响应头 bxpunish"}
	r.ReportFailure(t.Context(), "搜索结果", risk, nil)
	if len(got) != 2 || got[1].Kind != "risk" || len(got[1].Response) != 0 {
		t.Fatalf("风控保存了普通接口正文：%+v", got)
	}
}

func TestDiagnosticWriteFailureStopsPartialSellerHandling(t *testing.T) {
	r := &Runner{Log: func(string, map[string]any) {}, Diagnose: func(context.Context, Diagnostic) error { return errors.New("磁盘已满") }}
	err := r.ReportFailure(t.Context(), "卖家头部", errors.New("字段缺失"), &browser.Response{Body: []byte(`{}`)})
	if !errors.Is(err, ErrDiagnostics) || !IsStop(err) {
		t.Fatalf("诊断写入失败没有停手：%v", err)
	}
	out := SellerPage{}
	if got := out.recordFailure(t.Context(), "run.seller_head_failed", err); !errors.Is(got, ErrDiagnostics) {
		t.Fatalf("部分成功吞掉了诊断故障：%v", got)
	}
}

func TestRiskDiagnosticSourceAndPunishmentAction(t *testing.T) {
	for _, tc := range []struct{ name, body, source, action string }{
		{"ret", `{"ret":["RGV587_ERROR::请验证"],"data":{}}`, "ret", ""},
		{"data.url", `{"ret":["SUCCESS"],"data":{"url":"https://www.goofish.com/punish?action=deny&token=private-punishment-token"}}`, "data.url", "deny"},
		{"两者", `{"ret":["FAIL_SYS_USER_VALIDATE::请验证"],"data":{"url":"https://www.goofish.com/punish?action=captcha&token=private-punishment-token"}}`, "ret,data.url", "captcha"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind, ret, err := xianyu.CheckEnvelope([]byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			fields := responseRiskFields("搜索", &browser.Response{Seq: 4, URL: "https://h5api.m.goofish.com/h5/mtop.test/?token=private-request-token", Body: []byte(tc.body)}, kind, ret)
			if fields["来源"] != tc.source {
				t.Fatalf("风险命中位置不明确：%+v", fields)
			}
			if tc.action != "" && (fields["处罚动作"] != tc.action || fields["处罚网址命中"] != true) {
				t.Fatalf("处罚动作缺失：%+v", fields)
			}
			data, _ := json.Marshal(fields)
			if strings.Contains(string(data), "private-") || strings.Contains(string(data), "/punish?") {
				t.Fatalf("保存了完整处罚网址或凭据：%s", data)
			}
		})
	}
}
