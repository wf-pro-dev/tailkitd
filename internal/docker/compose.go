package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/wf-pro-dev/tailkit/types"

	"github.com/wf-pro-dev/tailkitd/internal/config"
	"github.com/wf-pro-dev/tailkitd/internal/exec"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
)

type Handler struct {
	cfg    config.DockerConfig
	jobs   *exec.JobStore
	logger *zap.Logger
	client *Client
}

// ComposeProject is the JSON shape returned by the projects listing.
// We define our own struct rather than depending on docker/compose SDK types
// so that the handler compiles without pulling in the full compose dependency tree.
// The tailkit client SDK uses this same struct on the receive side.
type ComposeProject struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	ConfigFiles string `json:"config_files"`
}

// routeCompose dispatches /integrations/docker/compose/{project}/{action}.
func (h *Handler) routeCompose(w http.ResponseWriter, r *http.Request) {

	if !h.cfg.Compose.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"compose integration not available in docker.toml", "")
		return
	}
	if !slices.Contains(h.cfg.Compose.Allow, "list") {
		helpers.WriteError(w, http.StatusForbidden,
			"compose.list not enabled in docker.toml", "")
		return
	}

	// Path: /integrations/docker/compose/{project}/{action}
	rest := strings.TrimPrefix(r.URL.Path, "/integrations/docker/compose/")
	parts := strings.SplitN(rest, "/", 2)
	project := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	if project == "" {
		helpers.WriteError(w, http.StatusBadRequest, "project name is required", "")
		return
	}

	switch action {
	case "":
		h.handleComposeProject(w, r, project)
	case "up":
		h.handleComposeUp(w, r, project)
	case "down":
		h.handleComposeDown(w, r, project)
	case "pull":
		h.handleComposePull(w, r, project)
	case "restart":
		h.handleComposeRestart(w, r, project)
	case "build":
		h.handleComposeBuild(w, r, project)
	default:
		helpers.WriteError(w, http.StatusNotFound, "unknown compose action: "+action,
			"valid actions: up, down, pull, restart, build")
	}
}

// handleComposeProjects serves GET /integrations/docker/compose/projects.
// Runs `docker compose ls --format json` via the exec runner and returns
// the parsed project list. This approach avoids the heavy docker/compose SDK
// dependency while producing the same output.
func (h *Handler) handleComposeProjects(w http.ResponseWriter, r *http.Request) {

	if r.Method != http.MethodGet {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET")
		return
	}
	if !h.cfg.Compose.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"compose integration not available in docker.toml", "")
		return
	}
	if !slices.Contains(h.cfg.Compose.Allow, "list") {
		helpers.WriteError(w, http.StatusForbidden,
			"compose.list not enabled in docker.toml", "")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	projects, err := h.listComposeProjects(ctx)
	if err != nil {
		h.logger.Error("docker: compose ls failed", zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError, "failed to list compose projects", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, projects)
}

// handleComposeProject serves GET /integrations/docker/compose/{project}.
func (h *Handler) handleComposeProject(w http.ResponseWriter, r *http.Request, project string) {
	if r.Method != http.MethodGet {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	projects, err := h.listComposeProjects(ctx)
	if err != nil {
		helpers.WriteError(w, http.StatusInternalServerError, "failed to list compose projects", "")
		return
	}
	for _, p := range projects {
		if p.Name == project {
			helpers.WriteJSON(w, http.StatusOK, p)
			return
		}
	}
	helpers.WriteError(w, http.StatusNotFound, "compose project not found: "+project, "")
}

// handleComposeUp serves POST /integrations/docker/compose/{project}/up.
func (h *Handler) handleComposeUp(w http.ResponseWriter, r *http.Request, project string) {
	if r.Method != http.MethodPost {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use POST")
		return
	}
	if !h.cfg.Compose.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"compose.up not enabled in docker.toml", "")
		return
	}

	// Optional: compose file path from body or query param.
	composefile := r.URL.Query().Get("file")

	jobID := h.jobs.NewJob()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		args := []string{"compose"}
		if composefile != "" {
			args = append(args, "-f", composefile)
		}
		args = append(args, "-p", project, "up", "-d", "--remove-orphans")

		result := h.runDockerCLI(ctx, jobID, args...)
		h.jobs.StoreResult(jobID, result)
		h.logger.Info("docker: compose up completed",
			zap.String("project", project),
			zap.String("status", string(result.Status)),
		)
	}()

	helpers.WriteJSON(w, http.StatusAccepted, types.Job{JobID: jobID, Status: types.JobStatusAccepted})
}

