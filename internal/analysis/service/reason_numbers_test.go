package modelservice

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
)

func TestProviderStoredReasonsPreserveOpaqueNumbersOnEveryRead(t *testing.T) {
	for _, sample := range []struct {
		name string
		raw  string
		args map[string]any
	}{
		{"large integers", `{"code":"legacy.provider.reason","args":{"above_safe":9007199254740993,"above_int64":18446744073709551616}}`,
			map[string]any{"above_safe": json.Number("9007199254740993"), "above_int64": json.Number("18446744073709551616")}},
		{"large exponents", `{"code":"legacy.provider.reason","args":{"positive":1e1000,"negative":-1e1000}}`,
			map[string]any{"positive": json.Number("1e1000"), "negative": json.Number("-1e1000")}},
		{"nested numbers", `{"code":"legacy.provider.reason","args":{"nested":[9007199254740993,{"value":1e1000,"fraction":0.12345678901234567890123456789}],"text":"9007199254740993","flag":true,"none":null}}`,
			map[string]any{"nested": []any{json.Number("9007199254740993"), map[string]any{"value": json.Number("1e1000"), "fraction": json.Number("0.12345678901234567890123456789")}}, "text": "9007199254740993", "flag": true, "none": nil}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			f := newModelFixture(t)
			provider := f.create(t, modelInput())
			f.exec(t, `UPDATE ai_providers SET error_state=?,error_at=? WHERE id=?`, sample.raw, f.now.Load(), provider.ID)
			clock := modelScalar[string](t, f, `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey)
			f.mu.Lock()
			published := len(f.published)
			f.mu.Unlock()
			if err := ValidateStored(t.Context(), f.db, f.cipher); err != nil {
				t.Fatal("合法历史原因没有通过恢复校验", err)
			}
			reads := []struct {
				name string
				read func(context.Context) (*locales.Message, error)
			}{
				{"GetProvider", func(ctx context.Context) (*locales.Message, error) {
					value, err := f.s.GetProvider(ctx, provider.ID)
					return value.ErrorState, err
				}},
				{"ListProviders", func(ctx context.Context) (*locales.Message, error) {
					values, err := f.s.ListProviders(ctx)
					if err != nil || len(values) != 1 {
						return nil, errors.Join(err, errors.New("服务商列表不完整"))
					}
					return values[0].ErrorState, nil
				}},
				{"Provider", func(ctx context.Context) (*locales.Message, error) {
					value, err := f.s.Provider(ctx, provider.ID)
					return value.ErrorState, err
				}},
				{"Providers", func(ctx context.Context) (*locales.Message, error) {
					values, err := f.s.Providers(ctx)
					if err != nil || len(values) != 1 {
						return nil, errors.Join(err, errors.New("请求配置列表不完整"))
					}
					return values[0].ErrorState, nil
				}},
				{"loadProviderTx", func(ctx context.Context) (*locales.Message, error) {
					tx, err := f.db.BeginTx(ctx, nil)
					if err != nil {
						return nil, err
					}
					defer tx.Rollback()
					value, err := loadProviderTx(ctx, tx, provider.ID)
					return value.view.ErrorState, err
				}},
			}
			for _, read := range reads {
				t.Run(read.name, func(t *testing.T) {
					reason, err := read.read(t.Context())
					if err != nil {
						t.Fatal("合法历史原因读取失败", err)
					}
					if reason == nil || reason.Code != "legacy.provider.reason" || !reflect.DeepEqual(reason.Args, sample.args) {
						t.Fatalf("原因参数被转换或舍入：%#v，期望%#v", reason, sample.args)
					}
					encoded, err := json.Marshal(reason)
					want, wantErr := json.Marshal(locales.Message{Code: "legacy.provider.reason", Args: sample.args})
					if err != nil || wantErr != nil || string(encoded) != string(want) {
						t.Fatal("原因参数无法原数值序列化", err, wantErr)
					}
				})
			}
			f.mu.Lock()
			unchanged := len(f.published) == published
			f.mu.Unlock()
			if !unchanged || modelScalar[string](t, f, `SELECT error_state FROM ai_providers WHERE id=?`, provider.ID) != sample.raw || modelScalar[string](t, f, `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey) != clock {
				t.Fatal("读取改写了原始原因、时钟或发布了事件")
			}
		})
	}
}

func TestProviderStoredReasonNumberFixKeepsJSONBoundaryChecks(t *testing.T) {
	f := newModelFixture(t)
	provider := f.create(t, modelInput())
	for _, raw := range []string{
		`{"code":"legacy.reason","args":{"private":1e1000}} {"code":"trailing"}`,
		`{"code":"legacy.reason","args":{"private":1e}}`,
		`{"code":"legacy.reason","args":{"private":1e1000},"unknown":true}`,
	} {
		f.exec(t, `UPDATE ai_providers SET error_state=? WHERE id=?`, raw, provider.ID)
		value, err := f.s.GetProvider(t.Context(), provider.ID)
		var unavailable *service.ServiceError
		if !errors.As(err, &unavailable) || unavailable.Kind != service.ErrorUnavailable || value.ErrorState != nil || strings.Contains(err.Error(), "private") {
			t.Fatalf("损坏原因未被安全拒绝：%#v %v", value, err)
		}
		if err := ValidateStored(t.Context(), f.db, f.cipher); err == nil {
			t.Fatal("恢复校验放过了损坏原因")
		}
		if modelScalar[string](t, f, `SELECT error_state FROM ai_providers WHERE id=?`, provider.ID) != raw {
			t.Fatal("坏历史原因被读取路径改写")
		}
	}
}
