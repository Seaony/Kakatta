package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
)

const (
	diagnosticMaxFiles = 2000
	diagnosticMaxBytes = int64(256 << 20)
	diagnosticMaxFile  = int64(16 << 20)
)

type DiagnosticFile struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

type DiagnosticManifest struct {
	Files      []DiagnosticFile      `json:"files"`
	Excluded   []DiagnosticExclusion `json:"excluded"`
	TotalBytes int64                 `json:"total_bytes"`
}

type DiagnosticExclusion struct {
	Path   string          `json:"path"`
	Reason locales.Message `json:"reason"`
}

type diagnosticHandler struct {
	server  *Server
	dataDir string
}

// RegisterDiagnostics 只导出程序生成的诊断文件，不打包整个数据目录。
func (s *Server) RegisterDiagnostics(dataDir string) error {
	if strings.TrimSpace(dataDir) == "" {
		return errors.New("诊断数据目录不能为空")
	}
	probe := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/api/v1/system/diagnostics"}}
	if _, pattern := s.mux.Handler(probe); pattern == "GET /api/v1/system/diagnostics" {
		return errors.New("诊断导出接口已经注册")
	}
	dir, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	handler := &diagnosticHandler{server: s, dataDir: dir}
	s.RegisterProtected("GET /api/v1/system/diagnostics", http.HandlerFunc(handler.manifest))
	s.RegisterProtected("GET /api/v1/system/diagnostics/download", http.HandlerFunc(handler.download))
	return nil
}

func (h *diagnosticHandler) request(w http.ResponseWriter, r *http.Request) bool {
	if _, err := queryValues(r); err != nil {
		h.server.writeFailure(w, r, err)
		return false
	}
	if err := noBody(r); err != nil {
		h.server.writeFailure(w, r, err)
		return false
	}
	return true
}

func (h *diagnosticHandler) manifest(w http.ResponseWriter, r *http.Request) {
	if !h.request(w, r) {
		return
	}
	root, err := os.OpenRoot(h.dataDir)
	if err != nil {
		h.server.writeFailure(w, r, diagnosticError(false))
		return
	}
	defer root.Close()
	manifest, _, err := collectDiagnostics(r.Context(), root)
	if err != nil {
		h.server.writeFailure(w, r, err)
		return
	}
	WriteJSON(w, r, http.StatusOK, manifest)
}

func (h *diagnosticHandler) download(w http.ResponseWriter, r *http.Request) {
	if !h.request(w, r) {
		return
	}
	err := stagedDownload(w, r, func(dst io.Writer) (service.ExportInfo, error) {
		info := service.ExportInfo{Filename: "kakatta-diagnostics.zip", MediaType: "application/zip"}
		root, err := os.OpenRoot(h.dataDir)
		if err != nil {
			return info, diagnosticError(false)
		}
		defer root.Close()
		manifest, stats, err := collectDiagnostics(r.Context(), root)
		if err != nil {
			return info, err
		}
		archive := zip.NewWriter(dst)
		for i, entry := range manifest.Files {
			if err := r.Context().Err(); err != nil {
				return info, diagnosticError(false)
			}
			file, err := root.Open(entry.Path)
			if err != nil {
				return info, diagnosticError(false)
			}
			stat, err := file.Stat()
			if err != nil || !os.SameFile(stats[i], stat) || !stat.Mode().IsRegular() || stat.Size() > diagnosticMaxFile {
				file.Close()
				return info, diagnosticError(false)
			}
			writer, err := archive.Create(entry.Path)
			if err == nil {
				_, err = io.CopyN(writer, file, entry.Bytes)
			}
			closeErr := file.Close()
			if err != nil || closeErr != nil {
				return info, diagnosticError(false)
			}
		}
		if err := archive.Close(); err != nil {
			return info, diagnosticError(false)
		}
		return info, nil
	})
	if err != nil {
		h.server.writeFailure(w, r, err)
	}
}

func diagnosticError(tooLarge bool) error {
	code := "diagnostics.unavailable"
	if tooLarge {
		code = "diagnostics.too_large"
	}
	return &requestError{status: http.StatusServiceUnavailable, code: code}
}

