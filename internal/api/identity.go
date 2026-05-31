package api

import (
	"net/http"

	tailkit "github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
	"github.com/wf-pro-dev/tailkitd/internal/identity"
	"go.uber.org/zap"
)

type IdentityHandler struct {
	NodeHostname string
	Logger       *zap.Logger
}

func (h *IdentityHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET")
		return
	}

	publicKey, err := identity.ReadPublicKeyString()
	if err != nil {
		h.logger().Error("identity: public key read failed", helpers.WithRequestLogFields(r.Context(), []zap.Field{
			zap.Error(err),
		})...)
		helpers.WriteError(w, http.StatusInternalServerError, "artifact public key not available", "")
		return
	}

	var callerIdentity string
	if caller, ok := tailkit.CallerFromContext(r.Context()); ok {
		callerIdentity = caller.UserLogin
	}

	helpers.WriteJSON(w, http.StatusOK, map[string]string{
		"node_hostname":       h.NodeHostname,
		"tailscale_identity":  callerIdentity,
		"artifact_public_key": publicKey,
	})
}

func (h *IdentityHandler) logger() *zap.Logger {
	if h.Logger != nil {
		return h.Logger.With(zap.String("component", "identity"))
	}
	return zap.NewNop()
}
