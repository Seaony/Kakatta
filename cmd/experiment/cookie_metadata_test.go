package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/browser"
)

func TestCookieMetadataDiagnosticsKeepStagesAndOnlySafeFields(t *testing.T) {
	c := newDiagnosticsController(t)
	var output bytes.Buffer
	c.sink = &output
	c.beginSummary("login")
	c.logCookieMetadata("after_startup", nil, nil)
	c.logCookieMetadata("after_login", []browser.CookieMetadata{{Name: "unb", Domain: ".local.test", Session: true, Expires: -1}}, nil)
	c.phase("completed", nil)
	files, err := filepath.Glob(filepath.Join(experimentLogDir(c.opts), "summaries", "*", "summary.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("元数据摘要没有落盘：%v，%v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var summary experimentSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatal(err)
	}
	stages := summary.Observations["cookie_metadata"].(map[string]any)["结果"].(map[string]any)
	startup := stages["after_startup"].(map[string]any)
	login := stages["after_login"].(map[string]any)
	if startup["available"] != true || len(startup["cookies"].([]any)) != 0 || login["available"] != true {
		t.Fatal("启动和登录后的观察相互覆盖或错误标记", stages)
	}
	metadata := login["cookies"].([]any)[0].(map[string]any)
	if len(metadata) != 4 || metadata["name"] != "unb" || metadata["domain"] != ".local.test" || metadata["session"] != true || metadata["expires"] != float64(-1) {
		t.Fatal("四项安全字段没有完整保存", metadata)
	}
	if !strings.Contains(output.String(), `"stage":"after_startup"`) || !strings.Contains(output.String(), `"stage":"after_login"`) {
		t.Fatal("日志没有标明比较阶段", output.String())
	}
}

func TestCookieMetadataFailureIsUnknownAndNeverPrintsProtocolError(t *testing.T) {
	for _, cause := range []error{errors.New("原始响应 credential-sentinel"), context.DeadlineExceeded, context.Canceled} {
		c := newDiagnosticsController(t)
		var output bytes.Buffer
		c.sink = &output
		c.beginSummary("run")
		c.logCookieMetadata("after_startup", []browser.CookieMetadata{{Name: "discard", Domain: ".local.test"}}, cause)
		c.mu.Lock()
		observation := c.summary.Observations["cookie_metadata"].(map[string]any)
		result := observation["结果"].(map[string]any)["after_startup"].(map[string]any)
		c.mu.Unlock()
		if observation["状态"] != "未知" || result["available"] != false || result["reason"] == "" {
			t.Fatal("读取失败必须明确标记为未知", observation)
		}
		if _, ok := result["cookies"]; ok {
			t.Fatal("读取失败不能用空列表或部分列表冒充成功", result)
		}
		if strings.Contains(output.String(), "credential-sentinel") || strings.Contains(output.String(), "discard") {
			t.Fatal("失败日志泄露了原始错误或部分响应")
		}
	}
}
