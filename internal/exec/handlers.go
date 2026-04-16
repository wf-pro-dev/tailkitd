package exec

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkit/types"
	"go.uber.org/zap"

	"github.com/wf-pro-dev/tailkitd/internal/helpers"
	"github.com/wf-pro-dev/tailkitd/internal/sse"
)

// Handler holds the dependencies for the exec HTTP endpoints and exposes
// methods that return http.HandlerFunc values for registration on the mux.
type Handler struct {
	registry        *Registry
	jobs            *JobStore
	logger          *zap.Logger
	jobPollInterval time.Duration
}

// NewHandler constructs an exec Handler.
func NewHandler(registry *Registry, jobs *JobStore, logger *zap.Logger) *Handler {
	return &Handler{
		registry:        registry,
		jobs:            jobs,
		logger:          logger.With(zap.String("component", "exec.handler")),
		jobPollInterval: 250 * time.Millisecond,
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
	jobID := h.jobIDFromRequest(r)
	if jobID == "" {
		helpers.WriteError(w, http.StatusBadRequest, "job_id is required", "")
		return
	}
	if r.URL.Query().Get("stream") == "true" {
		h.handleJobStream(w, r, jobID)
		return
	}

	result, ok := h.jobs.GetResult(jobID)
	if !ok {
		helpers.WriteError(w, http.StatusNotFound, "job not found — may have expired (TTL: 5 minutes)", "")
		return
	}

	helpers.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) handleJobStream(w http.ResponseWriter, r *http.Request, jobID string) {
	resume := sse.ResumeFrom(r)
	sse.Handler(15*time.Second, func(ctx context.Context, sw *sse.Writer) error {
		sw.SetSequence(resume)
		return h.streamJob(ctx, sw, jobID, resume)
	})(w, r)
}

func (h *Handler) streamJob(ctx context.Context, sw *sse.Writer, jobID string, resume int64) error {
	ticker := time.NewTicker(h.jobPollInterval)
	defer ticker.Stop()

	lastSent := resume
	for {
		result, ok := h.jobs.GetResult(jobID)
		if !ok {
			return fmt.Errorf("job not found")
		}

		events := buildJobEvents(jobID, result)
		for i, event := range events {
			seq := int64(i + 1)
			if seq <= lastSent {
				continue
			}
			if err := sw.Send(event.name, event.data); err != nil {
				return err
			}
			lastSent = seq
		}
		if isTerminalJobStatus(result.Status) {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (h *Handler) jobIDFromRequest(r *http.Request) string {
	if id := r.PathValue("id"); id != "" {
		return id
	}
	if id := strings.TrimPrefix(r.URL.Path, "/exec/jobs/"); id != "" && id != r.URL.Path {
		return id
	}
	return r.URL.Query().Get("id")
}

type jobStreamEvent struct {
	name string
	data types.JobUpdate
}

func buildJobEvents(jobID string, result types.JobResult) []jobStreamEvent {
	events := []jobStreamEvent{
		{
			name: tailkit.EventJobStatus,
			data: types.JobUpdate{
				Event:  tailkit.EventJobStatus,
				JobID:  jobID,
				Status: types.JobStatusAccepted,
			},
		},
	}
	if result.Status != types.JobStatusAccepted {
		events = append(events, jobStreamEvent{
			name: tailkit.EventJobStatus,
			data: types.JobUpdate{
				Event:  tailkit.EventJobStatus,
				JobID:  jobID,
				Status: result.Status,
			},
		})
	}

	for _, line := range splitLines(result.Stdout) {
		events = append(events, jobStreamEvent{
			name: tailkit.EventJobStdout,
			data: types.JobUpdate{
				Event:  tailkit.EventJobStdout,
				JobID:  jobID,
				Line:   line,
				Stream: "stdout",
			},
		})
	}
	for _, line := range splitLines(result.Stderr) {
		events = append(events, jobStreamEvent{
			name: tailkit.EventJobStderr,
			data: types.JobUpdate{
				Event:  tailkit.EventJobStderr,
				JobID:  jobID,
				Line:   line,
				Stream: "stderr",
			},
		})
	}

	if isTerminalJobStatus(result.Status) {
		payload := types.JobUpdate{
			JobID:    jobID,
			ExitCode: result.ExitCode,
			Error:    result.Error,
		}
		name := tailkit.EventJobCompleted
		if result.Status != types.JobStatusCompleted {
			name = tailkit.EventJobFailed
		}
		payload.Event = name
		events = append(events, jobStreamEvent{name: name, data: payload})
	}
	return events
}

func splitLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func isTerminalJobStatus(status types.JobStatus) bool {
	switch status {
	case types.JobStatusCompleted, types.JobStatusFailed, types.JobStatusCancelled:
		return true
	default:
		return false
	}
}
