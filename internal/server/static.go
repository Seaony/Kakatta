package server

import (
	"bytes"
	"io/fs"
	"mime"
	"net/http"
	"net/netip"
	"path"
	"strconv"
	"strings"
	"time"
)

var sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")

func reservedPath(value string) bool {
	for _, prefix := range []string{"/api", "/mcp", "/remote", "/healthz", "/docs", "/openapi.json"} {
		if value == prefix || strings.HasPrefix(value, prefix+"/") {
			return true
		}
	}
	return false
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if reservedPath(r.URL.Path) {
		s.authenticated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete} {
				probe := r.Clone(r.Context())
				probe.Method = method
				if _, pattern := s.mux.Handler(probe); pattern != "/" && pattern != "" {
					w.Header().Set("Allow", method)
					WriteError(w, r, http.StatusMethodNotAllowed, message("http.method_not_allowed"), nil)
					return
				}
			}
			WriteError(w, r, http.StatusNotFound, message("http.not_found"), nil)
		})).ServeHTTP(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		WriteError(w, r, http.StatusMethodNotAllowed, message("http.method_not_allowed"), nil)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	if !fs.ValidPath(name) || strings.Contains(name, "\\") || s.web == nil {
		WriteError(w, r, http.StatusNotFound, message("http.not_found"), nil)
		return
	}
	data, err := fs.ReadFile(s.web, name)
	if err != nil && path.Ext(name) == "" && acceptsHTML(r.Header.Get("Accept")) {
		name = "index.html"
		data, err = fs.ReadFile(s.web, name)
	}
	if err != nil {
		WriteError(w, r, http.StatusNotFound, message("http.not_found"), nil)
		return
	}
	if name == "index.html" {
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		peer := stateOf(r).peer
		if !peer.Secure && peer.IP.IsGlobalUnicast() && !peer.IP.IsPrivate() && !sharedAddressSpace.Contains(peer.IP) {
			data = bytes.Replace(data, []byte(`data-kakatta-public-http="false"`), []byte(`data-kakatta-public-http="true"`), 1)
		}
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

func acceptsHTML(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		value, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil || value != "text/html" {
			continue
		}
		if raw, exists := params["q"]; exists {
			weight, err := strconv.ParseFloat(raw, 64)
			if err != nil || weight <= 0 || weight > 1 {
				continue
			}
		}
		if value == "text/html" {
			return true
		}
	}
	return false
}
