package exec

import (
	"net/http"

	"go.uber.org/zap"

	"github.com/wf-pro-dev/tailkitd/internal/helpers"
)

// Handler holds the dependencies for the exec HTTP endpoints and exposes
// methods that return http.HandlerFunc values for registration on the mux.
type Handler struct {
	registry *Registry
	jobs     *JobStore
	logger   *zap.Logger
}

// NewHandler constructs an exec Handler.
func NewHandler(registry *Registry, jobs *JobStore, logger *zap.Logger) *Handler {
	return &Handler{
		registry: registry,
		jobs:     jobs,
		logger:   logger.With(zap.String("component", "exec.handler")),
	}
}

// Register mounts the exec endpoints onto mux:
//
//	POST /exec/{tool}/{cmd}   — fire-and-forget, returns job_id immediately
//	GET  /exec/jobs/{id}      — poll a job for results
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/exec/jobs/{id}", h.handleJobPoll)
}

// handleJobPoll processes GET /exec/jobs/{id}.
func (h *Handler) handleJobPoll(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		helpers.WriteError(w, http.StatusBadRequest, "job_id is required", "")
		return
	}

	result, ok := h.jobs.GetResult(jobID)
	if !ok {
		helpers.WriteError(w, http.StatusNotFound, "job not found — may have expired (TTL: 5 minutes)", "")
		return
	}

	helpers.WriteJSON(w, http.StatusOK, result)
}
