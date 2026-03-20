// Package files implements the tailkitd files integration:
//
//	POST /files        — receive a file written atomically to the node
//	GET  /files?path=  — read a single file (JSON wrapper or raw bytes)
//	GET  /files?dir=   — list a directory
//
// Both reads and writes are gated by files.toml configuration. Path traversal
// is checked on every operation before any filesystem I/O.
package files

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"

	tailkit "github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkitd/internal/config"
	"github.com/wf-pro-dev/tailkitd/internal/exec"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
)

// Handler serves GET and POST /files.
type Handler struct {
	cfg    config.FilesConfig
	reg    *exec.Registry
	jobs   *exec.JobStore
	logger *zap.Logger
}

// NewHandler constructs a files Handler. If cfg.Enabled is false the handler
// responds 503 to all requests.
func NewHandler(cfg config.FilesConfig, reg *exec.Registry, jobs *exec.JobStore, logger *zap.Logger) *Handler {
	return &Handler{
		cfg:    cfg,
		reg:    reg,
		jobs:   jobs,
		logger: logger.With(zap.String("component", "files")),
	}
}

// Register mounts the files endpoints onto mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/files", h.ServeHTTP)
}

// ServeHTTP dispatches on HTTP method.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.Enabled {
		helpers.WriteError(w, http.StatusServiceUnavailable,
			"files integration not configured on this node",
			"create /etc/tailkitd/integrations/files.toml to enable")
		return
	}
	switch r.Method {
	case http.MethodPost:
		h.handleWrite(w, r)
	case http.MethodGet:
		h.handleRead(w, r)
	default:
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET or POST")
	}
}

// ─── POST /files ──────────────────────────────────────────────────────────────

// handleWrite receives a file from the request body and writes it atomically
// to the path given in the X-Dest-Path header.
//
// Request:
//
//	POST /files
//	X-Dest-Path: /etc/nginx/conf.d/api.conf
//	Content-Type: application/octet-stream
//	<body>
//
// Response (200):
//
//	{"written_to":"/etc/nginx/conf.d/api.conf","bytes_written":1234}
//	{"written_to":"...","bytes_written":1234,"job_id":"<uuid>"}  ← if post_recv hook fired
func (h *Handler) handleWrite(w http.ResponseWriter, r *http.Request) {
	destPath := r.Header.Get("X-Dest-Path")
	if destPath == "" {
		helpers.WriteError(w, http.StatusBadRequest, "X-Dest-Path header is required", "")
		return
	}

	// 1. Find a matching write rule.
	rule, allowedDir, ok := h.matchWriteRule(destPath)
	if !ok {
		h.logger.Warn("files: no write rule matches path", zap.String("dest_path", destPath))
		helpers.WriteError(w, http.StatusForbidden,
			"no write rule configured for this path",
			"add a matching [write] entry in files.toml")
		return
	}

	if !rule.Permits("write") {
		helpers.WriteError(w, http.StatusForbidden,
			"write permission denied for this path",
			"add a matching [write] entry in files.toml")
		return
	}

	caller, _ := tailkit.CallerFromContext(r.Context())

	// 3. Path traversal check — before any I/O.
	cleanDest := filepath.Clean(destPath)
	if !strings.HasPrefix(cleanDest, strings.TrimSuffix(allowedDir, "/")) {
		h.logger.Warn("files: path traversal attempt",
			zap.String("dest_path", destPath),
			zap.String("clean_path", cleanDest),
			zap.String("allowed_dir", allowedDir),
			zap.String("caller", caller.Hostname),
		)
		helpers.WriteError(w, http.StatusBadRequest, "path traversal detected", "")
		return
	}

	// 4. Ensure destination directory exists.
	if err := os.MkdirAll(filepath.Dir(cleanDest), 0755); err != nil {
		h.logger.Error("files: mkdir failed", zap.String("path", cleanDest), zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError, "failed to create destination directory", "")
		return
	}

	// 5. Atomic write.
	n, err := atomicWrite(cleanDest, r.Body)
	if err != nil {
		h.logger.Error("files: write failed",
			zap.String("dest", cleanDest),
			zap.String("caller", caller.Hostname),
			zap.Error(err),
		)
		helpers.WriteError(w, http.StatusInternalServerError, "write failed: "+err.Error(), "")
		return
	}

	h.logger.Info("file received",
		zap.String("dest", cleanDest),
		zap.String("caller", caller.Hostname),
		zap.Int64("size", n),
	)

	result := tailkit.SendResult{
		WrittenTo:    cleanDest,
		BytesWritten: n,
	}

	helpers.WriteJSON(w, http.StatusOK, result)
}

// ─── GET /files ───────────────────────────────────────────────────────────────

func (h *Handler) handleRead(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if path := q.Get("path"); path != "" {
		h.handleReadFile(w, r, path)
		return
	}
	if dir := q.Get("dir"); dir != "" {
		h.handleListDir(w, r, dir)
		return
	}
	helpers.WriteError(w, http.StatusBadRequest,
		"missing query parameter",
		"use ?path=/absolute/file or ?dir=/absolute/dir/")
}

