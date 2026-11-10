package server

import (
	"errors"
	"net/http"
	"net/url"
	"reflect"

	"github.com/Seaony/Kakatta/internal/update"
)

func (s *Server) RegisterUpdate(checker update.API) error {
	if checker == nil || reflect.ValueOf(checker).Kind() == reflect.Pointer && reflect.ValueOf(checker).IsNil() {
		return errors.New("更新检查服务不能为空")
	}
	probe := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/api/v1/system/update"}}
	if _, pattern := s.mux.Handler(probe); pattern != "" && pattern != "/" {
		return errors.New("更新检查接口已经注册")
	}
	b := &businessHandler{server: s}
	s.RegisterProtected("GET /api/v1/system/update", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := queryValues(r)
		if err == nil {
			err = noBody(r)
		}
		if err != nil {
			b.failure(w, r, err)
			return
		}
		value, err := checker.Read(r.Context())
		b.reply(w, r, http.StatusOK, value, err)
	}))
	return nil
}
