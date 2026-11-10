package server

import "net/http"

func (s *Server) credentialAction(w http.ResponseWriter, r *http.Request, body any, validate func() error, action func() (any, error)) {
	attempt := s.beginAttempt(w, r)
	if attempt == nil {
		return
	}
	defer attempt.finish(r.Context(), attemptNeutral)
	err := decodeJSON(w, r, body)
	if err == nil {
		err = validate()
	}
	var value any
	if err == nil {
		value, err = action()
	}
	// 修改安全设置不是一次新登录，不能解除登录失败事件。
	if !s.finishAttempt(w, r, attempt, err, false) {
		return
	}
	if value == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	WriteJSON(w, r, http.StatusOK, value)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Old string `json:"old_password"`
		New string `json:"new_password"`
	}
	s.credentialAction(w, r, &body, func() error {
		return required(map[string]string{"old_password": body.Old, "new_password": body.New})
	}, func() (any, error) {
		return nil, s.auth.ChangePassword(r.Context(), currentIdentity(r), body.Old, body.New)
	})
}

func (s *Server) beginTOTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	s.credentialAction(w, r, &body, func() error {
		return required(map[string]string{"password": body.Password})
	}, func() (any, error) {
		setup, err := s.auth.BeginTOTP(r.Context(), currentIdentity(r), body.Password)
		if err != nil {
			return nil, err
		}
		return struct {
			SetupToken string `json:"setup_token"`
			Secret     string `json:"secret"`
			URI        string `json:"uri"`
			ExpiresAt  int64  `json:"expires_at"`
		}{setup.SetupToken, setup.Secret, setup.URI, setup.ExpiresAt}, nil
	})
}

func (s *Server) enableTOTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SetupToken string `json:"setup_token"`
		Code       string `json:"code"`
	}
	s.credentialAction(w, r, &body, func() error {
		if err := required(map[string]string{"setup_token": body.SetupToken, "code": body.Code}); err != nil {
			return err
		}
		if !validCode(body.Code) {
			return invalidCode()
		}
		return nil
	}, func() (any, error) {
		return nil, s.auth.EnableTOTP(r.Context(), currentIdentity(r), body.SetupToken, body.Code)
	})
}

func (s *Server) disableTOTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	s.credentialAction(w, r, &body, func() error {
		if err := required(map[string]string{"password": body.Password, "code": body.Code}); err != nil {
			return err
		}
		if !validCode(body.Code) {
			return invalidCode()
		}
		return nil
	}, func() (any, error) {
		return nil, s.auth.DisableTOTP(r.Context(), currentIdentity(r), body.Password, body.Code)
	})
}
