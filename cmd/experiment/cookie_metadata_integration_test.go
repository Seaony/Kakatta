package main

import (
	"reflect"
	"testing"
)

func TestControllerCookieMetadataBeforeNavigationAndAfterLogin(t *testing.T) {
	c := experimentController(t, 549)
	login, err := observeExperiment(t, c, c.login)
	if err != nil {
		t.Fatal(err)
	}
	assertExperimentReleased(t, c, login)
	stages := c.summary.Observations["cookie_metadata"].(map[string]any)["结果"].(map[string]any)
	startup := stages["after_startup"].(map[string]any)
	if startup["available"] != true || len(startup["cookies"].([]any)) != 0 {
		t.Fatal("新配置的启动观察必须早于假登录页写入 Cookie", startup)
	}
	loggedIn := metadataCookie(t, stages["after_login"].(map[string]any), "unb")
	if loggedIn["domain"] != "127.0.0.1" || loggedIn["session"] != false || loggedIn["expires"].(float64) <= 0 {
		t.Fatal("登录后的持久 Cookie 元数据不完整", loggedIn)
	}
	advanceExperimentGap(t, c)
	run, err := observeExperiment(t, c, c.run)
	if err != nil {
		t.Fatal(err)
	}
	assertExperimentReleased(t, c, run)
	stages = c.summary.Observations["cookie_metadata"].(map[string]any)["结果"].(map[string]any)
	restarted := metadataCookie(t, stages["after_startup"].(map[string]any), "unb")
	if !reflect.DeepEqual(loggedIn, restarted) {
		t.Fatal("下一次启动必须在导航前保留上次登录元数据，不能先由页面重写", loggedIn, restarted)
	}
	if _, ok := stages["after_login"]; ok {
		t.Fatal("单次搜索不应伪造本次重新登录的观察")
	}
}

func metadataCookie(t *testing.T, observation map[string]any, name string) map[string]any {
	t.Helper()
	if observation["available"] != true {
		t.Fatal("Cookie 元数据没有成功读到", observation)
	}
	for _, entry := range observation["cookies"].([]any) {
		metadata := entry.(map[string]any)
		if metadata["name"] == name {
			if len(metadata) != 4 {
				t.Fatal("Cookie 元数据混入了额外字段", metadata)
			}
			return metadata
		}
	}
	t.Fatal("Cookie 元数据里缺少指定名称", name, observation)
	return nil
}
