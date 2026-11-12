package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/backup"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
)

type backupHandler struct {
	*businessHandler
	backup *backup.Service
}

func (s *Server) RegisterBackup(backups *backup.Service) error {
	if backups == nil {
		return errors.New("备份服务不能为空")
	}
	probe := &http.Request{Method: http.MethodPost, URL: &url.URL{Path: "/api/v1/backup/export"}}
	if _, pattern := s.mux.Handler(probe); pattern != "" && pattern != "/" {
		return errors.New("备份接口已经注册")
	}
	b := &backupHandler{businessHandler: &businessHandler{server: s}, backup: backups}
	s.RegisterProtected("POST /api/v1/backup/export", http.HandlerFunc(b.exportBackup))
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"POST /api/v1/backup/restore", b.prepareRestore},
		{"POST /api/v1/backup/confirm", b.confirmRestore},
	} {
		s.mux.Handle(route.pattern, b.firstRunOrAuthenticated(route.handler))
	}
	return nil
}

func (b *backupHandler) firstRunOrAuthenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, err := b.server.auth.Setup(r.Context())
		if err != nil {
			b.server.writeFailure(w, r, err)
			return
		}
		if status.Initialized {
			b.server.authenticated(next).ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func backupOwner(identity auth.Identity) string {
	if identity.TokenID == 0 {
		return "first-run"
	}
	return fmt.Sprintf("%s:%d", identity.Kind, identity.TokenID)
}

func (b *backupHandler) backupFailure(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		WriteError(w, r, http.StatusServiceUnavailable, message("http.unavailable"), nil)
		return
	}
	// 候选库内的凭据损坏属于上传文件错误；运行中认证存储故障仍报服务不可用。
	if !errors.Is(err, backup.ErrInvalidArchive) &&
		(errors.Is(err, auth.ErrStorage) || errors.Is(err, auth.ErrUnauthenticated) && !errors.Is(err, backup.ErrRestoreAuthorization)) {
		b.server.writeFailure(w, r, err)
		return
	}
	for _, item := range []struct {
		kind   error
		status int
		field  string
	}{
		{backup.ErrPasswordRequired, http.StatusBadRequest, "password"},
		{backup.ErrInvalidArchive, http.StatusUnprocessableEntity, "file"},
		{backup.ErrAuthentication, http.StatusUnprocessableEntity, "password"},
		{backup.ErrUnsupportedVersion, http.StatusUnprocessableEntity, "file"},
		{backup.ErrRestoreTicket, http.StatusConflict, "ticket"},
		{backup.ErrRestoreExpired, http.StatusConflict, "ticket"},
		{backup.ErrRestoreChanged, http.StatusConflict, "ticket"},
		{backup.ErrRestoreAuthorization, http.StatusForbidden, ""},
		{backup.ErrClosed, http.StatusServiceUnavailable, ""},
		{backup.ErrInvalidDependencies, http.StatusServiceUnavailable, ""},
		{backup.ErrIO, http.StatusServiceUnavailable, ""},
	} {
		if !errors.Is(err, item.kind) {
			continue
		}
		reason := message(item.kind.Error())
		var fields []settings.Issue
		if item.field != "" {
			fields = []settings.Issue{{Key: item.field, Reason: reason}}
		}
		WriteError(w, r, item.status, reason, fields)
		return
	}
	b.failure(w, r, err)
}

func (b *backupHandler) exportBackup(w http.ResponseWriter, r *http.Request) {
	_, err := queryValues(r)
	var input struct {
		Password *string `json:"password"`
	}
	if err == nil {
		err = decodeJSON(w, r, &input)
	}
	if err == nil && (input.Password == nil || *input.Password == "") {
		err = invalidParameter("password")
	}
	if err != nil {
		b.backupFailure(w, r, err)
		return
	}
	err = stagedDownload(w, r, func(dst io.Writer) (service.ExportInfo, error) {
		return service.ExportInfo{Filename: "kakatta-backup.zip", MediaType: "application/zip"}, b.backup.Export(r.Context(), *input.Password, dst)
	})
	if err != nil {
		b.backupFailure(w, r, err)
	}
}

func (b *backupHandler) prepareRestore(w http.ResponseWriter, r *http.Request) {
	if _, err := queryValues(r); err != nil {
		b.backupFailure(w, r, err)
		return
	}
	file, password, err := readUpload(w, r, true)
	if err != nil {
		b.backupFailure(w, r, err)
		return
	}
	defer removeHTTPTemp(file)
	stat, err := file.Stat()
	if err != nil {
		b.backupFailure(w, r, &requestError{status: http.StatusServiceUnavailable, code: "http.unavailable"})
		return
	}
	preview, err := b.backup.PrepareRestore(r.Context(), file, stat.Size(), password, backupOwner(currentIdentity(r)))
	if err != nil {
		b.backupFailure(w, r, err)
		return
	}
	b.reply(w, r, http.StatusOK, preview, nil)
}

func (b *backupHandler) confirmRestore(w http.ResponseWriter, r *http.Request) {
	_, err := queryValues(r)
	var input struct {
		Ticket *string `json:"ticket"`
	}
	if err == nil {
		err = decodeJSON(w, r, &input)
	}
	if err == nil && (input.Ticket == nil || *input.Ticket == "") {
		err = invalidParameter("ticket")
	}
	if err != nil {
		b.backupFailure(w, r, err)
		return
	}
	identity := currentIdentity(r)
	err = b.backup.ConfirmRestore(r.Context(), *input.Ticket, backupOwner(identity), func(ctx context.Context) error {
		if identity.TokenID == 0 {
			status, err := b.server.auth.Setup(ctx)
			if err != nil {
				return err
			}
			if status.Initialized {
				return auth.ErrUnauthenticated
			}
			return nil
		}
		return b.server.auth.Validate(ctx, identity)
	})
	if err != nil {
		b.backupFailure(w, r, err)
		return
	}
	b.server.clearCookie(w, r)
	b.reply(w, r, http.StatusOK, struct {
		Restored bool `json:"restored"`
	}{Restored: true}, nil)
}
