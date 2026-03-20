package docker

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	tailkit "github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
	"go.uber.org/zap"
)

// ─── Containers ───────────────────────────────────────────────────────────────

// handleContainers serves GET /integrations/docker/containers.
// Returns all containers (running + stopped) when the containers permission is enabled.
func (h *Handler) handleContainers(w http.ResponseWriter, r *http.Request) {
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
	if !h.cfg.Containers.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"containers integration not enabled in docker.toml", "")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	containers, err := h.client.Docker().ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		h.logger.Error("docker: container list failed", zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError, "failed to list containers", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, containers)
}

// routeContainer dispatches /integrations/docker/containers/{id}/{action}.
func (h *Handler) routeContainer(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.Enabled {
		helpers.WriteError(w, http.StatusServiceUnavailable,
			"docker not configured on this node",
			"create /etc/tailkitd/integrations/docker.toml to enable")
		return
	}
	if !h.cfg.Containers.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"containers integration not enabled in docker.toml", "")
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, "/integrations/docker/containers/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	if id == "" {
		helpers.WriteError(w, http.StatusBadRequest, "container id is required", "")
		return
	}

	switch action {
	case "":
		h.handleContainerInspect(w, r, id)
	case "start":
		h.handleContainerStart(w, r, id)
	case "stop":
		h.handleContainerStop(w, r, id)
	case "restart":
		h.handleContainerRestart(w, r, id)
	case "logs":
		h.handleContainerLogs(w, r, id)
	default:
		helpers.WriteError(w, http.StatusNotFound, "unknown container action: "+action,
			"valid actions: start, stop, restart, logs")
	}
}

// handleContainerInspect serves GET /integrations/docker/containers/{id}.
func (h *Handler) handleContainerInspect(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	info, err := h.client.Docker().ContainerInspect(ctx, id)
	if err != nil {
		h.logger.Error("docker: container inspect failed",
			zap.String("container", id), zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError, "failed to inspect container", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, info)
}

// handleContainerStart serves POST /integrations/docker/containers/{id}/start.
func (h *Handler) handleContainerStart(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use POST")
		return
	}
	if !h.cfg.Containers.Permits("start") {
		helpers.WriteError(w, http.StatusForbidden,
			"containers.write not enabled in docker.toml", "")
		return
	}

	jobID := h.jobs.NewJob()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		err := h.client.Docker().ContainerStart(ctx, id, container.StartOptions{})
		if err != nil {
			result := tailkit.JobResult{
				JobID:  jobID,
				Status: tailkit.JobStatusFailed,
				Error:  err.Error(),
			}
			h.jobs.StoreResult(jobID, result)
			h.logger.Error("docker: container start failed",
				zap.String("container", id), zap.Error(err))
			return
		}
		h.jobs.StoreResult(jobID, tailkit.JobResult{
			JobID:  jobID,
			Status: tailkit.JobStatusCompleted,
		})
		h.logger.Info("docker: container start completed", zap.String("container", id))
	}()

	helpers.WriteJSON(w, http.StatusAccepted, tailkit.Job{JobID: jobID, Status: tailkit.JobStatusAccepted})
}

// handleContainerStop serves POST /integrations/docker/containers/{id}/stop.
func (h *Handler) handleContainerStop(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use POST")
		return
	}
	if !h.cfg.Containers.Permits("stop") {
		helpers.WriteError(w, http.StatusForbidden,
			"containers.write not enabled in docker.toml", "")
		return
	}

	jobID := h.jobs.NewJob()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		timeout := 30 // seconds
		err := h.client.Docker().ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
		if err != nil {
			result := tailkit.JobResult{
				JobID:  jobID,
				Status: tailkit.JobStatusFailed,
				Error:  err.Error(),
			}
			h.jobs.StoreResult(jobID, result)
			h.logger.Error("docker: container stop failed",
				zap.String("container", id), zap.Error(err))
			return
		}
		h.jobs.StoreResult(jobID, tailkit.JobResult{
			JobID:  jobID,
			Status: tailkit.JobStatusCompleted,
		})
		h.logger.Info("docker: container stop completed", zap.String("container", id))
	}()

	helpers.WriteJSON(w, http.StatusAccepted, tailkit.Job{JobID: jobID, Status: tailkit.JobStatusAccepted})
}

// handleContainerRestart serves POST /integrations/docker/containers/{id}/restart.
func (h *Handler) handleContainerRestart(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use POST")
		return
	}
	if !h.cfg.Containers.Permits("restart") {
		helpers.WriteError(w, http.StatusForbidden,
			"containers.write not enabled in docker.toml", "")
		return
	}

	jobID := h.jobs.NewJob()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		timeout := 30
		err := h.client.Docker().ContainerRestart(ctx, id, container.StopOptions{Timeout: &timeout})
		if err != nil {
			result := tailkit.JobResult{
				JobID:  jobID,
				Status: tailkit.JobStatusFailed,
				Error:  err.Error(),
			}
			h.jobs.StoreResult(jobID, result)
			h.logger.Error("docker: container restart failed",
				zap.String("container", id), zap.Error(err))
			return
		}
		h.jobs.StoreResult(jobID, tailkit.JobResult{
			JobID:  jobID,
			Status: tailkit.JobStatusCompleted,
		})
		h.logger.Info("docker: container restart completed", zap.String("container", id))
	}()

	helpers.WriteJSON(w, http.StatusAccepted, tailkit.Job{JobID: jobID, Status: tailkit.JobStatusAccepted})
}

// handleContainerLogs serves GET /integrations/docker/containers/{id}/logs.
// Query params: tail (default "100"), timestamps (default "false").
func (h *Handler) handleContainerLogs(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET")
		return
	}
	if !h.cfg.Containers.Permits("logs") {
		helpers.WriteError(w, http.StatusForbidden,
			"containers.logs not enabled in docker.toml", "")
		return
	}

	tail := r.URL.Query().Get("tail")
	if tail == "" {
		tail = "100"
	}
	showTimestamps := r.URL.Query().Get("timestamps") == "true"

	args := []string{"logs", "--tail", tail}
	if showTimestamps {
		args = append(args, "--timestamps")
	}
	args = append(args, id)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	out, err := runCommand(ctx, "docker", args...)
	if err != nil {
		h.logger.Error("docker: container logs failed",
			zap.String("container", id), zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError, "failed to fetch container logs", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, map[string]string{"logs": out})
}
