package systemd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/wf-pro-dev/tailkit/types"
	TailkitdExec "github.com/wf-pro-dev/tailkitd/internal/exec"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
)

// Handler serves all /integrations/systemd/* endpoints.
type Handler struct {
	client                  *Client
	jobs                    *TailkitdExec.JobStore
	logger                  *zap.Logger
	streamHeartbeatInterval time.Duration
	available               func(context.Context) bool
	followJournal           func(ctx context.Context, unit string, lines int, priority string, fn func(types.JournalEntry) error) error
}

// NewHandler constructs a systemd Handler.
func NewHandler(client *Client, jobs *TailkitdExec.JobStore, logger *zap.Logger) *Handler {
	h := &Handler{
		client:                  client,
		jobs:                    jobs,
		logger:                  logger.With(zap.String("component", "systemd")),
		streamHeartbeatInterval: 15 * time.Second,
	}
	h.available = client.Available
	h.followJournal = h.defaultFollowJournal
	return h
}

// Register mounts all systemd endpoints onto mux.
//
//	GET  /integrations/systemd/available
//	GET  /integrations/systemd/config
//	GET  /integrations/systemd/units
//	GET  /integrations/systemd/units/{unit}
//	GET  /integrations/systemd/units/{unit}/file
//	POST /integrations/systemd/units/{unit}/start
//	POST /integrations/systemd/units/{unit}/stop
//	POST /integrations/systemd/units/{unit}/restart
//	POST /integrations/systemd/units/{unit}/reload
//	POST /integrations/systemd/units/{unit}/enable
//	POST /integrations/systemd/units/{unit}/disable
//	GET  /integrations/systemd/units/{unit}/journal
//	GET  /integrations/systemd/journal
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/integrations/systemd/available", h.handleAvailable)
	mux.HandleFunc("/integrations/systemd/config", h.handleConfig)
	mux.HandleFunc("/integrations/systemd/units", h.handleUnits)
	mux.HandleFunc("/integrations/systemd/units/", h.routeUnit)
	mux.HandleFunc("/integrations/systemd/journal", h.handleSystemJournal)
}

// ─── Guards ───────────────────────────────────────────────────────────────────

func (h *Handler) guard(w http.ResponseWriter) bool {
	if !h.client.cfg.Enabled {
		helpers.WriteError(w, http.StatusServiceUnavailable,
			"systemd integration not configured on this node",
			"create /etc/tailkitd/integrations/systemd.toml to enable")
		return false
	}
	if !h.available(context.Background()) {
		helpers.WriteError(w, http.StatusServiceUnavailable,
			"systemd D-Bus connection unavailable",
			"check that tailkitd has D-Bus access")
		return false
	}
	return true
}

// --- GET /integrations/systemd/config ───────────────────────────────────────
func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request) {
	helpers.WriteJSON(w, http.StatusOK, h.client.cfg)
}

// ─── GET /integrations/systemd/available ─────────────────────────────────────

func (h *Handler) handleAvailable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, map[string]bool{
		"available": h.client.Available(r.Context()),
	})
}

// ─── GET /integrations/systemd/units ─────────────────────────────────────────

func (h *Handler) handleUnits(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w) {
		return
	}
	if r.Method != http.MethodGet {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET")
		return
	}
	if !h.client.cfg.Units.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"units.list not enabled in systemd.toml", "")
		return
	}

	conn, err := h.client.dbusConn()
	if err != nil {
		helpers.WriteError(w, http.StatusServiceUnavailable, err.Error(), "")
		return
	}

	units, err := conn.ListUnitsContext(r.Context())
	if err != nil {
		h.logger.Error("systemd: ListUnits failed", zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError, "failed to list units", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, units)
}

// ─── /integrations/systemd/units/{unit}[/action] ─────────────────────────────

// routeUnit dispatches /integrations/systemd/units/{unit}[/action].
func (h *Handler) routeUnit(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w) {
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, "/integrations/systemd/units/")
	parts := strings.SplitN(rest, "/", 2)
	unit := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	if unit == "" {
		helpers.WriteError(w, http.StatusBadRequest, "unit name is required", "")
		return
	}

	switch action {
	case "":
		h.handleUnitInspect(w, r, unit)
	case "file":
		h.handleUnitFile(w, r, unit)
	case "start":
		h.handleUnitControl(w, r, unit, "start")
	case "stop":
		h.handleUnitControl(w, r, unit, "stop")
	case "restart":
		h.handleUnitControl(w, r, unit, "restart")
	case "reload":
		h.handleUnitControl(w, r, unit, "reload")
	case "enable":
		h.handleUnitEnable(w, r, unit, true)
	case "disable":
		h.handleUnitEnable(w, r, unit, false)
	case "journal":
		h.handleUnitJournal(w, r, unit)
	default:
		helpers.WriteError(w, http.StatusNotFound, "unknown unit action: "+action,
			"valid actions: file, start, stop, restart, reload, enable, disable, journal")
	}
}

