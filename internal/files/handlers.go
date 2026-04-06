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
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"go.uber.org/zap"

	tailkit "github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkit/types"
	Tailkittypes "github.com/wf-pro-dev/tailkit/types/integrations"
	"github.com/wf-pro-dev/tailkitd/internal/exec"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
)

const recvBase = "/var/lib/tailkitd/recv"

// Handler serves GET and POST /files.
type Handler struct {
	cfg    Tailkittypes.FilesConfig
	reg    *exec.Registry
	jobs   *exec.JobStore
	logger *zap.Logger
}

// NewHandler constructs a files Handler. If cfg.Enabled is false the handler
// responds 503 to all requests.
func NewHandler(cfg Tailkittypes.FilesConfig, reg *exec.Registry, jobs *exec.JobStore, logger *zap.Logger) *Handler {
	return &Handler{
		cfg:    cfg,
		reg:    reg,
		jobs:   jobs,
		logger: logger.With(zap.String("component", "files")),
	}
}

// Register mounts the files endpoints onto mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/files/config", h.handleConfig)
	mux.HandleFunc("/files", h.ServeHTTP)
	mux.HandleFunc("/inbox/", h.serveInbox)
}

// ServeHTTP dispatches on HTTP method.
//
//	POST /files                          — write a file to the node
//	GET  /files/config                    — get the files config
//	GET  /files?path=                     — read a single file (JSON wrapper or raw bytes)
//	GET  /files?path=?stat=true          — read a single file and return the file state
//	GET  /files?dir=                      — list a directory
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

