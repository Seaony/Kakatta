package modelservice

import (
	"errors"
	"testing"

	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/service"
)

func TestProviderReorderRejectsUnsafeTypedIDs(t *testing.T) {
	f := newModelFixture(t)
	p := f.create(t, modelInput())
	for _, id := range []int64{0, -1, limits.MaxSafeInteger + 1} {
		_, err := f.s.ReorderProviders(t.Context(), []int64{id})
		var business *service.ServiceError
		if !errors.As(err, &business) || business.Kind != service.ErrorInvalid || business.Reason.Code != "ai.provider_invalid" {
			t.Fatalf("不安全编号没有在输入边界被拒绝：%d %v", id, err)
		}
	}
	if modelScalar[int64](t, f, `SELECT sort_index FROM ai_providers WHERE id=?`, p.ID) != 0 {
		t.Fatal("非法排序更改了持久数据")
	}
}
