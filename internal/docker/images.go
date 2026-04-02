package docker

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/docker/docker/api/types/image"
	"github.com/wf-pro-dev/tailkit/types"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
	"go.uber.org/zap"
)

// ─── Images ───────────────────────────────────────────────────────────────────

// handleImages serves GET /integrations/docker/images.
func (h *Handler) handleImages(w http.ResponseWriter, r *http.Request) {
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
	if !h.cfg.Images.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"images integration not enabled in docker.toml", "")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	images, err := h.client.Docker().ImageList(ctx, image.ListOptions{})
	if err != nil {
		h.logger.Error("docker: image list failed", zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError, "failed to list images", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, images)
}

// routeImage dispatches /integrations/docker/images/{id}/{action}.
func (h *Handler) routeImage(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.Enabled {
		helpers.WriteError(w, http.StatusServiceUnavailable,
			"docker not configured on this node",
			"create /etc/tailkitd/integrations/docker.toml to enable")
		return
	}
	if !h.cfg.Images.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"images integration not enabled in docker.toml", "")
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, "/integrations/docker/images/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	if id == "" {
		helpers.WriteError(w, http.StatusBadRequest, "image id or reference is required", "")
		return
	}

	switch action {
	case "":
		h.handleImageInspect(w, r, id)
	case "pull":
		h.handleImagePull(w, r, id)
	case "remove":
		h.handleImageRemove(w, r, id)
	default:
		helpers.WriteError(w, http.StatusNotFound, "unknown image action: "+action,
			"valid actions: pull, remove")
	}
}

// handleImageInspect serves GET /integrations/docker/images/{id}.
func (h *Handler) handleImageInspect(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	info, _, err := h.client.Docker().ImageInspectWithRaw(ctx, id)
	if err != nil {
		h.logger.Error("docker: image inspect failed",
			zap.String("image", id), zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError, "failed to inspect image", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, info)
}

// handleImagePull serves POST /integrations/docker/images/{ref}/pull.
func (h *Handler) handleImagePull(w http.ResponseWriter, r *http.Request, ref string) {
	if r.Method != http.MethodPost {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use POST")
		return
	}
	if !h.cfg.Images.Permits("pull") {
		helpers.WriteError(w, http.StatusForbidden,
			"images.pull not enabled in docker.toml", "")
		return
	}

	jobID := h.jobs.NewJob()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		result := h.runDockerCLI(ctx, jobID, "pull", ref)
		h.jobs.StoreResult(jobID, result)
		h.logger.Info("docker: image pull completed",
			zap.String("ref", ref),
			zap.String("status", string(result.Status)),
		)
	}()

	helpers.WriteJSON(w, http.StatusAccepted, types.Job{JobID: jobID, Status: types.JobStatusAccepted})
}

// handleImageRemove serves DELETE /integrations/docker/images/{id}/remove.
func (h *Handler) handleImageRemove(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodDelete {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use DELETE")
		return
	}
	if !h.cfg.Images.Permits("remove") {
		helpers.WriteError(w, http.StatusForbidden,
			"images.remove not enabled in docker.toml", "")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	_, err := h.client.Docker().ImageRemove(ctx, id, image.RemoveOptions{Force: false})
	if err != nil {
		h.logger.Error("docker: image remove failed",
			zap.String("image", id), zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError, "failed to remove image", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, map[string]string{"removed": id})
}
