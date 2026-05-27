package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/wf-pro-dev/tailkitd/internal/admin"
	"github.com/wf-pro-dev/tailkitd/internal/config"
	"github.com/wf-pro-dev/tailkitd/internal/services"
	"go.uber.org/zap"
)

type fakePromoter struct {
	err error
}

func (f fakePromoter) Promote(context.Context, string, int) error {
	return f.err
}

func newAdminHandlerForTest(t *testing.T) (*AdminHandler, string) {
	t.Helper()

	base := t.TempDir()
	oldAdminDir := admin.AdminDirPath
	oldAdminKey := admin.AdminKeyPath
	oldAdminFence := admin.AdminFencePath

	hostConfigPath := filepath.Join(base, "host.toml")
	servicesDir := filepath.Join(base, "services.d")
	admin.AdminDirPath = base
	admin.AdminKeyPath = filepath.Join(base, "admin.key")
	admin.AdminFencePath = filepath.Join(base, "admin.fence")
	t.Cleanup(func() {
		admin.AdminDirPath = oldAdminDir
		admin.AdminKeyPath = oldAdminKey
		admin.AdminFencePath = oldAdminFence
	})

	if err := os.WriteFile(hostConfigPath, []byte("name = \"node-a\"\n"), 0o644); err != nil {
		t.Fatalf("write host config: %v", err)
	}
	if err := admin.EnsureBootstrapFiles(); err != nil {
		t.Fatalf("EnsureBootstrapFiles: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	hostMgr, err := config.NewHostManager(ctx, hostConfigPath, "tailkitd-node-a", zap.NewNop())
	if err != nil {
		t.Fatalf("NewHostManager: %v", err)
	}
	t.Cleanup(func() { _ = hostMgr.Close() })

	svcReg, err := services.NewRegistry(ctx, servicesDir, zap.NewNop())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = svcReg.Close() })

	state := &admin.State{}
	state.SetAdmin(true)
	return &AdminHandler{
		Hostname:       "tailkitd-node-a",
		HostConfig:     hostMgr,
		HostConfigPath: hostConfigPath,
		Services:       svcReg,
		ServicesDir:    servicesDir,
		AdminState:     state,
		AdminFencePath: admin.AdminFencePath,
		Promoter:       fakePromoter{},
	}, base
}

func TestAdminHandlerRejectsMissingKey(t *testing.T) {
	handler, _ := newAdminHandlerForTest(t)

	req := httptest.NewRequest(http.MethodPost, "/admin/files/write", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAdminHandlerPushHostConfig(t *testing.T) {
	handler, _ := newAdminHandlerForTest(t)
	key, err := admin.GetAdminKey()
	if err != nil {
		t.Fatalf("GetAdminKey: %v", err)
	}

	body := []byte(`{"name":"node-b","role":"db"}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/hosts/me/config", bytes.NewReader(body))
	req.Header.Set("X-Admin-Key", key)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := handler.HostConfig.Get().Role; got != "db" {
		t.Fatalf("role = %q, want %q", got, "db")
	}
}

func TestAdminHandlerAtomicWriteRejectsDisallowedPath(t *testing.T) {
	handler, _ := newAdminHandlerForTest(t)
	key, err := admin.GetAdminKey()
	if err != nil {
		t.Fatalf("GetAdminKey: %v", err)
	}

	body := []byte(`{"path":"/tmp/not-allowed","data":"x","encoding":"utf-8"}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/files/write", bytes.NewReader(body))
	req.Header.Set("X-Admin-Key", key)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestAdminHandlerAtomicWriteAllowsBase64(t *testing.T) {
	handler, _ := newAdminHandlerForTest(t)
	key, err := admin.GetAdminKey()
	if err != nil {
		t.Fatalf("GetAdminKey: %v", err)
	}

	encoded := base64.StdEncoding.EncodeToString([]byte("hello"))
	body, _ := json.Marshal(map[string]any{
		"path":     handler.hostConfigPath(),
		"encoding": "base64",
		"data":     encoded,
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/files/write", bytes.NewReader(body))
	req.Header.Set("X-Admin-Key", key)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}
