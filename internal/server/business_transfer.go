package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
)

type transferHandler struct{ *businessHandler }

func (s *Server) RegisterTransfers(core *service.Service) error {
	if core == nil {
		return errors.New("导入导出服务不能为空")
	}
	probe := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/api/v1/tasks/export"}}
	if _, pattern := s.mux.Handler(probe); pattern == "GET /api/v1/tasks/export" {
		return errors.New("导入导出接口已经注册")
	}
	b := &transferHandler{&businessHandler{server: s, service: core}}
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"GET /api/v1/tasks/export", b.exportTasks},
		{"POST /api/v1/tasks/import", b.importTasks},
		{"GET /api/v1/tasks/{task_id}/findings/export", b.exportFindings},
		{"GET /api/v1/runs/export", b.exportRuns},
	} {
		s.RegisterProtected(route.pattern, route.handler)
	}
	return nil
}

func removeHTTPTemp(file *os.File) {
	_ = file.Close()
	_ = os.Remove(file.Name())
}

func stagedDownload(w http.ResponseWriter, r *http.Request, generate func(io.Writer) (service.ExportInfo, error)) error {
	file, err := os.CreateTemp("", ".kakatta-download-")
	if err != nil {
		return &requestError{status: http.StatusServiceUnavailable, code: "http.unavailable"}
	}
	defer removeHTTPTemp(file)
	info, err := generate(file)
	if err != nil {
		return err
	}
	stat, err := file.Stat()
	if err != nil {
		return &requestError{status: http.StatusServiceUnavailable, code: "http.unavailable"}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return &requestError{status: http.StatusServiceUnavailable, code: "http.unavailable"}
	}
	if err := r.Context().Err(); err != nil {
		return &requestError{status: http.StatusServiceUnavailable, code: "http.unavailable"}
	}
	w.Header().Set("Content-Type", info.MediaType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Filename}))
	w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		// 下载已开始，断开后只停止传输，不能把 JSON 错误拼进文件。
		_, _ = io.Copy(w, file)
	}
	return nil
}

type uploadBody struct {
	ctx        context.Context
	controller *http.ResponseController
	io.ReadCloser
}

func (body *uploadBody) Read(data []byte) (int, error) {
	if err := body.ctx.Err(); err != nil {
		return 0, err
	}
	if err := body.controller.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return 0, err
	}
	return body.ReadCloser.Read(data)
}

func readUpload(w http.ResponseWriter, r *http.Request, password bool) (file *os.File, text string, err error) {
	if len(r.Header.Values("Content-Type")) != 1 {
		return nil, "", &requestError{status: http.StatusUnsupportedMediaType, code: "http.unsupported_media_type"}
	}
	media, parameters, parseErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if parseErr != nil || media != "multipart/form-data" {
		return nil, "", &requestError{status: http.StatusUnsupportedMediaType, code: "http.unsupported_media_type"}
	}
	if parameters["boundary"] == "" {
		return nil, "", invalidParameter("body")
	}
	r.Body = &uploadBody{ctx: r.Context(), controller: http.NewResponseController(w), ReadCloser: r.Body}
	reader, readErr := r.MultipartReader()
	if readErr != nil {
		return nil, "", invalidParameter("body")
	}
	defer func() {
		if err != nil && file != nil {
			removeHTTPTemp(file)
			file = nil
		}
	}()
	seen := map[string]bool{}
	for {
		part, partErr := reader.NextPart()
		if partErr == io.EOF {
			break
		}
		if partErr != nil {
			return file, "", invalidParameter("body")
		}
		name := part.FormName()
		if seen[name] || name != "file" && (name != "password" || !password) {
			return file, "", invalidParameter("body")
		}
		seen[name] = true
		if name == "file" {
			file, err = os.CreateTemp("", ".kakatta-upload-")
			if err != nil {
				return nil, "", &requestError{status: http.StatusServiceUnavailable, code: "http.unavailable"}
			}
			if _, copyErr := io.Copy(file, part); copyErr != nil {
				var pathErr *os.PathError
				if errors.As(copyErr, &pathErr) {
					return file, "", &requestError{status: http.StatusServiceUnavailable, code: "http.unavailable"}
				}
				return file, "", invalidParameter("file")
			}
		} else {
			value, valueErr := io.ReadAll(io.LimitReader(part, maxBody+1))
			if valueErr != nil || !utf8.Valid(value) {
				return file, "", invalidParameter("password")
			}
			if len(value) > maxBody {
				return file, "", &requestError{status: http.StatusRequestEntityTooLarge, code: "http.body_too_large"}
			}
			text = string(value)
		}
		if closeErr := part.Close(); closeErr != nil {
			return file, "", invalidParameter("body")
		}
	}
	// 表单分界结束后仍要确认 HTTP 请求体完整，避免截断请求先写入数据。
	if _, drainErr := io.Copy(io.Discard, r.Body); drainErr != nil {
		return file, "", invalidParameter("body")
	}
	if file == nil {
		return nil, "", invalidParameter("file")
	}
	if password && (!seen["password"] || text == "") {
		return file, "", invalidParameter("password")
	}
	if _, seekErr := file.Seek(0, io.SeekStart); seekErr != nil {
		return file, "", &requestError{status: http.StatusServiceUnavailable, code: "http.unavailable"}
	}
	return file, text, nil
}

