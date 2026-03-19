package vars

import (
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	"go.uber.org/zap"

	tailkit "github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkitd/internal/config"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
)

// Handler serves all /vars/* endpoints.
//
//	GET  /vars                           — list all configured scopes
//	GET  /vars/{project}/{env}           — list all keys in scope (JSON map)
//	GET  /vars/{project}/{env}?format=env — render as KEY=VALUE text
//	GET  /vars/{project}/{env}/{key}     — get a single key
//	PUT  /vars/{project}/{env}/{key}     — set a key (body: plain text value)
//	DELETE /vars/{project}/{env}/{key}   — delete a key
//	DELETE /vars/{project}/{env}         — delete entire scope
type Handler struct {
	cfg    config.VarsConfig
	store  *Store
	logger *zap.Logger
}

// NewHandler constructs a vars Handler.
func NewHandler(cfg config.VarsConfig, store *Store, logger *zap.Logger) *Handler {
	return &Handler{
		cfg:    cfg,
		store:  store,
		logger: logger.With(zap.String("component", "vars")),
	}
}

// Register mounts all vars endpoints onto mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/vars", h.handleScopes)
	mux.HandleFunc("/vars/", h.route)
}

// ─── Routing ──────────────────────────────────────────────────────────────────

// handleScopes serves GET /vars — lists all configured scopes from vars.toml.
func (h *Handler) handleScopes(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.Enabled {
		helpers.WriteError(w, http.StatusServiceUnavailable,
			"vars integration not configured on this node",
			"create /etc/tailkitd/integrations/vars.toml to enable")
		return
	}
	if r.Method != http.MethodGet {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET")
		return
	}

	var visible []config.VarScope
	for _, scope := range h.cfg.Scopes {

		visible = append(visible, config.VarScope{
			Project: scope.Project,
			Env:     scope.Env,
			Allow:   scope.Allow,
		})

	}
	if visible == nil {
		visible = []config.VarScope{}
	}
	helpers.WriteJSON(w, http.StatusOK, visible)
}

// route dispatches /vars/{project}/{env}[/{key}] requests.
func (h *Handler) route(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.Enabled {
		helpers.WriteError(w, http.StatusServiceUnavailable,
			"vars integration not configured on this node",
			"create /etc/tailkitd/integrations/vars.toml to enable")
		return
	}

	// Strip "/vars/" prefix and split.
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/vars/"), "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		helpers.WriteError(w, http.StatusBadRequest,
			"invalid path — expected /vars/{project}/{env}[/{key}]", "")
		return
	}

	project, env := parts[0], parts[1]
	hasKey := len(parts) == 3 && parts[2] != ""

	scope, ok := h.findScope(project, env)
	if !ok {
		helpers.WriteError(w, http.StatusNotFound,
			"scope not configured on this node",
			"add a [[scope]] entry in vars.toml")
		return
	}

	caller, _ := tailkit.CallerFromContext(r.Context())
	if hasKey {
		h.routeKey(w, r, project, env, parts[2], scope, caller)
	} else {
		h.routeScope(w, r, project, env, scope, caller)
	}
}