// handleUnitInspect serves GET /integrations/systemd/units/{unit}.
// Returns the unit's properties map from D-Bus.
func (h *Handler) handleUnitInspect(w http.ResponseWriter, r *http.Request, unit string) {
	if r.Method != http.MethodGet {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET")
		return
	}
	if !h.client.cfg.Units.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"units.inspect not enabled in systemd.toml", "")
		return
	}

	conn, _ := h.client.dbusConn()
	props, err := conn.GetUnitPropertiesContext(r.Context(), unit)
	if err != nil {
		h.logger.Error("systemd: GetUnitProperties failed",
			zap.String("unit", unit), zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError,
			"failed to get unit properties", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, props)
}

// handleUnitFile serves GET /integrations/systemd/units/{unit}/file.
// Reads the unit file content from disk via the FragmentPath D-Bus property.
func (h *Handler) handleUnitFile(w http.ResponseWriter, r *http.Request, unit string) {
	if r.Method != http.MethodGet {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET")
		return
	}
	if !h.client.cfg.Units.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"units.unit_file not enabled in systemd.toml", "")
		return
	}

	conn, _ := h.client.dbusConn()
	props, err := conn.GetUnitPropertiesContext(r.Context(), unit)
	if err != nil {
		helpers.WriteError(w, http.StatusInternalServerError,
			"failed to get unit properties", "")
		return
	}

	fragPath, ok := props["FragmentPath"]
	if !ok {
		helpers.WriteError(w, http.StatusNotFound,
			"unit has no FragmentPath — may not be a file-backed unit", "")
		return
	}
	path, ok := fragPath.(string)
	if !ok || path == "" {
		helpers.WriteError(w, http.StatusNotFound, "unit file path is empty", "")
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			helpers.WriteError(w, http.StatusNotFound, "unit file not found on disk", "")
			return
		}
		helpers.WriteError(w, http.StatusInternalServerError,
			"failed to read unit file", "")
		return
	}

	helpers.WriteJSON(w, http.StatusOK, map[string]string{
		"unit":    unit,
		"path":    path,
		"content": string(data),
	})
}

// handleUnitControl serves POST /integrations/systemd/units/{unit}/{start|stop|restart|reload}.
//
// All four operations use the D-Bus job channel pattern: pass a buffered channel
// to the control method, which returns a job number immediately. We then wait on
// the channel (with the command's timeout) for the result string ("done", "failed",
// "cancelled", "timeout", "skipped"). This keeps the HTTP response non-blocking
// while still waiting for D-Bus to confirm the transition started.
//
// The result is stored in the job store so callers can poll GET /exec/jobs/{id}.
func (h *Handler) handleUnitControl(w http.ResponseWriter, r *http.Request, unit, action string) {
	if r.Method != http.MethodPost {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use POST")
		return
	}

	// Permission check per action.
	var permitted bool
	switch action {
	case "start":
		permitted = h.client.cfg.Units.Enabled
	case "stop":
		permitted = h.client.cfg.Units.Enabled
	case "restart":
		permitted = h.client.cfg.Units.Enabled
	case "reload":
		permitted = h.client.cfg.Units.Enabled
	}
	if !permitted {
		helpers.WriteError(w, http.StatusForbidden,
			fmt.Sprintf("units.%s not enabled in systemd.toml", action), "")
		return
	}

	conn, _ := h.client.dbusConn()
	jobID := h.jobs.NewJob()

	h.logger.Info("systemd: unit control accepted",
		zap.String("unit", unit),
		zap.String("action", action),
		zap.String("job_id", jobID),
	)

	go func() {
		// D-Bus result channel: systemd writes "done", "failed", etc. when the
		// job completes. Buffer of 1 so the write never blocks.
		ch := make(chan string, 1)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		var dbusErr error
		switch action {
		case "start":
			_, dbusErr = conn.StartUnitContext(ctx, unit, "replace", ch)
		case "stop":
			_, dbusErr = conn.StopUnitContext(ctx, unit, "replace", ch)
		case "restart":
			_, dbusErr = conn.RestartUnitContext(ctx, unit, "replace", ch)
		case "reload":
			_, dbusErr = conn.ReloadUnitContext(ctx, unit, "replace", ch)
		}

		if dbusErr != nil {
			h.logger.Error("systemd: unit control D-Bus call failed",
				zap.String("unit", unit),
				zap.String("action", action),
				zap.Error(dbusErr),
			)
			h.jobs.StoreResult(jobID, types.JobResult{
				JobID:  jobID,
				Status: types.JobStatusFailed,
				Error:  dbusErr.Error(),
			})
			return
		}

		// Wait for D-Bus to report the job result.
		var result string
		select {
		case result = <-ch:
		case <-ctx.Done():
			result = "timeout"
		}

		status := types.JobStatusCompleted
		errMsg := ""
		if result != "done" && result != "skipped" {
			status = types.JobStatusFailed
			errMsg = fmt.Sprintf("systemd job result: %s", result)
			h.logger.Error("systemd: unit control failed",
				zap.String("unit", unit),
				zap.String("action", action),
				zap.String("result", result),
			)
		} else {
			h.logger.Info("systemd: unit control completed",
				zap.String("unit", unit),
				zap.String("action", action),
				zap.String("result", result),
			)
		}

		h.jobs.StoreResult(jobID, types.JobResult{
			JobID:  jobID,
			Status: status,
			Error:  errMsg,
		})
	}()

	helpers.WriteJSON(w, http.StatusAccepted,
		types.Job{JobID: jobID, Status: types.JobStatusAccepted})
}

