package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/wf-pro-dev/tailkitd/internal/config"
	"go.uber.org/zap"
	"tailscale.com/ipn/ipnstate"
)

type fakeStatusClient struct {
	status *ipnstate.Status
	err    error
}

func (f fakeStatusClient) Status(context.Context) (*ipnstate.Status, error) {
	return f.status, f.err
}

func TestHostHandlerServesMergedResponse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.toml")
	data := []byte(`
name = "db-01"
role = "database"
environment = "prod"
provider = "aws"
instance_type = "t3.medium"
tags = ["critical", "stateful"]

[metadata]
owner = "platform"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr, err := config.NewHostManager(ctx, path, "ts-host", zap.NewNop())
	if err != nil {
		t.Fatalf("NewHostManager returned error: %v", err)
	}
	defer mgr.Close()

	handler := &HostHandler{
		LocalClient: fakeStatusClient{
			status: &ipnstate.Status{
				Version: "linux/amd64",
				Self: &ipnstate.PeerStatus{
					HostName:     "db-01-ts",
					DNSName:      "db-01-ts.tailnet.ts.net.",
					OS:           "linux",
					TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.10")},
				},
			},
		},
		HostManager: mgr,
	}

	req := httptest.NewRequest(http.MethodGet, "/host", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp HostResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Name != "db-01" {
		t.Fatalf("Name = %q, want %q", resp.Name, "db-01")
	}
	if resp.Role != "database" {
		t.Fatalf("Role = %q, want %q", resp.Role, "database")
	}
	if resp.TSHostname != "db-01-ts" {
		t.Fatalf("TSHostname = %q, want %q", resp.TSHostname, "db-01-ts")
	}
	if len(resp.TSIPs) != 1 || resp.TSIPs[0] != "100.64.0.10" {
		t.Fatalf("TSIPs = %#v, want [100.64.0.10]", resp.TSIPs)
	}
}

func TestHostHandlerRejectsWrongMethod(t *testing.T) {
	handler := &HostHandler{}

	req := httptest.NewRequest(http.MethodPost, "/host", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