// routeScope handles /vars/{project}/{env} — list or delete a scope.
func (h *Handler) routeScope(w http.ResponseWriter, r *http.Request,
	project, env string, scope config.VarScope, caller tailkit.CallerIdentity) {

	switch r.Method {
	case http.MethodGet:
		if !slices.Contains(scope.Allow, "read") {
			helpers.WriteError(w, http.StatusForbidden, "read not permitted for this scope", "")
			return
		}
		format := r.URL.Query().Get("format")
		if format == "env" {
			h.handleEnv(w, r, project, env)
		} else {
			h.handleList(w, r, project, env)
		}

	case http.MethodDelete:
		if !slices.Contains(scope.Allow, "write") {
			helpers.WriteError(w, http.StatusForbidden, "write not permitted for this scope", "")
			return
		}
		if err := h.store.DeleteScope(r.Context(), project, env); err != nil {
			h.logger.Error("vars: delete scope failed",
				zap.String("project", project), zap.String("env", env), zap.Error(err))
			helpers.WriteError(w, http.StatusInternalServerError, "failed to delete scope", "")
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET or DELETE")
	}
}

// routeKey handles /vars/{project}/{env}/{key} — get, set, or delete a key.
func (h *Handler) routeKey(w http.ResponseWriter, r *http.Request,
	project, env, key string, scope config.VarScope, caller tailkit.CallerIdentity) {

	switch r.Method {
	case http.MethodGet:
		if !slices.Contains(scope.Allow, "read") {
			helpers.WriteError(w, http.StatusForbidden, "read not permitted for this scope", "")
			return
		}
		h.handleGet(w, r, project, env, key)

	case http.MethodPut:
		if !slices.Contains(scope.Allow, "write") {
			helpers.WriteError(w, http.StatusForbidden, "write not permitted for this scope", "")
			return
		}
		h.handleSet(w, r, project, env, key, caller.Hostname)

	case http.MethodDelete:
		if !slices.Contains(scope.Allow, "write") {
			helpers.WriteError(w, http.StatusForbidden, "write not permitted for this scope", "")
			return
		}
		h.handleDelete(w, r, project, env, key, caller.Hostname)

	default:
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET, PUT, or DELETE")
	}
}

// ─── Endpoint implementations ─────────────────────────────────────────────────

func (h *Handler) handleList(w http.ResponseWriter, r *http.Request, project, env string) {
	vars, err := h.store.List(r.Context(), project, env)
	if err != nil {
		if errors.Is(err, ErrScopeNotFound) {
			// Scope is configured but no file written yet — return empty map.
			helpers.WriteJSON(w, http.StatusOK, map[string]string{})
			return
		}
		h.logger.Error("vars: list failed",
			zap.String("project", project), zap.String("env", env), zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError, "failed to read vars", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, vars)
}

func (h *Handler) handleEnv(w http.ResponseWriter, r *http.Request, project, env string) {
	text, err := h.store.RenderEnv(r.Context(), project, env)
	if err != nil {
		if errors.Is(err, ErrScopeNotFound) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			return
		}
		helpers.WriteError(w, http.StatusInternalServerError, "failed to render env", "")
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(text))
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request, project, env, key string) {
	val, err := h.store.Get(r.Context(), project, env, key)
	if err != nil {
		switch {
		case errors.Is(err, ErrKeyNotFound):
			helpers.WriteError(w, http.StatusNotFound, "key not found", "")
		case errors.Is(err, ErrScopeNotFound):
			helpers.WriteError(w, http.StatusNotFound, "scope not found or empty", "")
		case errors.Is(err, ErrReservedKey):
			helpers.WriteError(w, http.StatusBadRequest, err.Error(), "")
		default:
			helpers.WriteError(w, http.StatusInternalServerError, "failed to get var", "")
		}
		return
	}
	helpers.WriteJSON(w, http.StatusOK, map[string]string{"key": key, "value": val})
}

func (h *Handler) handleSet(w http.ResponseWriter, r *http.Request, project, env, key, caller string) {
	// Read value from request body as plain text.
	body, err := readBody(r, 1<<20) // 1 MiB max
	if err != nil {
		helpers.WriteError(w, http.StatusBadRequest, "failed to read request body: "+err.Error(), "")
		return
	}
	value := strings.TrimRight(string(body), "\n")

	if err := h.store.Set(r.Context(), project, env, key, value, caller); err != nil {
		switch {
		case errors.Is(err, ErrReservedKey):
			helpers.WriteError(w, http.StatusBadRequest, err.Error(), "")
		case errors.Is(err, ErrInvalidKey):
			helpers.WriteError(w, http.StatusBadRequest, err.Error(), "")
		default:
			h.logger.Error("vars: set failed",
				zap.String("project", project), zap.String("env", env),
				zap.String("key", key), zap.Error(err))
			helpers.WriteError(w, http.StatusInternalServerError, "failed to set var", "")
		}
		return
	}
	helpers.WriteJSON(w, http.StatusOK, map[string]string{"key": key, "status": "ok"})
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request, project, env, key, caller string) {
	if err := h.store.Delete(r.Context(), project, env, key, caller); err != nil {
		switch {
		case errors.Is(err, ErrReservedKey):
			helpers.WriteError(w, http.StatusBadRequest, err.Error(), "")
		default:
			h.logger.Error("vars: delete failed",
				zap.String("project", project), zap.String("env", env),
				zap.String("key", key), zap.Error(err))
			helpers.WriteError(w, http.StatusInternalServerError, "failed to delete var", "")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Scope lookup ─────────────────────────────────────────────────────────────

// findScope looks up the VarScope for project/env in the config.
func (h *Handler) findScope(project, env string) (config.VarScope, bool) {
	for _, s := range h.cfg.Scopes {
		if s.Project == project && s.Env == env {
			return s, true
		}
	}
	return config.VarScope{}, false
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func readBody(r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil {
		return []byte{}, nil
	}
	defer r.Body.Close()
	lr := &limitedReader{r: r.Body, limit: limit}
	data, err := io.ReadAll(lr)
	if lr.n >= limit {
		return nil, errors.New("request body too large (max 1 MiB)")
	}
	return data, err
}

type limitedReader struct {
	r     io.Reader
	limit int64
	n     int64
}

func (lr *limitedReader) Read(p []byte) (int, error) {
	if lr.n >= lr.limit {
		return 0, errors.New("body limit exceeded")
	}
	n, err := lr.r.Read(p)
	lr.n += int64(n)
	return n, err
}