// handleUnitEnable serves POST /integrations/systemd/units/{unit}/enable|disable.
func (h *Handler) handleUnitEnable(w http.ResponseWriter, r *http.Request, unit string, enable bool) {
	if r.Method != http.MethodPost {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use POST")
		return
	}

	if !h.client.cfg.Units.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"units integration not available in systemd.toml", "")
		return
	}

	if enable && !slices.Contains(h.client.cfg.Units.Allow, "enable") {
		helpers.WriteError(w, http.StatusForbidden,
			"units.enable not allowed in systemd.toml", "")
		return
	}
	if !enable && !slices.Contains(h.client.cfg.Units.Allow, "disable") {
		helpers.WriteError(w, http.StatusForbidden,
			"units.disable not allowed in systemd.toml", "")
		return
	}

	conn, _ := h.client.dbusConn()
	jobID := h.jobs.NewJob()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		var opErr error
		if enable {
			_, _, opErr = conn.EnableUnitFilesContext(
				ctx, []string{unit}, false /* runtime */, true /* force */)
		} else {
			_, opErr = conn.DisableUnitFilesContext(
				ctx, []string{unit}, false /* runtime */)
		}

		if opErr != nil {
			action := "enable"
			if !enable {
				action = "disable"
			}
			h.logger.Error("systemd: unit enable/disable failed",
				zap.String("unit", unit),
				zap.String("action", action),
				zap.Error(opErr),
			)
			h.jobs.StoreResult(jobID, types.JobResult{
				JobID:  jobID,
				Status: types.JobStatusFailed,
				Error:  opErr.Error(),
			})
			return
		}

		// Reload daemon after enable/disable so systemd picks up the change.
		_ = conn.ReloadContext(ctx)

		h.jobs.StoreResult(jobID, types.JobResult{
			JobID:  jobID,
			Status: types.JobStatusCompleted,
		})
	}()

	helpers.WriteJSON(w, http.StatusAccepted,
		types.Job{JobID: jobID, Status: types.JobStatusAccepted})
}

// ─── Journal ──────────────────────────────────────────────────────────────────

// journalctlJSON is the raw shape of a single journalctl --output=json line.
// journalctl emits one JSON object per line (not a JSON array).
// All fields are strings or arrays of strings — we only care about the
// ones that map to JournalEntry. Unknown fields are ignored.
type journalctlJSON struct {
	RealtimeTimestamp string `json:"__REALTIME_TIMESTAMP"` // microseconds since epoch, as string
	Message           string `json:"MESSAGE"`
	SystemdUnit       string `json:"_SYSTEMD_UNIT"`
	Priority          string `json:"PRIORITY"` // syslog integer as string: "0"–"7"
}

// handleUnitJournal serves GET /integrations/systemd/units/{unit}/journal.
func (h *Handler) handleUnitJournal(w http.ResponseWriter, r *http.Request, unit string) {
	if r.Method != http.MethodGet {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET")
		return
	}
	if !h.client.cfg.Journal.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"journal integration not available in systemd.toml", "")
		return
	}
	h.handleUnitJournalFollowOrSnapshot(w, r, unit)
}

// handleSystemJournal serves GET /integrations/systemd/journal.
func (h *Handler) handleSystemJournal(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w) {
		return
	}
	if r.Method != http.MethodGet {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET")
		return
	}
	if !h.client.cfg.Journal.SystemJournal {
		helpers.WriteError(w, http.StatusForbidden,
			"journal.system_journal not enabled in systemd.toml", "")
		return
	}

	lines := parseLines(r, h.client.cfg.Journal.Lines)
	if r.URL.Query().Get("follow") == "true" {
		h.streamJournal(w, r, "", lines, h.client.cfg.Journal.Priority)
		return
	}
	// Empty unit string → no unit filter → system-wide journal.
	entries, err := h.readJournal(r.Context(), "", lines, h.client.cfg.Journal.Priority)
	if err != nil {
		h.logger.Error("systemd: system journal read failed", zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError, "failed to read system journal", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, entries)
}

