package docker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dockertypescontainer "github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkit/types"
	"go.uber.org/zap"

	"github.com/wf-pro-dev/tailkitd/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newTestDockerClient(t *testing.T, transport http.RoundTripper) *Client {
	t.Helper()

	httpClient := &http.Client{Transport: transport}
	dc, err := dockerclient.NewClientWithOpts(
		dockerclient.WithHost("http://docker.test"),
		dockerclient.WithVersion("1.44"),
		dockerclient.WithHTTPClient(httpClient),
	)
	if err != nil {
		t.Fatalf("new docker client: %v", err)
	}
	t.Cleanup(func() {
		_ = dc.Close()
	})

	return &Client{docker: dc, logger: zap.NewNop()}
}

func TestDockerEndpointsBasicUsage(t *testing.T) {
	t.Parallel()

	t.Run("available and config", func(t *testing.T) {
		client := newTestDockerClient(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(req.URL.Path, "/_ping") {
				t.Fatalf("unexpected path: %s", req.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("OK")),
				Request:    req,
			}, nil
		}))

		cfg := config.DockerConfig{Enabled: true}
		handler := NewHandler(cfg, client, nil, zap.NewNop())
		mux := http.NewServeMux()
		handler.Register(mux)

		req := httptest.NewRequest(http.MethodGet, "/integrations/docker/available", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("available status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var available map[string]bool
		if err := json.Unmarshal(rec.Body.Bytes(), &available); err != nil {
			t.Fatalf("unmarshal available response: %v", err)
		}
		if !available["available"] {
			t.Fatalf("available = false, want true")
		}

		req = httptest.NewRequest(http.MethodGet, "/integrations/docker/config", nil)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("config status = %d, want %d", rec.Code, http.StatusOK)
		}

		var got config.DockerConfig
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal config response: %v", err)
		}
		if !got.Enabled {
			t.Fatalf("Enabled = false, want true")
		}
	})

	t.Run("disabled containers endpoint", func(t *testing.T) {
		handler := NewHandler(config.DockerConfig{}, &Client{logger: zap.NewNop()}, nil, zap.NewNop())
		req := httptest.NewRequest(http.MethodGet, "/integrations/docker/containers", nil)
		rec := httptest.NewRecorder()

		handler.handleContainers(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
		}
	})
}

func TestDockerContainerLogsFollowStream(t *testing.T) {
	t.Parallel()

	handler := NewHandler(config.DockerConfig{
		Enabled: true,
		Containers: config.DockerSectionConfig{
			Enabled: true,
			Allow:   []string{"logs"},
		},
	}, &Client{logger: zap.NewNop()}, nil, zap.NewNop())
	handler.streamHeartbeatInterval = 50 * time.Millisecond
	handler.followContainerLogs = func(ctx context.Context, id, tail string, timestamps bool, fn func(types.LogLine) error) error {
		if id != "abc123" {
			t.Fatalf("id = %q, want %q", id, "abc123")
		}
		if tail != "10" {
			t.Fatalf("tail = %q, want %q", tail, "10")
		}
		if !timestamps {
			t.Fatal("timestamps = false, want true")
		}
		for _, line := range []types.LogLine{
			{ContainerID: id, Stream: "stdout", TS: time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC), Line: "server listening on :3000"},
			{ContainerID: id, Stream: "stderr", TS: time.Date(2026, 4, 14, 10, 0, 1, 0, time.UTC), Line: "warning"},
		} {
			if err := fn(line); err != nil {
				return err
			}
		}
		return nil
	}

	req := httptest.NewRequest(http.MethodGet, "/integrations/docker/containers/abc123/logs?follow=true&tail=10&timestamps=true", nil)
	rec := httptest.NewRecorder()

	handler.handleContainerLogs(rec, req, "abc123")

	body := rec.Body.String()
	for _, want := range []string{
		"event: " + tailkit.EventLogLine,
		"\"container_id\":\"abc123\"",
		"\"stream\":\"stdout\"",
		"\"ts\":\"2026-04-14T10:00:00Z\"",
		"\"line\":\"server listening on :3000\"",
		"\"stream\":\"stderr\"",
		"\"ts\":\"2026-04-14T10:00:01Z\"",
		"\"line\":\"warning\"",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestDockerContainerStatsStream(t *testing.T) {
	t.Parallel()

	handler := NewHandler(config.DockerConfig{
		Enabled: true,
		Containers: config.DockerSectionConfig{
			Enabled: true,
			Allow:   []string{"stats"},
		},
	}, &Client{logger: zap.NewNop()}, nil, zap.NewNop())
	handler.streamHeartbeatInterval = 50 * time.Millisecond
	handler.streamContainerStats = func(ctx context.Context, id string, fn func(dockertypescontainer.StatsResponse) error) error {
		if id != "abc123" {
			t.Fatalf("id = %q, want %q", id, "abc123")
		}
		return fn(dockertypescontainer.StatsResponse{
			Name: "/abc123",
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/integrations/docker/containers/abc123/stats", nil)
	rec := httptest.NewRecorder()

	handler.handleContainerStats(rec, req, "abc123")

	body := rec.Body.String()
	if !strings.Contains(body, "event: "+tailkit.EventStatsSnapshot) || !strings.Contains(body, "\"name\":\"/abc123\"") {
		t.Fatalf("unexpected body:\n%s", body)
	}
}
