package modelservice

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis/client"
)

func TestCompatibilityAtomicallyMergesOnlyTowardLessDemandingSettings(t *testing.T) {
	f := newModelFixture(t)
	view := f.create(t, modelInput())
	p, _ := f.s.Provider(t.Context(), view.ID)
	key := client.CompatKey{Provider: p.Ref, BaseURL: p.BaseURL, Model: p.VisionModel}
	if got, err := f.s.LoadCompat(t.Context(), key); err != nil || !reflect.DeepEqual(got, client.DefaultCompat()) {
		t.Fatalf("只有真缺记录才应取默认：%+v %v", got, err)
	}
	requests := []client.Compat{
		{JSONMode: client.JSONObject, SendTemperature: true, MaxTokensParam: "max_tokens", MaxTokensFloor: modelPointer(int64(2000))},
		{JSONMode: client.JSONText, SendTemperature: false, MaxTokensParam: "max_tokens", MaxTokensFloor: modelPointer(int64(8000))},
		{JSONMode: client.JSONSchema, SendTemperature: true, MaxTokensParam: "max_completion_tokens", MaxTokensFloor: modelPointer(int64(3000))},
	}
	var workers sync.WaitGroup
	for _, request := range requests {
		workers.Go(func() {
			if _, err := f.s.SaveCompat(t.Context(), key, request); err != nil {
				t.Errorf("并发兼容保存失败：%v", err)
			}
		})
	}
	workers.Wait()
	want := client.Compat{JSONMode: client.JSONText, SendTemperature: false, MaxTokensParam: "max_completion_tokens", MaxTokensFloor: modelPointer(int64(8000))}
	got, err := f.s.SaveCompat(t.Context(), key, client.DefaultCompat())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("旧请求升级了兼容或丢了并发限制：%+v %v", got, err)
	}
	*got.MaxTokensFloor = 1
	loaded, err := f.s.LoadCompat(t.Context(), key)
	if err != nil || !reflect.DeepEqual(loaded, want) {
		t.Fatal("修改返回值污染了持久兼容")
	}
}

func TestCompatibilityRejectsWrongConfigurationURLAndModel(t *testing.T) {
	f := newModelFixture(t)
	view := f.create(t, modelInput())
	p, _ := f.s.Provider(t.Context(), view.ID)
	valid := client.CompatKey{Provider: p.Ref, BaseURL: p.BaseURL, Model: p.VisionModel}
	keys := []client.CompatKey{valid, valid, valid}
	keys[0].BaseURL += "/other"
	keys[1].Model = "not-configured"
	keys[2].Provider.ConfigToken = "old-token"
	for _, key := range keys {
		if _, err := f.s.LoadCompat(t.Context(), key); err != client.ErrConfigChanged {
			t.Fatalf("错误的配置读取未被拦住：%v", err)
		}
		if _, err := f.s.SaveCompat(t.Context(), key, client.DefaultCompat()); err != client.ErrConfigChanged {
			t.Fatalf("错误的配置写入未被拦住：%v", err)
		}
	}
	if modelScalar[int](t, f, `SELECT count(*) FROM ai_compat`) != 0 {
		t.Fatal("旧配置插入了新的兼容状态")
	}
}

func TestCompatibilityCorruptionAndWriteFailureNeverReturnDefault(t *testing.T) {
	f := newModelFixture(t)
	view := f.create(t, modelInput())
	p, _ := f.s.Provider(t.Context(), view.ID)
	key := client.CompatKey{Provider: p.Ref, BaseURL: p.BaseURL, Model: p.VisionModel}
	f.exec(t, `INSERT INTO ai_compat(provider_id,model,max_tokens_floor) VALUES(?,?,-1)`, view.ID, p.VisionModel)
	if _, err := f.s.LoadCompat(t.Context(), key); err == nil {
		t.Fatal("损坏兼容被当成默认")
	}
	if _, err := f.s.SaveCompat(t.Context(), key, client.DefaultCompat()); err == nil {
		t.Fatal("写入静默覆盖损坏兼容")
	}
	f.exec(t, `DELETE FROM ai_compat`)
	f.exec(t, `CREATE TRIGGER fail_compat BEFORE INSERT ON ai_compat BEGIN SELECT RAISE(ABORT,'local failure'); END`)
	if _, err := f.s.SaveCompat(t.Context(), key, client.DefaultCompat()); err == nil {
		t.Fatal("兼容写失败没有传出")
	}
	if modelScalar[int](t, f, `SELECT count(*) FROM ai_compat`) != 0 {
		t.Fatal("失败兼容写入仍然落盘")
	}
	for _, value := range []client.Compat{{}, {JSONMode: client.JSONText, MaxTokensParam: "bad"}, {JSONMode: client.JSONText, MaxTokensParam: "max_tokens", MaxTokensFloor: modelPointer(int64(-1))}} {
		if _, err := f.s.SaveCompat(t.Context(), key, value); err == nil || errors.Is(err, client.ErrConfigChanged) {
			t.Fatalf("无效参数应本地拒绝：%+v %v", value, err)
		}
	}
}
