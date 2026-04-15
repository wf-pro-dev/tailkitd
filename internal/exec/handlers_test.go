package exec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wf-pro-dev/tailkit/types"
	"go.uber.org/zap"
)

func TestHandleJobPollReadsJobIDFromPath(t *testing.T) {
	t.Parallel()

	jobs := NewJobStore(zap.NewNop())
	jobID := jobs.NewJob()
	jobs.StoreResult(jobID, types.JobResult{
		Status:   types.JobStatusCompleted,
		ExitCode: 0,
		Stdout:   "done",
	})

	handler := NewHandler(nil, jobs, zap.NewNop())
	req := httptest.NewRequest(http.MethodGet, "/exec/jobs/"+jobID, nil)
	rec := httptest.NewRecorder()

	handler.handleJobPoll(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var got types.JobResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got.JobID != jobID {
		t.Fatalf("JobID = %q, want %q", got.JobID, jobID)
	}
}

func TestHandleJobPollStreamReplaysEventsAndResumes(t *testing.T) {
	t.Parallel()

	jobs := NewJobStore(zap.NewNop())
	jobID := jobs.NewJob()
	jobs.StoreResult(jobID, types.JobResult{
		Status:   types.JobStatusCompleted,
		ExitCode: 0,
		Stdout:   "line one\nline two\n",
		Stderr:   "warn one\n",
	})

	handler := NewHandler(nil, jobs, zap.NewNop())
	handler.jobPollInterval = 5 * time.Millisecond

	t.Run("full replay", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/exec/jobs/"+jobID+"?stream=true", nil)
		rec := httptest.NewRecorder()

		handler.handleJobPoll(rec, req)

		body := rec.Body.String()
		for _, want := range []string{
			"event: job.status\nid: 1\ndata: {\"job_id\":\"" + jobID + "\",\"status\":\"accepted\"}",
			"event: job.status\nid: 2\ndata: {\"job_id\":\"" + jobID + "\",\"status\":\"completed\"}",
			"event: job.stdout\nid: 3\ndata: {\"job_id\":\"" + jobID + "\",\"line\":\"line one\"}",
			"event: job.stdout\nid: 4\ndata: {\"job_id\":\"" + jobID + "\",\"line\":\"line two\"}",
			"event: job.stderr\nid: 5\ndata: {\"job_id\":\"" + jobID + "\",\"line\":\"warn one\"}",
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("body missing %q:\n%s", want, body)
			}
		}
		for _, want := range []string{
			"event: job.completed\nid: 6",
			"\"job_id\":\"" + jobID + "\"",
			"\"exit_code\":0",
			"\"error\":\"\"",
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("body missing %q:\n%s", want, body)
			}
		}
	})

	t.Run("resume skips delivered events", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/exec/jobs/"+jobID+"?stream=true", nil)
		req.Header.Set("Last-Event-ID", "4")
		rec := httptest.NewRecorder()

		handler.handleJobPoll(rec, req)

		body := rec.Body.String()
		if strings.Contains(body, "event: job.status\nid: 1") || strings.Contains(body, "event: job.stdout\nid: 4") {
			t.Fatalf("body contains replayed earlier ids:\n%s", body)
		}
		for _, want := range []string{
			"event: job.stderr\nid: 5",
			"event: job.completed\nid: 6",
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("body missing %q:\n%s", want, body)
			}
		}
	})
}

func TestHandleJobPollStreamWaitsForCompletion(t *testing.T) {
	t.Parallel()

	jobs := NewJobStore(zap.NewNop())
	jobID := jobs.NewJob()
	handler := NewHandler(nil, jobs, zap.NewNop())
	handler.jobPollInterval = 5 * time.Millisecond

	go func() {
		time.Sleep(10 * time.Millisecond)
		jobs.StoreResult(jobID, types.JobResult{
			Status:   types.JobStatusCompleted,
			ExitCode: 0,
			Stdout:   "done\n",
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/exec/jobs/"+jobID+"?stream=true", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	handler.handleJobPoll(rec, req)

	if !strings.Contains(rec.Body.String(), "event: job.completed") {
		t.Fatalf("body missing completion event:\n%s", rec.Body.String())
	}
}
