package metrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wf-pro-dev/tailkit"
	"go.uber.org/zap"

	"github.com/wf-pro-dev/tailkit/types"
	"github.com/wf-pro-dev/tailkitd/internal/config"
)

func TestMetricsEndpointsBasicUsage(t *testing.T) {
	t.Parallel()

	limit := 5
	cfg := config.MetricsConfig{
		Enabled: true,
		Host: config.HostMetricsConfig{
			Enabled: true,
		},
		Processes: config.ProcessMetricsConfig{
			Enabled: true,
			Limit:   &limit,
		},
		Ports: config.PortMetricsConfig{
			Enabled: true,
		},
	}

	handler := NewHandler(cfg, zap.NewNop())
	mux := http.NewServeMux()
	handler.Register(mux)

	t.Run("available", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/integrations/metrics/available", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}

		var got map[string]bool
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if !got["available"] {
			t.Fatalf("available = false, want true")
		}
	})

	t.Run("config", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/integrations/metrics/config", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}

		var got config.MetricsConfig
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal config: %v", err)
		}
		if !got.Enabled || !got.Host.Enabled || !got.Processes.Enabled || !got.Ports.Enabled {
			t.Fatalf("config = %#v, want enabled host, processes, and ports sections", got)
		}
	})

	t.Run("all", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/integrations/metrics/all", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var got types.Metrics
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if got.Host == nil {
			t.Fatalf("Host = nil, want populated host metrics")
		}
		if len(got.Processes) > limit {
			t.Fatalf("len(Processes) = %d, want <= %d", len(got.Processes), limit)
		}
	})
}

func TestMetricsStreamEndpoints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		path      string
		event     string
		configure func(*Handler)
	}{
		{
			name:  "cpu stream",
			path:  "/integrations/metrics/cpu/stream",
			event: tailkit.EventCPU,
			configure: func(h *Handler) {
				h.cfg.CPU.Enabled = true
				h.cpuSampler = func(context.Context) (types.CPU, error) {
					return types.CPU{Percent: []float64{12.5}, Total: 12.5}, nil
				}
			},
		},
		{
			name:  "all stream",
			path:  "/integrations/metrics/all/stream",
			event: tailkit.EventAll,
			configure: func(h *Handler) {
				h.allSampler = func(context.Context) (types.Metrics, error) {
					return types.Metrics{
						Processes: []types.Process{{PID: 100, Name: "tailkitd"}},
					}, nil
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			handler := NewHandler(config.MetricsConfig{Enabled: true}, zap.NewNop())
			handler.streamInterval = 5 * time.Millisecond
			handler.heartbeatInterval = 50 * time.Millisecond
			tt.configure(handler)

			time.AfterFunc(20*time.Millisecond, cancel)

			req := httptest.NewRequest(http.MethodGet, tt.path, nil).WithContext(ctx)
			rec := httptest.NewRecorder()
			http.NewServeMux()
			switch tt.path {
			case "/integrations/metrics/cpu/stream":
				handler.handleCPUStream(rec, req)
			case "/integrations/metrics/all/stream":
				handler.handleAllStream(rec, req)
			}

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if !strings.Contains(rec.Body.String(), "event: "+tt.event) {
				t.Fatalf("body = %q, want event %q", rec.Body.String(), tt.event)
			}
		})
	}
}
