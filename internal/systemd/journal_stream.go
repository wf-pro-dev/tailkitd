package systemd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"

	"go.uber.org/zap"

	"github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkit/types"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
	"github.com/wf-pro-dev/tailkitd/internal/sse"
)

func (h *Handler) streamJournal(w http.ResponseWriter, r *http.Request, unit string, lines int, priority string) {
	sse.Handler(h.streamHeartbeatInterval, func(ctx context.Context, sw *sse.Writer) error {
		return h.followJournal(ctx, unit, lines, priority, func(entry types.JournalEntry) error {
			return sw.Send(tailkit.EventJournalEntry, entry)
		})
	})(w, r)
}

func (h *Handler) defaultFollowJournal(ctx context.Context, unit string, lines int, priority string, fn func(types.JournalEntry) error) error {
	args := []string{
		"--output=json",
		"--no-pager",
		"--quiet",
		"--follow",
		"--lines", strconv.Itoa(lines),
		"--priority", priority,
	}
	if unit != "" {
		args = append(args, "--unit", unit)
	}

	cmd := exec.CommandContext(ctx, "journalctl", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("journalctl stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("journalctl start: %w: %s", err, stderr.String())
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Bytes()
		var raw journalctlJSON
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}
		entries := parseJournalOutput(append([]byte(nil), line...))
		for _, entry := range entries {
			if err := fn(entry); err != nil {
				_ = cmd.Wait()
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Wait()
		return fmt.Errorf("journalctl scan: %w", err)
	}
	if err := cmd.Wait(); err != nil && !isNoEntriesExit(err) && ctx.Err() == nil {
		return fmt.Errorf("journalctl: %w: %s", err, stderr.String())
	}
	return ctx.Err()
}

func (h *Handler) handleUnitJournalFollowOrSnapshot(w http.ResponseWriter, r *http.Request, unit string) {
	lines := parseLines(r, h.client.cfg.Journal.Lines)
	if r.URL.Query().Get("follow") == "true" {
		h.streamJournal(w, r, unit, lines, h.client.cfg.Journal.Priority)
		return
	}

	entries, err := h.readJournal(r.Context(), unit, lines, h.client.cfg.Journal.Priority)
	if err != nil {
		h.logger.Error("systemd: journal read failed", zap.String("unit", unit), zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError, "failed to read journal", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, entries)
}
