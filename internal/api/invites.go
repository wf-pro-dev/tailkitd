package api

import (
	"encoding/json"
	"net/http"
	"time"

	tailkit "github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
	"github.com/wf-pro-dev/tailkitd/internal/identity"
	"github.com/wf-pro-dev/tailkitd/internal/invite"
)

type InviteClaimHandler struct {
	Store *invite.Store
}

func (h *InviteClaimHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use POST")
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" {
		helpers.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}

	publicKey, err := identity.LoadPublicKey()
	if err != nil {
		helpers.WriteError(w, http.StatusInternalServerError, "artifact public key not available", "")
		return
	}
	payload, err := invite.ParseAndVerify(req.Token, publicKey)
	if err != nil {
		helpers.WriteError(w, http.StatusUnauthorized, "invite verification failed: "+err.Error(), "")
		return
	}
	if time.Now().After(payload.ExpiresAt) {
		helpers.WriteError(w, http.StatusGone, "invite expired", "")
		return
	}

	caller, ok := tailkit.CallerFromContext(r.Context())
	if !ok || caller.UserLogin == "" {
		helpers.WriteError(w, http.StatusForbidden, "caller identity unavailable", "")
		return
	}
	if payload.Grantee != caller.UserLogin {
		helpers.WriteError(w, http.StatusForbidden, "token not issued to this identity", "")
		return
	}
	if h.Store.IsClaimed(payload.TokenID) {
		helpers.WriteError(w, http.StatusGone, "token already claimed", "")
		return
	}
	if err := h.Store.MarkClaimed(payload.TokenID, caller.UserLogin, payload.ExpiresAt); err != nil {
		helpers.WriteError(w, http.StatusConflict, err.Error(), "")
		return
	}

	helpers.WriteJSON(w, http.StatusOK, map[string]string{
		"status":       "claimed",
		"token_id":     payload.TokenID,
		"service_name": payload.ServiceName,
		"target_node":  payload.TargetNode,
	})
}