// --- GET /files/config ───────────────────────────────────────────────────────
func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request) {
	safePaths := []Tailkittypes.PathRule{}
	for _, rule := range h.cfg.Paths {
		if rule.Share {
			safePaths = append(safePaths, rule)
		}
	}
	safeCfg := h.cfg
	safeCfg.Paths = safePaths
	helpers.WriteJSON(w, http.StatusOK, safeCfg)
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
	tool := r.Header.Get("X-Tool")

	caller, _ := tailkit.CallerFromContext(r.Context())

	// ── Default-inbox path (no explicit destination) ──────────────────────────
	if destPath == "" {
		if tool == "" {
			helpers.WriteError(w, http.StatusBadRequest,
				"X-Dest-Path or X-Tool header is required", "")
			return
		}

		filename := r.Header.Get("X-Filename")
		if filename == "" {
			helpers.WriteError(w, http.StatusBadRequest,
				"X-Filename header is required when X-Tool is used without X-Dest-Path", "")
			return
		}

		toolDir := filepath.Join(recvBase, tool)
		dest := filepath.Join(toolDir, filename)
		destDir := filepath.Dir(dest)

		// Path traversal check — belt-and-suspenders after the regex above.
		if !strings.HasPrefix(filepath.Clean(dest), recvBase) {
			h.logger.Warn("files: path traversal in default inbox write",
				zap.String("tool", tool), zap.String("filename", filename),
				zap.String("caller", caller.Hostname))
			helpers.WriteError(w, http.StatusBadRequest, "path traversal detected", "")
			return
		}

		if err := os.MkdirAll(destDir, 0750); err != nil {
			h.logger.Error("files: mkdir recv tool dir failed",
				zap.String("dir", toolDir), zap.Error(err))
			helpers.WriteError(w, http.StatusInternalServerError,
				"failed to create recv directory", "")
			return
		}

		n, err := atomicWrite(dest, r.Body) // daemon identity, no drop
		if err != nil {
			h.logger.Error("files: default inbox write failed",
				zap.String("dest", dest), zap.String("caller", caller.Hostname), zap.Error(err))
			helpers.WriteError(w, http.StatusInternalServerError, "write failed: "+err.Error(), "")
			return
		}

		h.logger.Info("file received (default inbox)",
			zap.String("dest", dest),
			zap.String("tool", tool),
			zap.String("caller", caller.Hostname),
			zap.Int64("size", n),
		)

		helpers.WriteJSON(w, http.StatusOK, types.SendResult{
			WrittenTo:    dest,
			BytesWritten: n,
		})
		return
	}

	// ── Explicit destination path ─────────────────────────────────────────────
	rule, allowedDir, ok := h.cfg.MatchPathRule(destPath)
	if !ok {
		h.logger.Warn("files: no write rule matches path",
			zap.String("dest_path", destPath))
		helpers.WriteError(w, http.StatusForbidden,
			"no write rule configured for this path",
			"add a matching entry in files.toml")
		return
	}
	if !rule.Permits("write") {
		helpers.WriteError(w, http.StatusForbidden,
			"write permission denied for this path",
			"add write to the allow list in files.toml")
		return
	}

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

	n, err := atomicWriteAs(cleanDest, r.Body, rule.UseAs)
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

	helpers.WriteJSON(w, http.StatusOK, types.SendResult{
		WrittenTo:    cleanDest,
		BytesWritten: n,
	})
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

func (h *Handler) handleReadFile(w http.ResponseWriter, r *http.Request, path string) {

	rule, allowedDir, ok := h.cfg.MatchPathRule(path)
	if !ok {
		helpers.WriteError(w, http.StatusForbidden,
			"no read rule configured for this path",
			"add a matching entry in files.toml")
		return
	}
	if !rule.Permits("read") {
		helpers.WriteError(w, http.StatusForbidden,
			"read permission denied for this path",
			"add read to the allow list in files.toml")
		return
	}

	cleanPath := filepath.Clean(path)
	if !strings.HasPrefix(cleanPath, strings.TrimSuffix(allowedDir, "/")) {
		helpers.WriteError(w, http.StatusBadRequest, "path traversal detected", "")
		return
	}

	data, err := readFile(cleanPath, rule.UseAs)
	if err != nil {
		if os.IsNotExist(err) {
			helpers.WriteError(w, http.StatusNotFound, "file not found", "")
			return
		}
		helpers.WriteError(w, http.StatusInternalServerError, "read failed: "+err.Error(), "")
		return
	}

	if stat := r.URL.Query().Get("stat"); stat == "true" {
		stat, err := readFileStat(data, cleanPath, rule.UseAs)
		if err != nil {
			helpers.WriteError(w, http.StatusInternalServerError, "read failed: "+err.Error(), "")
			return
		}
		helpers.WriteJSON(w, http.StatusOK, stat)
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

func readFileStat(r []byte, path string, id Tailkittypes.ResolvedIdentity) (types.FileStat, error) {

	info, err := statFile(path, id)
	if err != nil {
		return types.FileStat{}, err
	}

	h := sha256.New()
	_, err = io.Copy(h, bytes.NewBuffer(r))
	if err != nil {
		return types.FileStat{}, fmt.Errorf("read content: %w", err)
	}

	digest := hex.EncodeToString(h.Sum(nil))

	return types.FileStat{
		DirEntry: types.DirEntry{
			Name:    info.Name(),
			Size:    info.Size(),
			IsDir:   info.IsDir(),
			ModTime: info.ModTime(),
			Mode:    info.Mode().String(),
		},
		SHA256: digest,
	}, nil
}

func (h *Handler) handleListDir(w http.ResponseWriter, r *http.Request, dir string) {
	rule, allowedDir, ok := h.cfg.MatchPathRule(dir)
	if !ok {
		helpers.WriteError(w, http.StatusForbidden,
			"no read rule configured for this path",
			"add a matching entry in files.toml")
		return
	}

	if !rule.Permits("read") {
		helpers.WriteError(w, http.StatusForbidden,
			"read permission denied for this path",
			"add read to the allow list in files.toml")
		return
	}

	cleanDir := filepath.Clean(dir)
	if !strings.HasPrefix(cleanDir, strings.TrimSuffix(allowedDir, "/")) {
		helpers.WriteError(w, http.StatusBadRequest, "path traversal detected", "")
		return
	}

	entries, err := readDir(cleanDir, rule.UseAs)
	if err != nil {
		if os.IsNotExist(err) {
			helpers.WriteError(w, http.StatusNotFound, "directory not found", "")
			return
		}
		helpers.WriteError(w, http.StatusInternalServerError, "list failed: "+err.Error(), "")
		return
	}

	result := make([]types.DirEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		result = append(result, types.DirEntry{
			Name:    e.Name(),
			Size:    info.Size(),
			IsDir:   e.IsDir(),
			ModTime: info.ModTime(),
			Mode:    info.Mode().String(),
		})
	}
	helpers.WriteJSON(w, http.StatusOK, result)
}

// ─── /inbox/* endpoints ───────────────────────────────────────────────────────
//
// These endpoints operate exclusively on /var/lib/tailkitd/recv/{tool}/.
// No files.toml rule is needed — the recv dir is daemon-owned.
// The files integration must be enabled (files.toml present), but no specific
// path rule for recv/ is required.

// serveInbox dispatches the three inbox endpoints:
//
//	GET    /inbox/{tool}             → list recv dir entries
//	GET    /inbox/{tool}/file?path=  → read a file relative to recv/{tool}
//	DELETE /inbox/{tool}/file?path=  → delete a file relative to recv/{tool}
func (h *Handler) serveInbox(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.Enabled {
		helpers.WriteError(w, http.StatusServiceUnavailable,
			"files integration not configured on this node",
			"create /etc/tailkitd/integrations/files.toml to enable")
		return
	}

	// Parse /inbox/{tool}[/file]
	rest := strings.TrimPrefix(r.URL.Path, "/inbox/")
	// rest is either "{tool}" or "{tool}/file"
	tool, suffix, _ := strings.Cut(rest, "/")

	if tool == "" {
		helpers.WriteError(w, http.StatusBadRequest, "tool name is required", "")
		return
	}

	toolDir := filepath.Join(recvBase, tool) + "/"

	switch {
	case suffix == "" && r.Method == http.MethodGet:
		h.handleInboxList(w, r, toolDir)

	case suffix == "file" && r.Method == http.MethodGet:
		h.handleInboxReadFile(w, r, toolDir)

	case suffix == "file" && r.Method == http.MethodDelete:
		h.handleInboxDelete(w, r, toolDir)

	case suffix == "file":
		helpers.WriteError(w, http.StatusMethodNotAllowed,
			"method not allowed", "use GET or DELETE for /inbox/{tool}/file")

	default:
		helpers.WriteError(w, http.StatusNotFound,
			fmt.Sprintf("unknown inbox sub-path %q", suffix),
			"valid: /inbox/{tool} or /inbox/{tool}/file?path=")
	}
}

// handleInboxList serves GET /inbox/{tool} — lists the recv dir for the tool.
// Returns an empty array when the directory does not exist yet (no files sent).
// Response shape matches GET /files?dir= ([]DirEntry).
func (h *Handler) handleInboxList(w http.ResponseWriter, r *http.Request, toolDir string) {
	entries, err := os.ReadDir(toolDir)
	if err != nil {
		if os.IsNotExist(err) {
			helpers.WriteJSON(w, http.StatusOK, []types.DirEntry{})
			return
		}
		h.logger.Error("inbox: list failed", zap.String("dir", toolDir), zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError, "list failed", "")
		return
	}

	result := make([]types.DirEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		result = append(result, types.DirEntry{
			Name:    e.Name(),
			Size:    info.Size(),
			IsDir:   e.IsDir(),
			ModTime: info.ModTime(),
			Mode:    info.Mode().String(),
		})
	}
	helpers.WriteJSON(w, http.StatusOK, result)
}

// handleInboxReadFile serves GET /inbox/{tool}/file?path=<relative>.
// path is relative to /var/lib/tailkitd/recv/{tool}/.
// Absolute paths and traversal sequences are rejected with 400.
// Response shape matches GET /files?path= (JSON or raw bytes).
func (h *Handler) handleInboxReadFile(w http.ResponseWriter, r *http.Request, toolDir string) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		helpers.WriteError(w, http.StatusBadRequest, "path query parameter is required", "")
		return
	}

	cleanPath, err := resolveInboxPath(toolDir, relPath)
	if err != nil {
		helpers.WriteError(w, http.StatusBadRequest, err.Error(), "")
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

// handleInboxDelete serves DELETE /inbox/{tool}/file?path=<relative>.
// path is relative to /var/lib/tailkitd/recv/{tool}/.
// Returns 204 on success, 404 if not found, 400 on traversal.
func (h *Handler) handleInboxDelete(w http.ResponseWriter, r *http.Request, toolDir string) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		helpers.WriteError(w, http.StatusBadRequest, "path query parameter is required", "")
		return
	}

	cleanPath, err := resolveInboxPath(toolDir, relPath)
	if err != nil {
		helpers.WriteError(w, http.StatusBadRequest, err.Error(), "")
		return
	}

	caller, _ := tailkit.CallerFromContext(r.Context())

	if err := os.Remove(cleanPath); err != nil {
		if os.IsNotExist(err) {
			helpers.WriteError(w, http.StatusNotFound, "file not found", "")
			return
		}
		h.logger.Error("inbox: delete failed",
			zap.String("path", cleanPath),
			zap.String("caller", caller.Hostname),
			zap.Error(err),
		)
		helpers.WriteError(w, http.StatusInternalServerError, "delete failed", "")
		return
	}

	h.logger.Info("inbox: file deleted",
		zap.String("path", cleanPath),
		zap.String("caller", caller.Hostname),
	)
	w.WriteHeader(http.StatusNoContent)
}

// resolveInboxPath resolves a relative path against toolDir and checks it
// stays within that directory. Returns 400-class errors for invalid inputs.
func resolveInboxPath(toolDir, relPath string) (string, error) {
	// Reject absolute paths in the query param.
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("path must be relative, got absolute path %q", relPath)
	}
	clean := filepath.Clean(filepath.Join(toolDir, relPath))
	// Ensure the resolved path is still inside toolDir.
	if !strings.HasPrefix(clean, filepath.Clean(toolDir)+string(filepath.Separator)) &&
		clean != filepath.Clean(toolDir) {
		return "", fmt.Errorf("path traversal detected: %q escapes inbox directory", relPath)
	}
	return clean, nil
}

