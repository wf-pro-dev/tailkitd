package api

import (
	"encoding/json"
	"net/http"
	"time"

	tailkit "github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
	"github.com/wf-pro-dev/tailkitd/internal/identity"
	"github.com/wf-pro-dev/tailkitd/internal/invite"
	"go.uber.org/zap"
)

type InviteClaimHandler struct {
	Store  *invite.Store
	Logger *zap.Logger
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
		if err != nil {
			h.logger().Warn("invite: invalid claim payload", helpers.WithRequestLogFields(r.Context(), []zap.Field{zap.Error(err)})...)
		} else {
			h.logger().Warn("invite: empty claim token", helpers.WithRequestLogFields(r.Context(), nil)...)
		}
		helpers.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}

	publicKey, err := identity.LoadPublicKey()
	if err != nil {
		h.logger().Error("invite: load public key failed", helpers.WithRequestLogFields(r.Context(), []zap.Field{zap.Error(err)})...)
		helpers.WriteError(w, http.StatusInternalServerError, "artifact public key not available", "")
		return
	}
	payload, err := invite.ParseAndVerify(req.Token, publicKey)
	if err != nil {
		h.logger().Warn("invite: verification failed", helpers.WithRequestLogFields(r.Context(), []zap.Field{zap.Error(err)})...)
		helpers.WriteError(w, http.StatusUnauthorized, "invite verification failed: "+err.Error(), "")
		return
	}
	if time.Now().After(payload.ExpiresAt) {
		h.logger().Warn("invite: token expired", helpers.WithRequestLogFields(r.Context(), []zap.Field{
			zap.String("token_id", payload.TokenID),
			zap.String("service_name", payload.ServiceName),
			zap.String("target_node", payload.TargetNode),
			zap.Time("expires_at", payload.ExpiresAt),
		})...)
		helpers.WriteError(w, http.StatusGone, "invite expired", "")
		return
	}

	caller, ok := tailkit.CallerFromContext(r.Context())
	if !ok || caller.UserLogin == "" {
		h.logger().Warn("invite: caller identity unavailable", helpers.WithRequestLogFields(r.Context(), nil)...)
		helpers.WriteError(w, http.StatusForbidden, "caller identity unavailable", "")
		return
	}
	if payload.Grantee != caller.UserLogin {
		h.logger().Warn("invite: grantee mismatch", helpers.WithRequestLogFields(r.Context(), []zap.Field{
			zap.String("token_id", payload.TokenID),
			zap.String("service_name", payload.ServiceName),
			zap.String("target_node", payload.TargetNode),
			zap.String("grantee", payload.Grantee),
			zap.String("caller", caller.Hostname),
			zap.String("caller_login", caller.UserLogin),
		})...)
		helpers.WriteError(w, http.StatusForbidden, "token not issued to this identity", "")
		return
	}
	if h.Store.IsClaimed(payload.TokenID) {
		h.logger().Warn("invite: token already claimed", helpers.WithRequestLogFields(r.Context(), []zap.Field{
			zap.String("token_id", payload.TokenID),
			zap.String("service_name", payload.ServiceName),
			zap.String("target_node", payload.TargetNode),
			zap.String("caller", caller.Hostname),
			zap.String("caller_login", caller.UserLogin),
		})...)
		helpers.WriteError(w, http.StatusGone, "token already claimed", "")
		return
	}
	if err := h.Store.MarkClaimed(payload.TokenID, caller.UserLogin, payload.ExpiresAt); err != nil {
		h.logger().Warn("invite: claim persist failed", helpers.WithRequestLogFields(r.Context(), []zap.Field{
			zap.String("token_id", payload.TokenID),
			zap.String("service_name", payload.ServiceName),
			zap.String("target_node", payload.TargetNode),
			zap.String("caller", caller.Hostname),
			zap.String("caller_login", caller.UserLogin),
			zap.Error(err),
		})...)
		helpers.WriteError(w, http.StatusConflict, err.Error(), "")
		return
	}
	h.logger().Info("invite: token claimed", helpers.WithRequestLogFields(r.Context(), []zap.Field{
		zap.String("token_id", payload.TokenID),
		zap.String("service_name", payload.ServiceName),
		zap.String("target_node", payload.TargetNode),
		zap.String("caller", caller.Hostname),
		zap.String("caller_login", caller.UserLogin),
		zap.Time("expires_at", payload.ExpiresAt),
	})...)

	helpers.WriteJSON(w, http.StatusOK, map[string]string{
		"status":       "claimed",
		"token_id":     payload.TokenID,
		"service_name": payload.ServiceName,
		"target_node":  payload.TargetNode,
	})
}

func (h *InviteClaimHandler) logger() *zap.Logger {
	if h.Logger != nil {
		return h.Logger.With(zap.String("component", "invite"))
	}
	return zap.NewNop()
}
