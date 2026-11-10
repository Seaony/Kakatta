package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
)

type businessHandler struct {
	server  *Server
	service *service.Service
	routes  []businessRoute
}

type businessRoute struct {
	pattern string
	handler http.Handler
}

func businessRoutes(server *Server, business *service.Service) []businessRoute {
	handler := &businessHandler{server: server, service: business}
	handler.registerTaskRoutes()
	handler.registerAccountRoutes()
	handler.registerResultRoutes()
	return handler.routes
}

func (b *businessHandler) register(pattern string, handler http.Handler) {
	b.routes = append(b.routes, businessRoute{pattern: pattern, handler: handler})
}

func (s *Server) RegisterService(business *service.Service) error {
	if business == nil {
		return errors.New("业务服务不能为空")
	}
	probe := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/api/v1/tasks"}}
	if _, pattern := s.mux.Handler(probe); pattern != "" && pattern != "/" {
		return errors.New("业务接口已经注册")
	}
	for _, route := range businessRoutes(s, business) {
		s.RegisterProtected(route.pattern, route.handler)
	}
	return nil
}

func (b *businessHandler) reply(w http.ResponseWriter, r *http.Request, status int, value any, err error) {
	if err != nil {
		b.failure(w, r, err)
		return
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	WriteJSON(w, r, status, value)
}

func (b *businessHandler) failure(w http.ResponseWriter, r *http.Request, err error) {
	var transport *requestError
	if errors.As(err, &transport) {
		b.server.writeFailure(w, r, err)
		return
	}
	var business *service.ServiceError
	if errors.As(err, &business) {
		status := map[service.ErrorKind]int{
			service.ErrorBadRequest:  http.StatusBadRequest,
			service.ErrorInvalid:     http.StatusUnprocessableEntity,
			service.ErrorNotFound:    http.StatusNotFound,
			service.ErrorConflict:    http.StatusConflict,
			service.ErrorUnavailable: http.StatusServiceUnavailable,
		}[business.Kind]
		if status != 0 {
			args := make(map[string]any, len(business.Reason.Args)+1)
			for key, value := range business.Reason.Args {
				args[key] = value
			}
			args["next_allowed_at"] = business.NextAllowedAt
			WriteError(w, r, status, locales.Message{Code: business.Reason.Code, Args: args}, business.Fields)
			return
		}
	}
	WriteError(w, r, http.StatusInternalServerError, message("http.internal"), nil)
}

func readPatch(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, error) {
	raw, err := readJSONObject(w, r)
	if err != nil {
		return nil, err
	}
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(raw, &patch); err != nil {
		return nil, &requestError{status: http.StatusBadRequest, code: "http.invalid_json"}
	}
	return patch, nil
}

func invalidParameter(key string) error {
	return &requestError{status: http.StatusBadRequest, code: "http.invalid_request", fields: []settings.Issue{{Key: key, Reason: message("http.invalid_request")}}}
}

func positiveInteger(raw, key string) (int64, error) {
	if raw == "" {
		return 0, invalidParameter(key)
	}
	for _, char := range raw {
		if char < '0' || char > '9' {
			return 0, invalidParameter(key)
		}
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 1 {
		return 0, invalidParameter(key)
	}
	return value, nil
}

func pathID(r *http.Request, key string) (int64, error) {
	return positiveInteger(r.PathValue(key), key)
}

func pathText(r *http.Request, key string) (string, error) {
	value := r.PathValue(key)
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) {
		return "", invalidParameter(key)
	}
	return value, nil
}

func queryValues(r *http.Request, allowed ...string) (url.Values, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, invalidParameter("query")
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		permitted := false
		for _, name := range allowed {
			if name == key {
				permitted = true
				break
			}
		}
		if !permitted || len(values[key]) != 1 || !utf8.ValidString(key) || !utf8.ValidString(values.Get(key)) {
			return nil, invalidParameter(key)
		}
	}
	return values, nil
}

func queryList(values url.Values) (service.ListQuery, error) {
	query := service.ListQuery{Cursor: values.Get("cursor")}
	if values.Has("limit") {
		limit, err := positiveInteger(values.Get("limit"), "limit")
		if err != nil || limit > 200 {
			return service.ListQuery{}, invalidParameter("limit")
		}
		query.Limit = limit
	}
	return query, nil
}

func queryBool(values url.Values, key string) (*bool, error) {
	if !values.Has(key) {
		return nil, nil
	}
	value := values.Get(key)
	if value != "true" && value != "false" {
		return nil, invalidParameter(key)
	}
	parsed := value == "true"
	return &parsed, nil
}

func queryID(values url.Values, key string) (*int64, error) {
	if !values.Has(key) {
		return nil, nil
	}
	value, err := positiveInteger(values.Get(key), key)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func queryTime(values url.Values, key string) (*int64, error) {
	if !values.Has(key) {
		return nil, nil
	}
	raw := values.Get(key)
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || strings.HasPrefix(raw, "+") {
		return nil, invalidParameter(key)
	}
	return &value, nil
}

func noBody(r *http.Request) error {
	if r.ContentLength != 0 {
		return invalidParameter("body")
	}
	return nil
}
