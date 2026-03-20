package docker

import (
	"net/http"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
	"go.uber.org/zap"
)

// handleSwarmNodes serves GET /integrations/docker/swarm/nodes.
func (h *Handler) handleSwarmNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET")
		return
	}
	if !h.cfg.Swarm.Enabled {
		helpers.WriteError(w, http.StatusForbidden, "swarm.read not enabled in docker.toml", "")
		return
	}

	nodes, err := h.client.Docker().NodeList(r.Context(), dockertypes.NodeListOptions{
		Filters: filters.NewArgs(),
	})
	if err != nil {
		h.logger.Error("docker: NodeList failed", zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError, "failed to list swarm nodes", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, nodes)
}

// handleSwarmServices serves GET /integrations/docker/swarm/services.
func (h *Handler) handleSwarmServices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET")
		return
	}
	if !h.cfg.Swarm.Enabled {
		helpers.WriteError(w, http.StatusForbidden, "swarm.read not enabled in docker.toml", "")
		return
	}

	services, err := h.client.Docker().ServiceList(r.Context(), dockertypes.ServiceListOptions{
		Filters: filters.NewArgs(),
	})
	if err != nil {
		h.logger.Error("docker: ServiceList failed", zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError, "failed to list swarm services", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, services)
}