// readJournal reads journal entries by shelling out to journalctl.
//
// This replaces the previous sdjournal (CGO) implementation with a pure Go
// approach. journalctl is always available on any systemd host, requires no
// C headers, and produces identical output via --output=json.
//
// Behaviour is preserved exactly:
//   - unit != "" → filtered to that unit via --unit
//   - unit == "" → system-wide (no --unit flag)
//   - lines      → --lines cap
//   - priorityFloor → --priority floor (journalctl handles the severity filter natively)
func (h *Handler) readJournal(ctx context.Context, unit string, lines int, priorityFloor string) ([]types.JournalEntry, error) {
	args := []string{
		"--output=json", // one JSON object per line
		"--no-pager",    // never pause waiting for a pager
		"--quiet",       // suppress "-- Boot ID --" header lines
		"--lines", strconv.Itoa(lines),
		"--priority", priorityFloor, // journalctl accepts names like "info", "err", etc.
	}

	if unit != "" {
		args = append(args, "--unit", unit)
	}

	cmd := exec.CommandContext(ctx, "journalctl", args...)

	// Capture stdout; surface stderr only on failure.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.Output()
	if err != nil {
		// Exit code 1 from journalctl means no entries matched — not an error.
		if isNoEntriesExit(err) {
			return []types.JournalEntry{}, nil
		}
		return nil, fmt.Errorf("journalctl: %w: %s", err, stderr.String())
	}

	return parseJournalOutput(stdout), nil
}

// parseJournalOutput parses journalctl --output=json stdout into JournalEntry slice.
// journalctl emits one JSON object per line (newline-delimited JSON, not an array).
// Malformed lines are silently skipped — a single corrupted entry should not
// fail the entire response.
func parseJournalOutput(data []byte) []types.JournalEntry {
	var entries []types.JournalEntry
	scanner := bufio.NewScanner(bytes.NewReader(data))

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var raw journalctlJSON
		if err := json.Unmarshal(line, &raw); err != nil {
			continue // skip malformed lines
		}

		// __REALTIME_TIMESTAMP is a string containing microseconds since epoch.
		var ts uint64
		if raw.RealtimeTimestamp != "" {
			ts, _ = strconv.ParseUint(raw.RealtimeTimestamp, 10, 64)
		}

		// Decode the full fields map so callers get all journal metadata.
		var allFields map[string]string
		_ = json.Unmarshal(line, &allFields)
		// Strip the internal __ prefixed journalctl fields from the fields map
		// so they don't duplicate the structured top-level fields.
		for k := range allFields {
			if strings.HasPrefix(k, "__") {
				delete(allFields, k)
			}
		}

		entries = append(entries, types.JournalEntry{
			Timestamp: ts,
			Message:   raw.Message,
			Unit:      raw.SystemdUnit,
			Priority:  priorityIntToName(raw.Priority),
			Fields:    allFields,
		})
	}

	return entries
}

// isNoEntriesExit returns true when journalctl exits with code 1 because
// no journal entries matched the filter — this is not an error condition.
func isNoEntriesExit(err error) bool {
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode() == 1
	}
	return false
}

// priorityIntToName converts a syslog priority integer string ("0"–"7")
// to its human-readable name. journalctl --output=json stores PRIORITY as
// a string-encoded integer, matching the syslog convention.
func priorityIntToName(s string) string {
	switch s {
	case "0":
		return "emerg"
	case "1":
		return "alert"
	case "2":
		return "crit"
	case "3":
		return "err"
	case "4":
		return "warning"
	case "5":
		return "notice"
	case "6":
		return "info"
	case "7":
		return "debug"
	default:
		return s
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// priorityToInt is kept for any internal callers that still need the int form.
var priorityMap = map[string]int{
	"emerg":   0,
	"alert":   1,
	"crit":    2,
	"err":     3,
	"warning": 4,
	"notice":  5,
	"info":    6,
	"debug":   7,
}

func priorityToInt(name string) int {
	if v, ok := priorityMap[strings.ToLower(name)]; ok {
		return v
	}
	return 6 // default to "info"
}

// parseLines reads ?lines= from the request, falling back to defaultLines.
func parseLines(r *http.Request, defaultLines int) int {
	s := r.URL.Query().Get("lines")
	if s == "" {
		return defaultLines
	}
	v, err := strconv.Atoi(s)
	if err != nil || v <= 0 {
		return defaultLines
	}
	return v
}
