package server

import (
	"net/http"
	"testing"
)

func TestRevokeTokenSafeIntegerBoundary(t *testing.T) {
	f := newServerFixture(t, true)
	requireStatus(t, f.request(http.MethodDelete, "/api/v1/security/tokens/9007199254740991", "", true), http.StatusNotFound)
	for _, id := range []string{"9007199254740992", "9223372036854775807", "-9007199254740991"} {
		response := f.request(http.MethodDelete, "/api/v1/security/tokens/"+id, "", true)
		requireStatus(t, response, http.StatusBadRequest)
		value := decodeBody[ErrorEnvelope](t, response)
		if value.Error.Code != "http.invalid_request" || len(value.Fields) != 1 || value.Fields[0].Key != "id" {
			t.Fatal("令牌编号越界必须指向id字段")
		}
	}
	requireStatus(t, f.request(http.MethodGet, "/api/v1/me", "", true), http.StatusOK)
}
