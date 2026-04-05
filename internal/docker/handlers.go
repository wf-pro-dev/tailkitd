package docker

import (
	"context"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/wf-pro-dev/tailkitd/internal/config"
	"github.com/wf-pro-dev/tailkitd/internal/exec"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
)

// NewHandler constructs a Handler for the docker integration.
// cfg is loaded from docker.toml at startup; client is the shared Docker SDK
// wrapper; jobs is the daemon-wide in-memory job store.
func NewHandler(cfg config.DockerConfig, client *Client, jobs *exec.JobStore, logger *zap.Logger) *Handler {
	return &Handler{
		cfg:    cfg,
		client: client,
		jobs:   jobs,
		logger: logger,
	}
}

// Register wires all /integrations/docker/* routes onto mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/integrations/docker/available", h.handleAvailable)
	mux.HandleFunc("/integrations/docker/config", h.handleConfig)

	// Containers
	mux.HandleFunc("/integrations/docker/containers", h.handleContainers)
	mux.HandleFunc("/integrations/docker/containers/", h.routeContainer)

	// Images
	mux.HandleFunc("/integrations/docker/images", h.handleImages)
	mux.HandleFunc("/integrations/docker/images/", h.routeImage)

	mux.HandleFunc("/integrations/docker/compose/projects", h.handleComposeProjects)
	mux.HandleFunc("/integrations/docker/compose/", h.routeCompose)

	// Swarm

	mux.HandleFunc("/integrations/docker/swarm/nodes", h.handleSwarmNodes)
	mux.HandleFunc("/integrations/docker/swarm/services", h.handleSwarmServices)

}

// ─── Available ────────────────────────────────────────────────────────────────

// handleAvailable serves GET /integrations/docker/available.
// Returns 200 if the docker integration is configured and the daemon is
// reachable, 503 otherwise.
func (h *Handler) handleAvailable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET")
		return
	}
	if !h.cfg.Enabled {
		helpers.WriteError(w, http.StatusServiceUnavailable,
			"docker not configured on this node",
			"create /etc/tailkitd/integrations/docker.toml to enable")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if _, err := h.client.Docker().Ping(ctx); err != nil {
		h.logger.Warn("docker socket unavailable on availability check", zap.Error(err))
		helpers.WriteError(w, http.StatusServiceUnavailable,
			"docker daemon unreachable", err.Error())
		return
	}

	helpers.WriteJSON(w, http.StatusOK, map[string]bool{"available": true})
}

// --- GET /integrations/docker/config ───────────────────────────────────────
func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request) {
	helpers.WriteJSON(w, http.StatusOK, h.cfg)
}
