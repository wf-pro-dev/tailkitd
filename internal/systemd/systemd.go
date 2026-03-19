package systemd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-systemd/v22/sdjournal"
	"go.uber.org/zap"

	tailkit "github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkitd/internal/exec"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
)

// Handler serves all /integrations/systemd/* endpoints.
type Handler struct {
	client *Client
	jobs   *exec.JobStore
	logger *zap.Logger
}

// NewHandler constructs a systemd Handler.
func NewHandler(client *Client, jobs *exec.JobStore, logger *zap.Logger) *Handler {
	return &Handler{
		client: client,
		jobs:   jobs,
		logger: logger.With(zap.String("component", "systemd")),
	}
}

// Register mounts all systemd endpoints onto mux.
//
//	GET  /integrations/systemd/available
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
	if !h.client.Available(context.Background()) {
		helpers.WriteError(w, http.StatusServiceUnavailable,
			"systemd D-Bus connection unavailable",
			"check that tailkitd has D-Bus access")
		return false
	}
	return true
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
			h.jobs.StoreResult(jobID, tailkit.JobResult{
				JobID:  jobID,
				Status: tailkit.JobStatusFailed,
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

		status := tailkit.JobStatusCompleted
		errMsg := ""
		if result != "done" && result != "skipped" {
			status = tailkit.JobStatusFailed
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

		h.jobs.StoreResult(jobID, tailkit.JobResult{
			JobID:  jobID,
			Status: status,
			Error:  errMsg,
		})
	}()

	helpers.WriteJSON(w, http.StatusAccepted,
		tailkit.Job{JobID: jobID, Status: tailkit.JobStatusAccepted})
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
			h.jobs.StoreResult(jobID, tailkit.JobResult{
				JobID:  jobID,
				Status: tailkit.JobStatusFailed,
				Error:  opErr.Error(),
			})
			return
		}

		// Reload daemon after enable/disable so systemd picks up the change.
		_ = conn.ReloadContext(ctx)

		h.jobs.StoreResult(jobID, tailkit.JobResult{
			JobID:  jobID,
			Status: tailkit.JobStatusCompleted,
		})
	}()

	helpers.WriteJSON(w, http.StatusAccepted,
		tailkit.Job{JobID: jobID, Status: tailkit.JobStatusAccepted})
}

// ─── Journal ──────────────────────────────────────────────────────────────────

// JournalEntry is the shape returned in journal responses.
// We define our own struct rather than exposing sdjournal.JournalEntry directly
// so the API surface is stable regardless of sdjournal internals.
type JournalEntry struct {
	Timestamp uint64            `json:"timestamp_us"` // realtime timestamp in microseconds
	Message   string            `json:"message"`
	Unit      string            `json:"unit,omitempty"`
	Priority  string            `json:"priority,omitempty"`
	Fields    map[string]string `json:"fields,omitempty"`
}

// handleUnitJournal serves GET /integrations/systemd/units/{unit}/journal.
// Returns up to ?lines= entries (default from systemd.toml) for the named unit,
// filtered to the configured minimum priority floor.
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

	lines := parseLines(r, h.client.cfg.Journal.Lines)
	entries, err := h.readJournal(r.Context(), unit, lines,
		h.client.cfg.Journal.Priority)
	if err != nil {
		h.logger.Error("systemd: journal read failed",
			zap.String("unit", unit), zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError,
			"failed to read journal", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, entries)
}

// handleSystemJournal serves GET /integrations/systemd/journal.
// Returns up to ?lines= entries from the system journal (all units).
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
	// Empty unit string → no unit filter → system-wide journal.
	entries, err := h.readJournal(r.Context(), "", lines,
		h.client.cfg.Journal.Priority)
	if err != nil {
		h.logger.Error("systemd: system journal read failed", zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError,
			"failed to read system journal", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, entries)
}

// readJournal opens the system journal, optionally filters by unit, applies the
// priority floor, seeks to the tail, and collects up to `lines` entries going
// backward. sdjournal requires cgo — this file is only compiled when cgo is
// available (standard on Linux with libsystemd-dev installed).
func (h *Handler) readJournal(ctx context.Context, unit string, lines int, priorityFloor string) ([]JournalEntry, error) {
	j, err := sdjournal.NewJournal()
	if err != nil {
		return nil, fmt.Errorf("open journal: %w", err)
	}
	defer j.Close()

	// Filter by unit if specified.
	if unit != "" {
		if err := j.AddMatch(
			sdjournal.SD_JOURNAL_FIELD_SYSTEMD_UNIT + "=" + unit,
		); err != nil {
			return nil, fmt.Errorf("add unit match: %w", err)
		}
	}

	// Seek to tail, then step back `lines` entries.
	if err := j.SeekTail(); err != nil {
		return nil, fmt.Errorf("seek tail: %w", err)
	}
	// PreviousSkip moves the cursor back without reading; we then read forward.
	if _, err := j.PreviousSkip(uint64(lines)); err != nil {
		return nil, fmt.Errorf("previous skip: %w", err)
	}

	minPriority := priorityToInt(priorityFloor)
	var entries []JournalEntry

	for len(entries) < lines {
		// Check context cancellation on each iteration.
		if err := ctx.Err(); err != nil {
			break
		}

		n, err := j.Next()
		if err != nil {
			return nil, fmt.Errorf("journal next: %w", err)
		}
		if n == 0 {
			break // end of journal
		}

		entry, err := j.GetEntry()
		if err != nil {
			continue // skip unreadable entries
		}

		// Apply priority floor: skip entries below the configured minimum.
		if priStr, ok := entry.Fields[sdjournal.SD_JOURNAL_FIELD_PRIORITY]; ok {
			priInt, convErr := strconv.Atoi(priStr)
			if convErr == nil && priInt > minPriority {
				continue // higher number = lower priority in syslog convention
			}
		}

		entries = append(entries, JournalEntry{
			Timestamp: entry.RealtimeTimestamp,
			Message:   entry.Fields[sdjournal.SD_JOURNAL_FIELD_MESSAGE],
			Unit:      entry.Fields[sdjournal.SD_JOURNAL_FIELD_SYSTEMD_UNIT],
			Priority:  entry.Fields[sdjournal.SD_JOURNAL_FIELD_PRIORITY],
			Fields:    entry.Fields,
		})
	}

	return entries, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// priorityToInt converts a syslog priority name to its integer value.
// In syslog convention, lower numbers are more severe (0=emerg, 7=debug).
// The priority floor means: include this priority and anything more severe.
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
