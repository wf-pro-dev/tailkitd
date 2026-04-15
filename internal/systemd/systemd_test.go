package systemd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	integrationtypes "github.com/wf-pro-dev/tailkit/types/integrations"
	"github.com/wf-pro-dev/tailkitd/internal/config"
)

func TestSystemdEndpointsBasicUsage(t *testing.T) {
	t.Parallel()

	cfg := config.SystemdConfig{
		Enabled: true,
		Units: integrationtypes.UnitConfig{
			Enabled: true,
			Allow:   []string{"list", "inspect"},
		},
		Journal: integrationtypes.JournalConfig{
			Enabled:  true,
			Lines:    50,
			Priority: "info",
		},
	}

	handler := NewHandler(&Client{cfg: cfg, logger: zap.NewNop()}, nil, zap.NewNop())
	mux := http.NewServeMux()
	handler.Register(mux)

	t.Run("available and config", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/integrations/systemd/available", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("available status = %d, want %d", rec.Code, http.StatusOK)
		}

		var available map[string]bool
		if err := json.Unmarshal(rec.Body.Bytes(), &available); err != nil {
			t.Fatalf("unmarshal available response: %v", err)
		}
		if available["available"] {
			t.Fatalf("available = true, want false without D-Bus connection")
		}

		req = httptest.NewRequest(http.MethodGet, "/integrations/systemd/config", nil)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("config status = %d, want %d", rec.Code, http.StatusOK)
		}

		var got config.SystemdConfig
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal config response: %v", err)
		}
		if !got.Enabled || !got.Units.Enabled || got.Journal.Lines != 50 {
			t.Fatalf("config = %#v, want enabled units and journal lines 50", got)
		}
	})

	t.Run("units guard when unavailable", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/integrations/systemd/units", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("units status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
		}
	})
}

func TestSystemdUnitJournalFollowStream(t *testing.T) {
	t.Parallel()

	cfg := config.SystemdConfig{
		Enabled: true,
		Journal: integrationtypes.JournalConfig{
			Enabled:  true,
			Lines:    50,
			Priority: "info",
		},
	}

	handler := NewHandler(&Client{cfg: cfg, logger: zap.NewNop()}, nil, zap.NewNop())
	handler.streamHeartbeatInterval = 50 * time.Millisecond
	handler.followJournal = func(ctx context.Context, unit string, lines int, priority string, fn func(JournalEntry) error) error {
		if unit != "tailkitd.service" {
			t.Fatalf("unit = %q, want %q", unit, "tailkitd.service")
		}
		if lines != 25 {
			t.Fatalf("lines = %d, want %d", lines, 25)
		}
		if priority != "info" {
			t.Fatalf("priority = %q, want %q", priority, "info")
		}
		return fn(JournalEntry{
			Timestamp: 1742300000000000,
			Message:   "Started tailkitd node agent.",
			Unit:      unit,
			Priority:  "info",
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/integrations/systemd/units/tailkitd.service/journal?follow=true&lines=25", nil)
	rec := httptest.NewRecorder()

	handler.handleUnitJournal(rec, req, "tailkitd.service")

	body := rec.Body.String()
	if !strings.Contains(body, "event: journal.entry") || !strings.Contains(body, "\"message\":\"Started tailkitd node agent.\"") {
		t.Fatalf("unexpected body:\n%s", body)
	}
}

func TestSystemdSystemJournalFollowStream(t *testing.T) {
	t.Parallel()

	cfg := config.SystemdConfig{
		Enabled: true,
		Journal: integrationtypes.JournalConfig{
			Enabled:       true,
			Lines:         50,
			Priority:      "info",
			SystemJournal: true,
		},
	}

	handler := NewHandler(&Client{cfg: cfg, logger: zap.NewNop()}, nil, zap.NewNop())
	handler.streamHeartbeatInterval = 50 * time.Millisecond
	handler.available = func(context.Context) bool { return true }
	handler.followJournal = func(ctx context.Context, unit string, lines int, priority string, fn func(JournalEntry) error) error {
		if unit != "" {
			t.Fatalf("unit = %q, want empty", unit)
		}
		return fn(JournalEntry{
			Timestamp: 1742300000000001,
			Message:   "system message",
			Priority:  "notice",
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/integrations/systemd/journal?follow=true", nil)
	rec := httptest.NewRecorder()

	handler.handleSystemJournal(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "event: journal.entry") || !strings.Contains(body, "\"message\":\"system message\"") {
		t.Fatalf("unexpected body:\n%s", body)
	}
}