// ─── Rule matching ────────────────────────────────────────────────────────────

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

// atomicWriteAs performs the write with uid/gid dropped on a locked OS thread.
// The body is read into memory first (on the calling goroutine) so the locked
// thread spends as little time as possible with the dropped identity.
func atomicWriteAs(dest string, r io.Reader, id Tailkittypes.ResolvedIdentity) (int64, error) {
	// Read the body on the calling goroutine — no privilege needed for this.
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, fmt.Errorf("read body for write_as: %w", err)
	}

	type result struct {
		n   int64
		err error
	}
	ch := make(chan result, 1)

	go func() {
		// Pin this goroutine to its OS thread. The thread will not be reused

		runtime.LockOSThread()
		// No defer UnlockOSThread — intentionally let the thread die.

		// Drop to target gid first, then uid. Order matters: once uid is
		// dropped we may not be able to change gid.
		if errno := rawSetgid(id.GID); errno != 0 {
			ch <- result{err: fmt.Errorf("setgid(%d): %w", id.GID, errno)}
			return
		}
		if errno := rawSetuid(id.UID); errno != 0 {
			ch <- result{err: fmt.Errorf("setuid(%d): %w", id.UID, errno)}
			return
		}

		dir := filepath.Dir(dest)

		if err := os.MkdirAll(dir, 0755); err != nil {
			ch <- result{err: fmt.Errorf("create destination directory %s: %w", dest, err)}
			return
		}

		tmp, err := os.CreateTemp(dir, ".tailkitd-recv-*")
		if err != nil {
			ch <- result{err: fmt.Errorf("create temp file in %s: %w", dir, err)}
			return
		}
		tmpName := tmp.Name()
		defer os.Remove(tmpName)

		n, err := tmp.Write(data)
		if err != nil {
			_ = tmp.Close()
			ch <- result{err: fmt.Errorf("write temp file: %w", err)}
			return
		}
		if err := tmp.Chmod(0644); err != nil {
			_ = tmp.Close()
			ch <- result{err: fmt.Errorf("chmod temp file: %w", err)}
			return
		}
		if err := tmp.Close(); err != nil {
			ch <- result{err: fmt.Errorf("close temp file: %w", err)}
			return
		}

		if err := os.Rename(tmpName, dest); err != nil {
			ch <- result{err: fmt.Errorf("rename %s → %s: %w", tmpName, dest, err)}
			return
		}
		ch <- result{n: int64(n)}
	}()

	r2 := <-ch
	return r2.n, r2.err
}

