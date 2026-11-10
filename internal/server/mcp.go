package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/buildinfo"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpHTTPContextKey struct{}

// RegisterMCP 让六个工具与网页共用同一个业务服务。
func (s *Server) RegisterMCP(business *service.Service) error {
	if business == nil {
		return errors.New("业务服务不能为空")
	}
	probe := &http.Request{Method: http.MethodPost, URL: &url.URL{Path: "/mcp"}}
	if _, pattern := s.mux.Handler(probe); pattern != "" && pattern != "/" {
		return errors.New("工具接口已经注册")
	}
	sdk := mcp.NewServer(&mcp.Implementation{Name: "Kakatta", Version: buildinfo.Version}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	})
	if err := registerMCPTools(sdk, business); err != nil {
		return err
	}
	sdk.AddReceivingMiddleware(s.mcpMiddleware)
	options := mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, MaxRequestBodyBytes: maxBody,
		PropagateRequestCancellation: true,
	}
	getServer := func(*http.Request) *mcp.Server { return sdk }
	direct := mcp.NewStreamableHTTPHandler(getServer, &options)
	options.DisableLocalhostProtection = true
	proxied := mcp.NewStreamableHTTPHandler(getServer, &options)
	s.RegisterProtected("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := IdentityFromContext(r.Context())
		if !ok || identity.Kind != auth.APIToken {
			WriteError(w, r, http.StatusUnauthorized, message("auth.unauthenticated"), nil)
			return
		}
		if len(r.Header.Values("Origin")) != 0 && !accountViewerOrigin(r, stateOf(r).peer) {
			WriteError(w, r, http.StatusForbidden, message("http.forbidden"), nil)
			return
		}
		if _, err := queryValues(r); err != nil {
			s.writeFailure(w, r, err)
			return
		}
		for _, header := range []string{"Content-Type", "MCP-Protocol-Version", "Mcp-Method", "Mcp-Name", "Mcp-Session-Id"} {
			if len(r.Header.Values(header)) > 1 {
				WriteError(w, r, http.StatusBadRequest, message("http.invalid_request"), nil)
				return
			}
		}
		if r.Method == http.MethodPost {
			if err := readMCPBody(w, r); err != nil {
				s.writeFailure(w, r, err)
				return
			}
		}
		trusted, err := mcpTrustedPeer(r)
		if err != nil {
			WriteError(w, r, http.StatusBadRequest, message("http.invalid_forwarded"), nil)
			return
		}
		httpContext, cancel := context.WithCancel(r.Context())
		defer cancel()
		// 只在请求存活时等待关闭，不给闲置客户端留后台会话。
		go func() {
			select {
			case <-s.closed:
				cancel()
			case <-httpContext.Done():
			}
		}()
		ctx := context.WithValue(httpContext, mcpHTTPContextKey{}, httpContext)
		r = r.WithContext(ctx)
		if trusted {
			proxied.ServeHTTP(w, r)
		} else {
			direct.ServeHTTP(w, r)
		}
	}))
	return nil
}

func mcpTrustedPeer(r *http.Request) (bool, error) {
	var trusted []string
	if err := json.Unmarshal(stateOf(r).settings.Values["security.trusted_proxies"], &trusted); err != nil {
		return false, err
	}
	prefixes, err := proxyPrefixes(trusted)
	if err != nil {
		return false, err
	}
	remote, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return false, err
	}
	return trustedProxy(remote.Addr().WithZone("").Unmap(), prefixes), nil
}

func readMCPBody(w http.ResponseWriter, r *http.Request) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return &requestError{status: http.StatusUnsupportedMediaType, code: "http.unsupported_media_type"}
	}
	defer r.Body.Close()
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			return &requestError{status: http.StatusRequestEntityTooLarge, code: "http.body_too_large"}
		}
		return &requestError{status: http.StatusBadRequest, code: "http.invalid_json"}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if !utf8.Valid(data) || !uniqueValue(decoder, 0) {
		return &requestError{status: http.StatusBadRequest, code: "http.invalid_json"}
	}
	if _, err := decoder.Token(); err != io.EOF {
		return &requestError{status: http.StatusBadRequest, code: "http.invalid_json"}
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	return nil
}

func (s *Server) mcpMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		// 旧协议会去掉请求取消，但会保留 context 中的值。
		if original, ok := ctx.Value(mcpHTTPContextKey{}).(context.Context); ok {
			merged, cancel := context.WithCancel(ctx)
			stop := context.AfterFunc(original, cancel)
			defer stop()
			defer cancel()
			if original.Err() != nil {
				cancel()
			}
			ctx = merged
		}
		if method == "tools/call" {
			identity, ok := IdentityFromContext(ctx)
			if !ok || identity.Kind != auth.APIToken {
				return mcpErrorResult(mcpErrorEnvelope(ctx, auth.ErrUnauthenticated)), nil
			}
			if err := s.auth.Validate(ctx, identity); err != nil {
				return mcpErrorResult(mcpErrorEnvelope(ctx, err)), nil
			}
		}
		result, err := next(ctx, method, request)
		if err != nil {
			var protocol *jsonrpc.Error
			if errors.As(err, &protocol) && protocol.Code != jsonrpc.CodeInternalError {
				return nil, &jsonrpc.Error{Code: protocol.Code, Message: "http.invalid_request"}
			}
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "http.internal"}
		}
		if value, ok := result.(*mcp.CallToolResult); ok && value.IsError && value.StructuredContent == nil {
			return mcpErrorResult(mcpErrorEnvelope(ctx, &requestError{code: "http.invalid_request"})), nil
		}
		return result, nil
	}
}

func mcpErrorResult(envelope ErrorEnvelope) *mcp.CallToolResult {
	data, _ := json.Marshal(envelope)
	return &mcp.CallToolResult{IsError: true, StructuredContent: json.RawMessage(data), Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}
}

func mcpErrorEnvelope(ctx context.Context, err error) ErrorEnvelope {
	value := ErrorEnvelope{Error: message("http.internal"), Fields: []settings.Issue{}, RequestID: RequestID(ctx)}
	var business *service.ServiceError
	var transport *requestError
	switch {
	case errors.As(err, &business):
		switch business.Kind {
		case service.ErrorBadRequest, service.ErrorInvalid, service.ErrorNotFound, service.ErrorConflict, service.ErrorUnavailable:
		default:
			return value
		}
		args := make(map[string]any, len(business.Reason.Args)+1)
		for key, arg := range business.Reason.Args {
			args[key] = arg
		}
		args["next_allowed_at"] = business.NextAllowedAt
		value.Error = locales.Message{Code: business.Reason.Code, Args: args}
		if business.Fields != nil {
			value.Fields = business.Fields
		}
	case errors.As(err, &transport):
		value.Error = message(transport.code)
		if transport.fields != nil {
			value.Fields = transport.fields
		}
	case errors.Is(err, auth.ErrUnauthenticated):
		value.Error = message("auth.unauthenticated")
	case errors.Is(err, auth.ErrStorage), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		value.Error = message("http.unavailable")
	}
	return value
}
