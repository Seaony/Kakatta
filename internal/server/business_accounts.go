package server

import (
	"context"
	"net/http"
	"time"

	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/service"
)

func (b *businessHandler) registerAccountRoutes() {
	b.register("GET /api/v1/accounts", http.HandlerFunc(b.listAccounts))
	b.register("POST /api/v1/accounts", http.HandlerFunc(b.createAccount))
	b.register("GET /api/v1/accounts/{account_id}", http.HandlerFunc(b.getAccount))
	b.register("PATCH /api/v1/accounts/{account_id}", http.HandlerFunc(b.updateAccount))
	b.register("DELETE /api/v1/accounts/{account_id}", http.HandlerFunc(b.deleteAccount))
	b.register("POST /api/v1/accounts/{account_id}/logout", http.HandlerFunc(b.logoutAccount))
	b.register("POST /api/v1/accounts/{account_id}/login", http.HandlerFunc(b.loginAccount))
	b.register("POST /api/v1/accounts/{account_id}/verification", http.HandlerFunc(b.verifyAccount))
	b.register("POST /api/v1/accounts/{account_id}/verification/confirm", http.HandlerFunc(b.confirmAccount))
	b.register("GET /api/v1/accounts/{account_id}/sessions/{session_id}", http.HandlerFunc(b.getAccountSession))
	b.register("DELETE /api/v1/accounts/{account_id}/sessions/{session_id}", http.HandlerFunc(b.closeAccountSession))
	b.register("GET /api/v1/proxies", http.HandlerFunc(b.listProxies))
	b.register("POST /api/v1/proxies", http.HandlerFunc(b.createProxy))
	b.register("PATCH /api/v1/proxies/{proxy_id}", http.HandlerFunc(b.updateProxy))
	b.register("DELETE /api/v1/proxies/{proxy_id}", http.HandlerFunc(b.deleteProxy))
	b.register("POST /api/v1/proxies/{proxy_id}/test", http.HandlerFunc(b.testProxy))
	b.register("GET /remote/sessions/{session_id}", http.HandlerFunc(b.accountRemote))
	b.register("GET /remote/sessions/{session_id}/", http.HandlerFunc(b.accountRemote))
}

func accountRouteID(r *http.Request, name string) (int64, error) {
	if _, err := queryValues(r); err != nil {
		return 0, err
	}
	return pathID(r, name)
}

func accountActionID(r *http.Request, name string) (int64, error) {
	if err := noBody(r); err != nil {
		return 0, err
	}
	return accountRouteID(r, name)
}

func accountListQuery(r *http.Request) (service.ListQuery, error) {
	query, err := queryValues(r, "limit", "cursor")
	if err != nil {
		return service.ListQuery{}, err
	}
	return queryList(query)
}