// handleComposeDown serves POST /integrations/docker/compose/{project}/down.
func (h *Handler) handleComposeDown(w http.ResponseWriter, r *http.Request, project string) {
	if r.Method != http.MethodPost {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use POST")
		return
	}

	if !h.cfg.Compose.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"compose integration not available in docker.toml", "")
		return
	}

	if !slices.Contains(h.cfg.Compose.Allow, "down") {
		helpers.WriteError(w, http.StatusForbidden,
			"compose.down not enabled in docker.toml", "")
		return
	}

	jobID := h.jobs.NewJob()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		result := h.runDockerCLI(ctx, jobID, "compose", "-p", project, "down")
		h.jobs.StoreResult(jobID, result)
		h.logger.Info("docker: compose down completed",
			zap.String("project", project),
			zap.String("status", string(result.Status)),
		)
	}()

	helpers.WriteJSON(w, http.StatusAccepted, types.Job{JobID: jobID, Status: types.JobStatusAccepted})
}

// handleComposePull serves POST /integrations/docker/compose/{project}/pull.
func (h *Handler) handleComposePull(w http.ResponseWriter, r *http.Request, project string) {
	if r.Method != http.MethodPost {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use POST")
		return
	}
	if !h.cfg.Compose.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"compose integration not available in docker.toml", "")
		return
	}
	if !slices.Contains(h.cfg.Compose.Allow, "pull") {
		helpers.WriteError(w, http.StatusForbidden, "compose.pull not enabled in docker.toml", "")
		return
	}

	jobID := h.jobs.NewJob()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		result := h.runDockerCLI(ctx, jobID, "compose", "-p", project, "pull")
		h.jobs.StoreResult(jobID, result)
	}()

	helpers.WriteJSON(w, http.StatusAccepted, types.Job{JobID: jobID, Status: types.JobStatusAccepted})
}

// handleComposeRestart serves POST /integrations/docker/compose/{project}/restart.
func (h *Handler) handleComposeRestart(w http.ResponseWriter, r *http.Request, project string) {
	if r.Method != http.MethodPost {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use POST")
		return
	}
	if !h.cfg.Compose.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"compose integration not available in docker.toml", "")
		return
	}
	if !slices.Contains(h.cfg.Compose.Allow, "restart") {
		helpers.WriteError(w, http.StatusForbidden, "compose.restart not enabled in docker.toml", "")
		return
	}

	jobID := h.jobs.NewJob()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		result := h.runDockerCLI(ctx, jobID, "compose", "-p", project, "restart")
		h.jobs.StoreResult(jobID, result)
	}()

	helpers.WriteJSON(w, http.StatusAccepted, types.Job{JobID: jobID, Status: types.JobStatusAccepted})
}

// handleComposeBuild serves POST /integrations/docker/compose/{project}/build.
func (h *Handler) handleComposeBuild(w http.ResponseWriter, r *http.Request, project string) {
	if r.Method != http.MethodPost {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use POST")
		return
	}
	if !h.cfg.Compose.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"compose integration not available in docker.toml", "")
		return
	}
	if !slices.Contains(h.cfg.Compose.Allow, "build") {
		helpers.WriteError(w, http.StatusForbidden, "compose.build not enabled in docker.toml", "")
		return
	}

	jobID := h.jobs.NewJob()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		result := h.runDockerCLI(ctx, jobID, "compose", "-p", project, "build")
		h.jobs.StoreResult(jobID, result)
	}()

	helpers.WriteJSON(w, http.StatusAccepted, types.Job{JobID: jobID, Status: types.JobStatusAccepted})
}

// ─── Compose helpers ──────────────────────────────────────────────────────────

// listComposeProjects runs `docker compose ls --format json` and parses the output.
func (h *Handler) listComposeProjects(ctx context.Context) ([]ComposeProject, error) {
	out, err := runCommand(ctx, "docker", "compose", "ls", "--format", "json")
	if err != nil {
		return nil, err
	}
	var projects []ComposeProject
	if err := json.Unmarshal([]byte(out), &projects); err != nil {
		// docker compose ls may return an empty string when no projects exist.
		return []ComposeProject{}, nil
	}
	return projects, nil
}

// runDockerCLI runs a `docker` subcommand as an exec.JobResult.
// Used for compose operations where the SDK dependency is too heavy.
func (h *Handler) runDockerCLI(ctx context.Context, jobID string, args ...string) types.JobResult {
	out, err := runCommand(ctx, "docker", args...)
	if err != nil {
		return types.JobResult{
			JobID:  jobID,
			Status: types.JobStatusFailed,
			Error:  err.Error(),
			Stderr: out,
		}
	}
	return types.JobResult{
		JobID:  jobID,
		Status: types.JobStatusCompleted,
		Stdout: out,
	}
}