// handleReadFile serves GET /files?path=<absolute-path>.
// Responds with JSON {"content":"..."} by default, or raw bytes if
// Accept: application/octet-stream is set.
func (h *Handler) handleReadFile(w http.ResponseWriter, r *http.Request, path string) {
	_, allowedDir, ok := h.matchReadRule(path)
	if !ok {
		helpers.WriteError(w, http.StatusForbidden,
			"no read rule configured for this path",
			"add a matching [read] entry in files.toml")
		return
	}

	cleanPath := filepath.Clean(path)
	if !strings.HasPrefix(cleanPath, strings.TrimSuffix(allowedDir, "/")) {
		helpers.WriteError(w, http.StatusBadRequest, "path traversal detected", "")
		return
	}

	data, err := os.ReadFile(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			helpers.WriteError(w, http.StatusNotFound, "file not found", "")
			return
		}
		helpers.WriteError(w, http.StatusInternalServerError, "read failed: "+err.Error(), "")
		return
	}

	if r.Header.Get("Accept") == "application/octet-stream" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		return
	}

	helpers.WriteJSON(w, http.StatusOK, map[string]string{"content": string(data)})
}

// DirEntry is one entry in a directory listing response.
type DirEntry struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	IsDir   bool      `json:"is_dir"`
	ModTime time.Time `json:"mod_time"`
	Mode    string    `json:"mode"`
}

// handleListDir serves GET /files?dir=<absolute-path>.
func (h *Handler) handleListDir(w http.ResponseWriter, r *http.Request, dir string) {
	_, allowedDir, ok := h.matchReadRule(dir)
	if !ok {
		helpers.WriteError(w, http.StatusForbidden,
			"no read rule configured for this path",
			"add a matching [read] entry in files.toml")
		return
	}

	cleanDir := filepath.Clean(dir)
	if !strings.HasPrefix(cleanDir, strings.TrimSuffix(allowedDir, "/")) {
		helpers.WriteError(w, http.StatusBadRequest, "path traversal detected", "")
		return
	}

	entries, err := os.ReadDir(cleanDir)
	if err != nil {
		if os.IsNotExist(err) {
			helpers.WriteError(w, http.StatusNotFound, "directory not found", "")
			return
		}
		helpers.WriteError(w, http.StatusInternalServerError, "list failed: "+err.Error(), "")
		return
	}

	result := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		result = append(result, DirEntry{
			Name:    e.Name(),
			Size:    info.Size(),
			IsDir:   e.IsDir(),
			ModTime: info.ModTime(),
			Mode:    info.Mode().String(),
		})
	}
	helpers.WriteJSON(w, http.StatusOK, result)
}

// ─── Rule matching ────────────────────────────────────────────────────────────

// matchWriteRule finds the longest-prefix write rule covering destPath.
func (h *Handler) matchWriteRule(destPath string) (config.PathRule, string, bool) {
	best := ""
	var bestRule config.PathRule
	for _, rule := range h.cfg.Paths {
		dir := rule.Dir
		if strings.HasPrefix(destPath, dir) && len(dir) > len(best) {
			best = dir
			bestRule = rule
		}
	}
	if best == "" {
		return config.PathRule{}, "", false
	}
	return bestRule, best, true
}

// matchReadRule finds the longest-prefix read rule covering path.
func (h *Handler) matchReadRule(path string) (config.PathRule, string, bool) {
	// For a file path, check its parent directory against the read rules.
	checkPath := path
	if !strings.HasSuffix(checkPath, "/") {
		checkPath = filepath.Dir(checkPath) + "/"
	}
	best := ""
	var bestRule config.PathRule
	for _, rule := range h.cfg.Paths {
		dir := rule.Dir
		if strings.HasPrefix(checkPath, dir) && len(dir) > len(best) {
			best = dir
			bestRule = rule
		}
	}
	if best == "" {
		return config.PathRule{}, "", false
	}
	return bestRule, best, true
}

// ─── Atomic write ─────────────────────────────────────────────────────────────

// atomicWrite reads from r and writes to dest using temp-then-rename.
// The temp file is created in the same directory as dest to guarantee
// the rename is on the same filesystem (invariant 2).
func atomicWrite(dest string, r io.Reader) (int64, error) {
	dir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(dir, ".tailkitd-recv-*")
	if err != nil {
		return 0, fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename

	n, err := io.Copy(tmp, r)
	if err != nil {
		_ = tmp.Close()
		return 0, fmt.Errorf("write to temp file: %w", err)
	}
	if err := tmp.Chmod(0644); err != nil {
		_ = tmp.Close()
		return 0, fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return 0, fmt.Errorf("rename %s → %s: %w", tmpName, dest, err)
	}
	return n, nil
}