func (b *businessHandler) listAccounts(w http.ResponseWriter, r *http.Request) {
	query, err := accountListQuery(r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.ListAccounts(r.Context(), query)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) createAccount(w http.ResponseWriter, r *http.Request) {
	if _, err := queryValues(r); err != nil {
		b.failure(w, r, err)
		return
	}
	patch, err := readPatch(w, r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.CreateAccount(r.Context(), patch)
	b.reply(w, r, http.StatusCreated, value, err)
}

func (b *businessHandler) getAccount(w http.ResponseWriter, r *http.Request) {
	id, err := accountRouteID(r, "account_id")
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.GetAccount(r.Context(), id)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) updateAccount(w http.ResponseWriter, r *http.Request) {
	id, err := accountRouteID(r, "account_id")
	if err != nil {
		b.failure(w, r, err)
		return
	}
	patch, err := readPatch(w, r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.UpdateAccount(r.Context(), id, patch)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	id, err := accountActionID(r, "account_id")
	if err != nil {
		b.failure(w, r, err)
		return
	}
	b.reply(w, r, http.StatusNoContent, nil, b.service.DeleteAccount(r.Context(), id))
}

func (b *businessHandler) logoutAccount(w http.ResponseWriter, r *http.Request) {
	id, err := accountActionID(r, "account_id")
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.LogoutAccount(r.Context(), id)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) loginAccount(w http.ResponseWriter, r *http.Request) {
	id, err := accountActionID(r, "account_id")
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.OpenLogin(r.Context(), id)
	b.reply(w, r, http.StatusAccepted, value, err)
}

func (b *businessHandler) verifyAccount(w http.ResponseWriter, r *http.Request) {
	id, err := accountActionID(r, "account_id")
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.OpenVerification(r.Context(), id)
	b.reply(w, r, http.StatusAccepted, value, err)
}

func (b *businessHandler) confirmAccount(w http.ResponseWriter, r *http.Request) {
	id, err := accountRouteID(r, "account_id")
	if err != nil {
		b.failure(w, r, err)
		return
	}
	var body struct {
		SessionID string `json:"session_id"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		b.failure(w, r, err)
		return
	}
	if err := required(map[string]string{"session_id": body.SessionID}); err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.ConfirmVerification(r.Context(), id, body.SessionID)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) getAccountSession(w http.ResponseWriter, r *http.Request) {
	id, err := accountRouteID(r, "account_id")
	if err != nil {
		b.failure(w, r, err)
		return
	}
	sessionID, err := pathText(r, "session_id")
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.GetSession(r.Context(), id, sessionID)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) closeAccountSession(w http.ResponseWriter, r *http.Request) {
	id, err := accountActionID(r, "account_id")
	if err != nil {
		b.failure(w, r, err)
		return
	}
	sessionID, err := pathText(r, "session_id")
	if err != nil {
		b.failure(w, r, err)
		return
	}
	b.reply(w, r, http.StatusNoContent, nil, b.service.CloseSession(r.Context(), id, sessionID))
}

func (b *businessHandler) listProxies(w http.ResponseWriter, r *http.Request) {
	query, err := accountListQuery(r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.ListProxies(r.Context(), query)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) createProxy(w http.ResponseWriter, r *http.Request) {
	if _, err := queryValues(r); err != nil {
		b.failure(w, r, err)
		return
	}
	patch, err := readPatch(w, r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.CreateProxy(r.Context(), patch)
	b.reply(w, r, http.StatusCreated, value, err)
}

func (b *businessHandler) updateProxy(w http.ResponseWriter, r *http.Request) {
	id, err := accountRouteID(r, "proxy_id")
	if err != nil {
		b.failure(w, r, err)
		return
	}
	patch, err := readPatch(w, r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.UpdateProxy(r.Context(), id, patch)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) deleteProxy(w http.ResponseWriter, r *http.Request) {
	id, err := accountActionID(r, "proxy_id")
	if err != nil {
		b.failure(w, r, err)
		return
	}
	b.reply(w, r, http.StatusNoContent, nil, b.service.DeleteProxy(r.Context(), id))
}

func (b *businessHandler) testProxy(w http.ResponseWriter, r *http.Request) {
	id, err := accountActionID(r, "proxy_id")
	if err != nil {
		b.failure(w, r, err)
		return
	}
	value, err := b.service.TestProxy(r.Context(), id)
	b.reply(w, r, http.StatusOK, value, err)
}

func (b *businessHandler) accountRemote(w http.ResponseWriter, r *http.Request) {
	if _, err := queryValues(r); err != nil {
		b.failure(w, r, err)
		return
	}
	if err := noBody(r); err != nil {
		b.failure(w, r, err)
		return
	}
	sessionID, err := pathText(r, "session_id")
	if err != nil {
		b.failure(w, r, err)
		return
	}
	prefix := "/remote/sessions/" + sessionID
	view, err := b.service.RemoteSession(r.Context(), sessionID)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	if r.URL.Path == prefix {
		http.Redirect(w, r, prefix+"/", http.StatusPermanentRedirect)
		return
	}
	viewer := r.URL.Path == prefix+"/vnc"
	peer := stateOf(r).peer
	request := r.Clone(r.Context())
	request.Host = peer.Host
	if viewer {
		request.Host = sameOriginHost(r, peer)
		if r.Method != http.MethodGet || request.Host == "" {
			WriteError(w, r, http.StatusForbidden, message("http.forbidden"), nil)
			return
		}
		ctx, cancel := context.WithCancel(request.Context())
		request = request.WithContext(ctx)
		done := make(chan struct{})
		go b.watchAccountViewer(ctx, cancel, currentIdentity(r), done)
		defer func() { cancel(); <-done }()
	}
	handler := view.HandlerWithViewer(func(r *http.Request) bool {
		_, authenticated := IdentityFromContext(r.Context())
		return authenticated && r.Context().Err() == nil
	}, func(ctx context.Context) error {
		return b.service.ViewerOpened(ctx, sessionID)
	})
	http.StripPrefix(prefix, handler).ServeHTTP(w, request)
}

func (b *businessHandler) watchAccountViewer(ctx context.Context, cancel context.CancelFunc, identity auth.Identity, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-b.server.closed:
			cancel()
			return
		case <-ticker.C:
			check, stop := context.WithTimeout(ctx, 5*time.Second)
			err := b.server.auth.Validate(check, identity)
			stop()
			if err != nil {
				cancel()
				return
			}
		}
	}
}
