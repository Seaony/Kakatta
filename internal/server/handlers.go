package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/settings"
)

type meResponse struct {
	Username    string    `json:"username"`
	AuthKind    auth.Kind `json:"auth_kind"`
	TokenID     int64     `json:"token_id"`
	Language    string    `json:"language"`
	Appearance  string    `json:"appearance"`
	TOTPEnabled bool      `json:"totp_enabled"`
}

func currentIdentity(r *http.Request) auth.Identity {
	identity, _ := IdentityFromContext(r.Context())
	return identity
}

func meValue(r *http.Request, identity auth.Identity) meResponse {
	value := meResponse{
		Username: identity.Username, AuthKind: identity.Kind, TokenID: identity.TokenID,
		TOTPEnabled: identity.TOTPEnabled,
	}
	_ = json.Unmarshal(stateOf(r).settings.Values["general.language"], &value.Language)
	_ = json.Unmarshal(stateOf(r).settings.Values["general.appearance"], &value.Appearance)
	return value
}

func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	value, err := s.auth.Setup(r.Context())
	if err != nil {
		s.writeFailure(w, r, err)
		return
	}
	WriteJSON(w, r, http.StatusOK, value)
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	attempt := s.beginAttempt(w, r)
	if attempt == nil {
		return
	}
	defer attempt.finish(r.Context(), attemptNeutral)
	var body struct {
		Token    string `json:"token"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	err := decodeJSON(w, r, &body)
	if err == nil {
		err = required(map[string]string{"token": body.Token, "username": body.Username, "password": body.Password})
	}
	var session auth.Session
	if err == nil {
		session, err = s.auth.Initialize(r.Context(), body.Token, body.Username, body.Password, r.UserAgent())
	}
	if !s.finishAttempt(w, r, attempt, err, true) {
		return
	}
	s.setCookie(w, r, session.Token, session.ExpiresAt)
	WriteJSON(w, r, http.StatusOK, meValue(r, session.Identity))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	attempt := s.beginAttempt(w, r)
	if attempt == nil {
		return
	}
	defer attempt.finish(r.Context(), attemptNeutral)
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	err := decodeJSON(w, r, &body)
	if err == nil {
		err = required(map[string]string{"username": body.Username, "password": body.Password})
	}
	var result auth.LoginResult
	if err == nil {
		result, err = s.auth.PasswordLogin(r.Context(), body.Username, body.Password, r.UserAgent())
	}
	if !s.finishAttempt(w, r, attempt, err, result.Session != nil) {
		return
	}
	if result.Session != nil {
		s.setCookie(w, r, result.Session.Token, result.Session.ExpiresAt)
		WriteJSON(w, r, http.StatusOK, struct {
			Status string     `json:"status"`
			User   meResponse `json:"user"`
		}{"authenticated", meValue(r, result.Session.Identity)})
		return
	}
	WriteJSON(w, r, http.StatusOK, struct {
		Status    string `json:"status"`
		Challenge string `json:"challenge"`
		ExpiresAt int64  `json:"expires_at"`
	}{"totp_required", result.Challenge, result.ExpiresAt})
}

func (s *Server) loginTOTP(w http.ResponseWriter, r *http.Request) {
	attempt := s.beginAttempt(w, r)
	if attempt == nil {
		return
	}
	defer attempt.finish(r.Context(), attemptNeutral)
	var body struct {
		Challenge string `json:"challenge"`
		Code      string `json:"code"`
	}
	err := decodeJSON(w, r, &body)
	if err == nil {
		err = required(map[string]string{"challenge": body.Challenge, "code": body.Code})
	}
	if err == nil && !validCode(body.Code) {
		err = invalidCode()
	}
	var session auth.Session
	if err == nil {
		session, err = s.auth.VerifyChallenge(r.Context(), body.Challenge, body.Code)
	}
	if !s.finishAttempt(w, r, attempt, err, true) {
		return
	}
	s.setCookie(w, r, session.Token, session.ExpiresAt)
	WriteJSON(w, r, http.StatusOK, meValue(r, session.Identity))
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.Logout(r.Context(), currentIdentity(r)); err != nil {
		s.writeFailure(w, r, err)
		return
	}
	s.clearCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, r, http.StatusOK, meValue(r, currentIdentity(r)))
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, r, http.StatusOK, stateOf(r).settings)
}

func (s *Server) patchSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Values settings.Values `json:"values"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeFailure(w, r, err)
		return
	}
	if body.Values == nil {
		s.writeFailure(w, r, &requestError{status: http.StatusBadRequest, code: "http.invalid_request", fields: []settings.Issue{{Key: "values", Reason: message("http.invalid_request")}}})
		return
	}
	value, err := s.settings.Apply(r.Context(), body.Values)
	if err != nil {
		s.writeFailure(w, r, err)
		return
	}
	WriteJSON(w, r, http.StatusOK, value)
}

func (s *Server) settingDefinitions(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, r, http.StatusOK, s.settings.Definitions())
}

func (s *Server) tokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.auth.ListTokens(r.Context(), currentIdentity(r))
	if err != nil {
		s.writeFailure(w, r, err)
		return
	}
	WriteJSON(w, r, http.StatusOK, struct {
		Tokens []auth.Token `json:"tokens"`
	}{tokens})
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	err := decodeJSON(w, r, &body)
	if err == nil {
		err = required(map[string]string{"name": body.Name})
	}
	if err != nil {
		s.writeFailure(w, r, err)
		return
	}
	token, err := s.auth.CreateToken(r.Context(), currentIdentity(r), body.Name)
	if err != nil {
		s.writeFailure(w, r, err)
		return
	}
	WriteJSON(w, r, http.StatusCreated, struct {
		ID        int64  `json:"id"`
		Token     string `json:"token"`
		CreatedAt int64  `json:"created_at"`
	}{token.ID, token.Token, token.CreatedAt})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 || strings.TrimSpace(raw) != raw {
		s.writeFailure(w, r, &requestError{status: http.StatusBadRequest, code: "http.invalid_request", fields: []settings.Issue{{Key: "id", Reason: message("http.invalid_request")}}})
		return
	}
	if err := s.auth.RevokeToken(r.Context(), currentIdentity(r), id); err != nil {
		s.writeFailure(w, r, err)
		return
	}
	if id == currentIdentity(r).TokenID {
		s.clearCookie(w, r)
	}
	w.WriteHeader(http.StatusNoContent)
}

func validCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, value := range code {
		if value < '0' || value > '9' {
			return false
		}
	}
	return true
}

func invalidCode() error {
	return &requestError{status: http.StatusBadRequest, code: "http.invalid_request", fields: []settings.Issue{{Key: "code", Reason: message("http.invalid_request")}}}
}