func (b *transferHandler) exportTasks(w http.ResponseWriter, r *http.Request) {
	if _, err := queryValues(r); err != nil {
		b.failure(w, r, err)
		return
	}
	if err := noBody(r); err != nil {
		b.failure(w, r, err)
		return
	}
	err := stagedDownload(w, r, func(dst io.Writer) (service.ExportInfo, error) {
		return service.ExportInfo{Filename: "kakatta-tasks.json", MediaType: "application/json; charset=utf-8"}, b.service.ExportTasks(r.Context(), dst)
	})
	if err != nil {
		b.failure(w, r, err)
	}
}

func (b *transferHandler) importTasks(w http.ResponseWriter, r *http.Request) {
	if _, err := queryValues(r); err != nil {
		b.failure(w, r, err)
		return
	}
	file, _, err := readUpload(w, r, false)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	defer removeHTTPTemp(file)
	report, err := b.service.ImportTasks(r.Context(), file)
	if report != nil {
		b.reply(w, r, http.StatusOK, report, nil)
		return
	}
	b.reply(w, r, http.StatusOK, report, err)
}

func (b *transferHandler) exportFindings(w http.ResponseWriter, r *http.Request) {
	_, err := queryValues(r, "view", "decision", "price_drop", "over_budget", "q", "sort")
	if err == nil {
		err = noBody(r)
	}
	id, idErr := pathID(r, "task_id")
	if err == nil {
		err = idErr
	}
	var query service.FindingQuery
	if err == nil {
		query, err = resultFindingQuery(r, true)
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	err = stagedDownload(w, r, func(dst io.Writer) (service.ExportInfo, error) {
		return b.service.ExportFindingsCSV(r.Context(), id, query, dst)
	})
	if err != nil {
		b.failure(w, r, err)
	}
}

func (b *transferHandler) exportRuns(w http.ResponseWriter, r *http.Request) {
	_, err := queryValues(r, "task_id", "outcome", "failure_kind", "q", "from_at", "to_at")
	if err == nil {
		err = noBody(r)
	}
	var query service.RunQuery
	if err == nil {
		query, err = resultRunQuery(r)
	}
	var preference string
	if err == nil {
		err = json.Unmarshal(stateOf(r).settings.Values["general.language"], &preference)
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	language := locales.Select(preference, r.Header.Get("Accept-Language"))
	err = stagedDownload(w, r, func(dst io.Writer) (service.ExportInfo, error) {
		return b.service.ExportRunLogs(r.Context(), query, language, dst)
	})
	if err != nil {
		b.failure(w, r, err)
	}
}