var (
	diagnosticAccount = regexp.MustCompile(`^[1-9][0-9]*$`)
	diagnosticEvent   = regexp.MustCompile(`^events-[0-9]{4}-[0-9]{2}-[0-9]{2}\.jsonl$`)
	diagnosticBundle  = regexp.MustCompile(`^bundle-[0-9]{8}-[0-9]{6}-[A-Za-z0-9_-]+$`)
	diagnosticSummary = regexp.MustCompile(`^summary-[0-9]{8}-[0-9]{6}-[A-Za-z0-9_-]+$`)
)

func diagnosticPath(name string) (directory, file bool) {
	parts := strings.Split(name, "/")
	switch parts[0] {
	case ".":
		return true, false
	case "diagnostics":
		parts = parts[1:]
		if len(parts) > 0 && parts[0] == "accounts" {
			if len(parts) == 1 {
				return true, false
			}
			if !diagnosticAccount.MatchString(parts[1]) {
				return false, false
			}
			parts = parts[2:]
		} else if len(parts) > 0 && (parts[0] == "service" || parts[0] == "preflight" || parts[0] == "storage") {
			parts = parts[1:]
		}
	case "experiment":
		if len(parts) == 1 {
			return true, false
		}
		if !diagnosticAccount.MatchString(parts[1]) {
			return false, false
		}
		if len(parts) == 2 {
			return true, false
		}
		if parts[2] != "logs" {
			return false, false
		}
		parts = parts[3:]
	default:
		return false, false
	}
	if len(parts) == 0 {
		return true, false
	}
	if len(parts) == 1 {
		name := parts[0]
		if name == "bundles" || name == "summaries" {
			return true, false
		}
		return false, diagnosticEvent.MatchString(name) || name == "chrome.log" || name == "chrome.log.1" || name == "xvfb.log" || name == "xvfb.log.1" || name == "x11vnc.log" || name == "x11vnc.log.1"
	}
	if parts[0] == "bundles" && diagnosticBundle.MatchString(parts[1]) {
		return len(parts) == 2, len(parts) == 3 && (parts[2] == "page.json" || parts[2] == "events.json" || parts[2] == "screenshot.jpg" || parts[2] == "page.html" || parts[2] == "response.json")
	}
	if parts[0] == "summaries" && diagnosticSummary.MatchString(parts[1]) {
		return len(parts) == 2, len(parts) == 3 && parts[2] == "summary.json"
	}
	return false, false
}

func diagnosticScreenshotReason(root *os.Root, name string) string {
	metadata := path.Join(path.Dir(name), "page.json")
	stat, err := root.Lstat(metadata)
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > 64<<10 {
		return "diagnostics.screenshot_unverified"
	}
	file, err := root.Open(metadata)
	if err != nil {
		return "diagnostics.screenshot_unverified"
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(stat, opened) {
		return "diagnostics.screenshot_unverified"
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	var page struct {
		LoginPage *bool `json:"login_page"`
	}
	if err != nil || len(data) > 64<<10 || !utf8.Valid(data) || json.Unmarshal(data, &page) != nil || page.LoginPage == nil {
		return "diagnostics.screenshot_unverified"
	}
	if *page.LoginPage {
		return "diagnostics.login_screenshot_excluded"
	}
	return ""
}

func collectDiagnostics(ctx context.Context, root *os.Root) (DiagnosticManifest, []fs.FileInfo, error) {
	manifest := DiagnosticManifest{Files: []DiagnosticFile{}, Excluded: []DiagnosticExclusion{}}
	var stats []fs.FileInfo
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || ctx.Err() != nil {
			return diagnosticError(false)
		}
		directory, file := diagnosticPath(name)
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if !directory {
				return fs.SkipDir
			}
			return nil
		}
		if !file {
			return nil
		}
		if len(manifest.Files)+len(manifest.Excluded) >= diagnosticMaxFiles {
			return diagnosticError(true)
		}
		if path.Base(name) == "screenshot.jpg" {
			if code := diagnosticScreenshotReason(root, name); code != "" {
				manifest.Excluded = append(manifest.Excluded, DiagnosticExclusion{Path: name, Reason: message(code)})
				return nil
			}
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return diagnosticError(false)
		}
		if info.Size() > diagnosticMaxFile || info.Size() > diagnosticMaxBytes-manifest.TotalBytes {
			return diagnosticError(true)
		}
		manifest.Files = append(manifest.Files, DiagnosticFile{Path: path.Clean(name), Bytes: info.Size()})
		manifest.TotalBytes += info.Size()
		stats = append(stats, info)
		return nil
	})
	return manifest, stats, err
}
