package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/wf-pro-dev/tailkit/types"
	"github.com/wf-pro-dev/tailkitd/internal/services"
	"github.com/wf-pro-dev/tailkitd/internal/tools"
	"go.uber.org/zap"
)

type staticOutsiders struct {
	items []services.OutsiderServiceConfig
}

func (s staticOutsiders) ListServices() []services.OutsiderServiceConfig {
	return append([]services.OutsiderServiceConfig(nil), s.items...)
}

type errTools struct{}

func (errTools) List(context.Context) ([]types.Tool, error) {
	return nil, context.DeadlineExceeded
}

func TestServicesHandlerMergesOutsidersAndTools(t *testing.T) {
	toolsDir := t.TempDir()
	data := []byte(`{"name":"devbox","version":"v1.2.3","tsnet_host":"tailkitd-devbox"}`)
	if err := os.WriteFile(filepath.Join(toolsDir, "devbox.json"), data, 0o644); err != nil {
		t.Fatalf("write tool file: %v", err)
	}

	handler := &ServicesHandler{
		Outsiders: staticOutsiders{
			items: []services.OutsiderServiceConfig{
				{
					Name:          "nginx",
					Runtime:       "systemd",
					Priority:      "high",
					Tags:          []string{"edge"},
					ExpectedPorts: []uint16{80, 443},
					SystemdUnit:   "nginx.service",
				},
			},
		},
		Tools: tools.NewRegistry(toolsDir, zap.NewNop()),
	}

	req := httptest.NewRequest(http.MethodGet, "/services", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp []ServiceResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp) != 2 {
		t.Fatalf("len(resp) = %d, want 2", len(resp))
	}
	if resp[0].Source != "outsider" || resp[1].Source != "tool" {
		t.Fatalf("sources = %q, %q, want outsider then tool", resp[0].Source, resp[1].Source)
	}
}

func TestServicesHandlerRejectsWrongMethod(t *testing.T) {
	handler := &ServicesHandler{
		Outsiders: staticOutsiders{},
		Tools:     tools.NewRegistry(t.TempDir(), zap.NewNop()),
	}

	req := httptest.NewRequest(http.MethodPost, "/services", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestServicesHandlerReturnsToolListError(t *testing.T) {
	handler := &ServicesHandler{
		Outsiders: staticOutsiders{},
		Tools:     errTools{},
	}

	req := httptest.NewRequest(http.MethodGet, "/services", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
