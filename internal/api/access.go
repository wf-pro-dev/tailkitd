package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/wf-pro-dev/tailkitd/internal/access"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
	"github.com/wf-pro-dev/tailkitd/internal/utils"
)

type AccessHandler struct {
	Registry *access.Registry
	Dir      string
}

func (h *AccessHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		helpers.WriteJSON(w, http.StatusOK, map[string]any{"grants": h.Registry.List()})
	case http.MethodPost:
		var req struct {
			Grants []access.Grant `json:"grants"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			helpers.WriteError(w, http.StatusBadRequest, "invalid request body", "")
			return
		}
		for i := range req.Grants {
			if err := req.Grants[i].Validate(); err != nil {
				helpers.WriteError(w, http.StatusBadRequest, "invalid grant: "+err.Error(), "")
				return
			}
		}
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(map[string]any{"grants": req.Grants}); err != nil {
			helpers.WriteError(w, http.StatusInternalServerError, "failed to encode grants", "")
			return
		}
		path := filepath.Join(h.Dir, "grants.toml")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			helpers.WriteError(w, http.StatusInternalServerError, "failed to create access dir", "")
			return
		}
		if err := utils.AtomicWrite(path, buf.Bytes(), 0o644); err != nil {
			helpers.WriteError(w, http.StatusConflict, "transaction failed: "+err.Error(), "")
			return
		}
		if err := h.Registry.Reload(); err != nil {
			helpers.WriteError(w, http.StatusInternalServerError, "failed to reload access grants", "")
			return
		}
		helpers.WriteJSON(w, http.StatusOK, map[string]string{"status": "success"})
	default:
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET or POST")
	}
}