// rawSetuid calls SYS_SETUID on the current OS thread directly.
// Must be called from a goroutine that has called runtime.LockOSThread.
func rawSetuid(uid int) syscall.Errno {
	_, _, errno := syscall.RawSyscall(syscall.SYS_SETUID, uintptr(uid), 0, 0)
	return errno
}

// rawSetgid calls SYS_SETGID on the current OS thread directly.
func rawSetgid(gid int) syscall.Errno {
	_, _, errno := syscall.RawSyscall(syscall.SYS_SETGID, uintptr(gid), 0, 0)
	return errno
}

// ─── Read helpers (with optional identity drop) ───────────────────────────────

// readFile reads the file at path, optionally dropping to id's uid/gid first.
// When id.Set is false the file is read as the daemon user (plain os.ReadFile).
// When id.Set is true the open+read happens on a locked OS thread with the
// identity dropped — matching the write_as semantics used by atomicWriteAs.
func readFile(path string, id Tailkittypes.ResolvedIdentity) ([]byte, error) {
	if id.Set {
		return readFileAs(path, id)
	}
	return os.ReadFile(path)
}

func statFile(path string, id Tailkittypes.ResolvedIdentity) (os.FileInfo, error) {
	if id.Set {
		return statFileAs(path, id)
	}
	return os.Stat(path)
}

