package api

import (
	"net/http"

	tailkit "github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
	"github.com/wf-pro-dev/tailkitd/internal/identity"
)

type IdentityHandler struct {
	NodeHostname string
}

func (h *IdentityHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET")
		return
	}

	publicKey, err := identity.ReadPublicKeyString()
	if err != nil {
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
