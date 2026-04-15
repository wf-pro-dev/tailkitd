package sse

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewWriterSetsSSEHeaders(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/stream", nil)
	rec := httptest.NewRecorder()

	sw, err := NewWriter(rec, req)
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}
	if sw == nil {
		t.Fatal("NewWriter() returned nil writer")
	}

	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want %q", got, "text/event-stream")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want %q", got, "no-cache")
	}
	if got := rec.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want %q", got, "no")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "*")
	}
}

func TestWriterSendAssignsMonotonicIDs(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/stream", nil)
	rec := httptest.NewRecorder()
	sw, err := NewWriter(rec, req)
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}

	if err := sw.Send("metrics.cpu", map[string]string{"status": "ok"}); err != nil {
		t.Fatalf("Send() first error = %v", err)
	}
	if err := sw.Send("metrics.memory", map[string]string{"status": "ok"}); err != nil {
		t.Fatalf("Send() second error = %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: metrics.cpu\nid: 1\ndata: {\"status\":\"ok\"}\n\n") {
		t.Fatalf("first event missing from body: %q", body)
	}
	if !strings.Contains(body, "event: metrics.memory\nid: 2\ndata: {\"status\":\"ok\"}\n\n") {
		t.Fatalf("second event missing from body: %q", body)
	}
}

func TestWriterHeartbeat(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/stream", nil)
	rec := httptest.NewRecorder()
	sw, err := NewWriter(rec, req)
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}

	if err := sw.Heartbeat(); err != nil {
		t.Fatalf("Heartbeat() error = %v", err)
	}

	if !strings.Contains(rec.Body.String(), ": heartbeat\n\n") {
		t.Fatalf("heartbeat missing from body: %q", rec.Body.String())
	}
}

func TestWriterDetectsCanceledRequest(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/stream", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	sw, err := NewWriter(rec, req)
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}

	cancel()

	if err := sw.Send("metrics.cpu", map[string]string{"status": "ok"}); !errors.Is(err, ErrClientGone) {
		t.Fatalf("Send() error = %v, want %v", err, ErrClientGone)
	}
	if err := sw.Heartbeat(); !errors.Is(err, ErrClientGone) {
		t.Fatalf("Heartbeat() error = %v, want %v", err, ErrClientGone)
	}
}

func TestResumeFrom(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
		want   int64
	}{
		{name: "missing", want: 0},
		{name: "invalid", header: "abc", want: 0},
		{name: "valid", header: "42", want: 42},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/stream", nil)
			if tt.header != "" {
				req.Header.Set("Last-Event-ID", tt.header)
			}
			if got := ResumeFrom(req); got != tt.want {
				t.Fatalf("ResumeFrom() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestHandlerSendsTerminalErrorEvent(t *testing.T) {
	t.Parallel()

	handler := Handler(10*time.Millisecond, func(ctx context.Context, sw *Writer) error {
		return errors.New("boom")
	})

	req := httptest.NewRequest(http.MethodGet, "/stream", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "event: error\nid: 1\ndata: {\"error\":\"boom\"}\n\n") {
		t.Fatalf("error event missing from body: %q", body)
	}
}

func TestHandlerSuppressesCanceledErrors(t *testing.T) {
	t.Parallel()

	handler := Handler(10*time.Millisecond, func(ctx context.Context, sw *Writer) error {
		return context.Canceled
	})

	req := httptest.NewRequest(http.MethodGet, "/stream", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), "event: error") {
		t.Fatalf("unexpected error event in body: %q", rec.Body.String())
	}
}