func statFileAs(path string, id Tailkittypes.ResolvedIdentity) (os.FileInfo, error) {
	type result struct {
		info os.FileInfo
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		// No defer UnlockOSThread — intentionally let the thread die.

		if errno := rawSetgid(id.GID); errno != 0 {
			ch <- result{err: fmt.Errorf("setgid(%d): %w", id.GID, errno)}
			return
		}
		if errno := rawSetuid(id.UID); errno != 0 {
			ch <- result{err: fmt.Errorf("setuid(%d): %w", id.UID, errno)}
			return
		}

		info, err := os.Stat(path)
		ch <- result{info: info, err: err}
	}()

	r := <-ch
	return r.info, r.err
}

// readDir reads the directory entries at path, optionally dropping to id's
// uid/gid first. When id.Set is false it reads as the daemon user.
func readDir(path string, id Tailkittypes.ResolvedIdentity) ([]os.DirEntry, error) {
	if id.Set {
		return readDirAs(path, id)
	}
	return os.ReadDir(path)
}

// readFileAs opens and reads a file on a goroutine pinned to a locked OS thread
// with gid/uid dropped via RawSyscall. The entire open+read must happen inside
// the locked goroutine — the permission check occurs at open time, so the
// identity must already be dropped before os.ReadFile is called.
func readFileAs(path string, id Tailkittypes.ResolvedIdentity) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)

	go func() {
		runtime.LockOSThread()
		// No defer UnlockOSThread — intentionally let the thread die after
		// mutating per-thread uid/gid state.

		if errno := rawSetgid(id.GID); errno != 0 {
			ch <- result{err: fmt.Errorf("setgid(%d): %w", id.GID, errno)}
			return
		}
		if errno := rawSetuid(id.UID); errno != 0 {
			ch <- result{err: fmt.Errorf("setuid(%d): %w", id.UID, errno)}
			return
		}

		data, err := os.ReadFile(path)
		ch <- result{data: data, err: err}
	}()

	r := <-ch
	return r.data, r.err
}

// readDirAs reads directory entries on a goroutine pinned to a locked OS thread
// with gid/uid dropped via RawSyscall.
func readDirAs(path string, id Tailkittypes.ResolvedIdentity) ([]os.DirEntry, error) {
	type result struct {
		entries []os.DirEntry
		err     error
	}
	ch := make(chan result, 1)

	go func() {
		runtime.LockOSThread()
		// No defer UnlockOSThread — intentionally let the thread die.

		if errno := rawSetgid(id.GID); errno != 0 {
			ch <- result{err: fmt.Errorf("setgid(%d): %w", id.GID, errno)}
			return
		}
		if errno := rawSetuid(id.UID); errno != 0 {
			ch <- result{err: fmt.Errorf("setuid(%d): %w", id.UID, errno)}
			return
		}

		entries, err := os.ReadDir(path)
		ch <- result{entries: entries, err: err}
	}()

	r := <-ch
	return r.entries, r.err
}
