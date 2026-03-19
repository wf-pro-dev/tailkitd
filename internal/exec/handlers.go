package exec

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"go.uber.org/zap"

	tailkit "github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
)

// Handler holds the dependencies for the exec HTTP endpoints and exposes
// methods that return http.HandlerFunc values for registration on the mux.
type Handler struct {
	registry *Registry
	runner   *Runner
	jobs     *JobStore
	logger   *zap.Logger
}

// NewHandler constructs an exec Handler.
func NewHandler(registry *Registry, runner *Runner, jobs *JobStore, logger *zap.Logger) *Handler {
	return &Handler{
		registry: registry,
		runner:   runner,
		jobs:     jobs,
		logger:   logger.With(zap.String("component", "exec.handler")),
	}
}

// Register mounts the exec endpoints onto mux:
//
//	POST /exec/{tool}/{cmd}   — fire-and-forget, returns job_id immediately
//	GET  /exec/jobs/{id}      — poll a job for results
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/exec/", h.route)
}

// route dispatches between the two exec endpoints based on path shape.
//
//	/exec/jobs/{id}       → handleJobPoll
//	/exec/{tool}/{cmd}    → handleExec
func (h *Handler) route(w http.ResponseWriter, r *http.Request) {
	// Strip the leading "/exec/" prefix.
	path := strings.TrimPrefix(r.URL.Path, "/exec/")

	if strings.HasPrefix(path, "jobs/") {
		jobID := strings.TrimPrefix(path, "jobs/")
		if r.Method != http.MethodGet {
			helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "")
			return
		}
		h.handleJobPoll(w, r, jobID)
		return
	}

	// Expect "{tool}/{cmd}".
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		helpers.WriteError(w, http.StatusNotFound, "not found — expected /exec/{tool}/{cmd}", "")
		return
	}
	if r.Method != http.MethodPost {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}
	h.handleExec(w, r, parts[0], parts[1])
}

// handleExec processes POST /exec/{tool}/{cmd}.
//
// Validation order (invariant 6):
//  1. Tool and command exist in registry.
//  2. Caller has the required ACL cap.
//  3. Args parse correctly from the request body.
//  4. Accept job and start goroutine.
func (h *Handler) handleExec(w http.ResponseWriter, r *http.Request, toolName, cmdName string) {
	// 1. Registry lookup — tool/command must exist.
	entry, ok := h.registry.Lookup(toolName, cmdName)
	if !ok {
		h.logger.Warn("exec: tool or command not found",
			zap.String("tool", toolName),
			zap.String("command", cmdName),
		)
		helpers.WriteError(w, http.StatusNotFound,
			"tool or command not found?", "Review the tool and command names, and ensure the tool is installed on this node.")
		return
	}

	// 2. ACL cap check — caller must have the capability declared on the command.
	caller, _ := tailkit.CallerFromContext(r.Context())

	// 3. Parse args from request body (optional — commands may have no args).
	args := make(map[string]string)
	if r.ContentLength != 0 && r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			helpers.WriteError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "")
			return
		}
	}

	// 4. Accept job — create record immediately, run in goroutine.
	jobID := h.jobs.NewJob()

	h.logger.Info("exec accepted",
		zap.String("tool", toolName),
		zap.String("command", cmdName),
		zap.String("job_id", jobID),
		zap.String("caller", caller.Hostname),
	)

	// The goroutine uses its own context derived from Background so it is not
	// tied to the HTTP request lifetime. The job timeout comes from the command
	// declaration. Cancelling the HTTP request does NOT cancel the running job.
	jobCtx, cancel := context.WithTimeout(context.Background(), entry.Command.Timeout)

	go func() {
		defer cancel()
		result := h.runner.Run(jobCtx, entry, args)
		result.JobID = jobID
		h.jobs.StoreResult(jobID, result)

		h.logger.Info("exec completed",
			zap.String("job_id", jobID),
			zap.String("status", string(result.Status)),
			zap.Int("exit_code", result.ExitCode),
			zap.Int64("duration_ms", result.DurationMs),
		)
	}()

	helpers.WriteJSON(w, http.StatusAccepted, tailkit.Job{
		JobID:  jobID,
		Status: tailkit.JobStatusAccepted,
	})
}

// handleJobPoll processes GET /exec/jobs/{id}.
func (h *Handler) handleJobPoll(w http.ResponseWriter, r *http.Request, jobID string) {
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
