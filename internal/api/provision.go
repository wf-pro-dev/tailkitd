package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	tailkit "github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkitd/internal/access"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
	"github.com/wf-pro-dev/tailkitd/internal/provision"
	"github.com/wf-pro-dev/tailkitd/internal/services"
	"github.com/wf-pro-dev/tailkitd/internal/state"
)

type ProvisionHandler struct {
	AccessRegistry *access.Registry
	Services       *services.Registry
	ServicesDir    string
	Epoch          *state.Epoch
}

func (h *ProvisionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use POST")
		return
	}

	callerEpoch, err := parseCallerEpoch(r.Header.Get("X-State-Epoch"))
	if err != nil {
		helpers.WriteError(w, http.StatusBadRequest, err.Error(), "")
		return
	}
	if h.Epoch != nil {
		if err := h.Epoch.Validate(callerEpoch); err != nil {
			w.Header().Set("X-State-Epoch", strconv.FormatInt(h.Epoch.Current(), 10))
			helpers.WriteError(w, http.StatusConflict, err.Error(), "")
			return
		}
	}

	var req provision.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		helpers.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}

	caller, ok := tailkit.CallerFromContext(r.Context())
	if !ok || caller.UserLogin == "" {
		helpers.WriteError(w, http.StatusForbidden, "caller identity unavailable", "")
		return
	}
	if h.AccessRegistry != nil && h.AccessRegistry.HasAnyGrants() && !h.AccessRegistry.Allow(caller.UserLogin, "service.write", req.ServiceName) {
		helpers.WriteError(w, http.StatusForbidden, "insufficient privileges for this action", "")
		return
	}

	meta, err := provision.Provision(r.Context(), req, h.ServicesDir)
	if err != nil {
		helpers.WriteError(w, http.StatusBadRequest, err.Error(), "")
		return
	}
	if err := h.Services.Reload(); err != nil {
		helpers.WriteError(w, http.StatusInternalServerError, "failed to reload services", "")
		return
	}
	if h.Epoch != nil {
		if nextEpoch, err := h.Epoch.Increment(); err == nil {
			w.Header().Set("X-State-Epoch", strconv.FormatInt(nextEpoch, 10))
		}
	}
	helpers.WriteJSON(w, http.StatusOK, meta)
}
