package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
)

const (
	cookieName       = "kakatta_session"
	secureCookieName = "__Host-kakatta_session"
)

func (s *Server) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, kind, err := credentials(r, stateOf(r).peer.Secure)
		if err != nil {
			s.clearCookie(w, r)
			WriteError(w, r, http.StatusUnauthorized, message("auth.unauthenticated"), nil)
			return
		}
		var identity auth.Identity
		readOnly := false
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && s.runtimeFailure != nil && s.runtimeFailure() != nil {
			switch r.URL.Path {
			case "/api/v1/me", "/api/v1/settings", "/api/v1/settings/definitions", "/api/v1/system/status", "/api/v1/system/diagnostics", "/api/v1/system/diagnostics/download":
				readOnly = true
			}
		}
		if readOnly {
			identity, err = s.auth.AuthenticateReadOnly(r.Context(), token, kind)
		} else {
			identity, err = s.auth.Authenticate(r.Context(), token, kind)
		}
		if err != nil {
			if errors.Is(err, auth.ErrUnauthenticated) {
				s.clearCookie(w, r)
			}
			s.writeFailure(w, r, err)
			return
		}
		if kind == auth.WebSession && !readOnly {
			s.setCookie(w, r, token, identity.CookieExpiresAt)
		}
		ctx := context.WithValue(r.Context(), identityKey, identity)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func credentials(r *http.Request, secure bool) (string, auth.Kind, error) {
	var cookie string
	cookieCount := 0
	for _, value := range r.Cookies() {
		if value.Name != cookieName && value.Name != secureCookieName {
			continue
		}
		cookieCount++
		if value.Name == cookieName && secure || value.Name == secureCookieName && !secure {
			return "", "", auth.ErrUnauthenticated
		}
		cookie = value.Value
	}
	if cookieCount > 1 {
		return "", "", auth.ErrUnauthenticated
	}
	authorization := r.Header.Values("Authorization")
	if len(authorization) > 1 || len(authorization) != 0 && cookieCount != 0 {
		return "", "", auth.ErrUnauthenticated
	}
	if len(authorization) == 1 {
		parts := strings.Fields(authorization[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return "", "", auth.ErrUnauthenticated
		}
		return parts[1], auth.APIToken, nil
	}
	if cookieCount == 1 && cookie != "" {
		return cookie, auth.WebSession, nil
	}
	return "", "", auth.ErrUnauthenticated
}

func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, token string, expiresAt int64) {
	secure := stateOf(r).peer.Secure
	name := cookieName
	if secure {
		name = secureCookieName
		// 从明文入口换到加密入口时，清掉旧 Cookie，避免两个凭据并存。
		s.expireCookie(w, cookieName, false)
	}
	seconds := int((expiresAt - s.now().UnixMilli()) / 1000)
	if seconds < 1 {
		seconds = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: token, Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode, Expires: time.UnixMilli(expiresAt).UTC(), MaxAge: seconds,
	})
}

func (s *Server) clearCookie(w http.ResponseWriter, r *http.Request) {
	s.expireCookie(w, cookieName, false)
	if stateOf(r).peer.Secure {
		s.expireCookie(w, secureCookieName, true)
	}
}

func (s *Server) expireCookie(w http.ResponseWriter, name string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
		Expires: time.Unix(1, 0).UTC(), MaxAge: -1,
	})
}

func (s *Server) beginAttempt(w http.ResponseWriter, r *http.Request) *loginAttempt {
	attempt, wait, err := s.limiter.begin(r.Context(), stateOf(r).peer.IP.String())
	if err != nil {
		WriteError(w, r, http.StatusServiceUnavailable, message("http.unavailable"), nil)
		return nil
	}
	if attempt == nil {
		seconds := max(1, int64((wait+time.Second-1)/time.Second))
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		WriteError(w, r, http.StatusTooManyRequests, locales.Message{Code: "auth.rate_limited", Args: map[string]any{"seconds": seconds}}, nil)
	}
	return attempt
}

func (s *Server) finishAttempt(w http.ResponseWriter, r *http.Request, attempt *loginAttempt, err error, fullLogin bool) bool {
	outcome := attemptNeutral
	if err != nil && !errors.Is(err, auth.ErrStorage) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		outcome = attemptFailure
	} else if err == nil && fullLogin {
		outcome = attemptSuccess
	}
	if finishErr := attempt.finish(r.Context(), outcome); finishErr != nil {
		WriteError(w, r, http.StatusServiceUnavailable, message("http.unavailable"), nil)
		return false
	}
	if err != nil {
		s.writeFailure(w, r, err)
		return false
	}
	return true
}

func (s *Server) writeFailure(w http.ResponseWriter, r *http.Request, err error) {
	var input *requestError
	if errors.As(err, &input) {
		WriteError(w, r, input.status, message(input.code), input.fields)
		return
	}
	var validation *settings.ValidationError
	if errors.As(err, &validation) {
		WriteError(w, r, http.StatusUnprocessableEntity, message("settings.invalid"), validation.Issues)
		return
	}
	for _, choice := range []struct {
		err    error
		status int
	}{
		{auth.ErrInvalidCredentials, http.StatusUnauthorized},
		{auth.ErrUnauthenticated, http.StatusUnauthorized},
		{auth.ErrSetupExpired, http.StatusUnauthorized},
		{auth.ErrChallengeExpired, http.StatusUnauthorized},
		{auth.ErrAlreadyInitialized, http.StatusConflict},
		{auth.ErrInvalidInput, http.StatusBadRequest},
		{auth.ErrNotFound, http.StatusNotFound},
		{auth.ErrTOTPEnabled, http.StatusConflict},
		{auth.ErrTOTPDisabled, http.StatusConflict},
		{auth.ErrStorage, http.StatusServiceUnavailable},
	} {
		if errors.Is(err, choice.err) {
			WriteError(w, r, choice.status, message(choice.err.Error()), nil)
			return
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		WriteError(w, r, http.StatusServiceUnavailable, message("http.unavailable"), nil)
		return
	}
	WriteError(w, r, http.StatusInternalServerError, message("http.internal"), nil)
}
