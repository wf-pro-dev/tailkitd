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

	tailkit "github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkitd/internal/access"
	"github.com/wf-pro-dev/tailkitd/internal/admin"
	"github.com/wf-pro-dev/tailkitd/internal/config"
	"github.com/wf-pro-dev/tailkitd/internal/services"
	"github.com/wf-pro-dev/tailkitd/internal/state"
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
	oldAccessDir := access.DefaultAccessDir
	oldEpochPath := state.EpochFilePath

	hostConfigPath := filepath.Join(base, "hosts.toml")
	servicesDir := filepath.Join(base, "services.d")
	accessDir := filepath.Join(base, "access.d")
	epochPath := filepath.Join(base, "state.epoch")
	admin.AdminDirPath = base
	admin.AdminKeyPath = filepath.Join(base, "admin.key")
	admin.AdminFencePath = filepath.Join(base, "admin.fence")
	access.DefaultAccessDir = accessDir
	state.EpochFilePath = epochPath
	t.Cleanup(func() {
		admin.AdminDirPath = oldAdminDir
		admin.AdminKeyPath = oldAdminKey
		admin.AdminFencePath = oldAdminFence
		access.DefaultAccessDir = oldAccessDir
		state.EpochFilePath = oldEpochPath
	})

	if err := os.WriteFile(hostConfigPath, []byte("[[hosts]]\nname = \"node-a-tailnet\"\n"), 0o644); err != nil {
		t.Fatalf("write host config: %v", err)
	}
	if err := admin.EnsureBootstrapFiles(); err != nil {
		t.Fatalf("EnsureBootstrapFiles: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	hostMgr, err := config.NewHostManager(ctx, hostConfigPath, "node-a-tailnet", zap.NewNop())
	if err != nil {
		t.Fatalf("NewHostManager: %v", err)
	}
	t.Cleanup(func() { _ = hostMgr.Close() })

	svcReg, err := services.NewRegistry(ctx, servicesDir, zap.NewNop())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = svcReg.Close() })

	accessReg, err := access.NewRegistry(ctx, accessDir, zap.NewNop())
	if err != nil {
		t.Fatalf("NewRegistry access: %v", err)
	}
	t.Cleanup(func() { _ = accessReg.Close() })
	epoch, err := state.NewEpoch(epochPath)
	if err != nil {
		t.Fatalf("NewEpoch: %v", err)
	}

	adminState := &admin.State{}
	adminState.SetAdmin(true)
	return &AdminHandler{
		Hostname:       "tailkitd-node-a",
		HostConfig:     hostMgr,
		HostConfigPath: hostConfigPath,
		Services:       svcReg,
		ServicesDir:    servicesDir,
		AdminState:     adminState,
		AdminFencePath: admin.AdminFencePath,
		AccessRegistry: accessReg,
		Epoch:          epoch,
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

	body := []byte(`{"name":"node-a-tailnet","role":"db"}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/hosts/me/config", bytes.NewReader(body))
	req.Header.Set("X-Admin-Key", key)
	req.Header.Set("X-State-Epoch", "0")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := handler.HostConfig.Get().Role; got != "db" {
		t.Fatalf("role = %q, want %q", got, "db")
	}
}

func TestAdminHandlerPushHostConfigRejectsRenamingPeer(t *testing.T) {
	handler, _ := newAdminHandlerForTest(t)
	key, err := admin.GetAdminKey()
	if err != nil {
		t.Fatalf("GetAdminKey: %v", err)
	}

	body := []byte(`{"name":"other-peer","role":"db"}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/hosts/me/config", bytes.NewReader(body))
	req.Header.Set("X-Admin-Key", key)
	req.Header.Set("X-State-Epoch", "0")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
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
	req.Header.Set("X-State-Epoch", "0")
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
	req.Header.Set("X-State-Epoch", "0")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestAdminHandlerRBACDeniesWithoutGrant(t *testing.T) {
	handler, _ := newAdminHandlerForTest(t)
	key, err := admin.GetAdminKey()
	if err != nil {
		t.Fatalf("GetAdminKey: %v", err)
	}

	grantsBody := []byte(`{"grants":[{"identity":"alice@example.com","target":"nextcloud","role":"admin"}]}`)
	grantsReq := httptest.NewRequest(http.MethodPost, "/admin/access/grants", bytes.NewReader(grantsBody))
	grantsReq.Header.Set("X-Admin-Key", key)
	grantsReq.Header.Set("X-State-Epoch", "0")
	grantsReq = grantsReq.WithContext(context.WithValue(grantsReq.Context(), tailkit.CallerContextKey{}, tailkit.CallerIdentity{UserLogin: "alice@example.com"}))
	grantsRec := httptest.NewRecorder()
	handler.ServeHTTP(grantsRec, grantsReq)
	if grantsRec.Code != http.StatusOK {
		t.Fatalf("grant status = %d, want %d", grantsRec.Code, http.StatusOK)
	}

	body := []byte(`{"runtime":"systemd","systemd_unit":"nginx.service"}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/hosts/me/services/other", bytes.NewReader(body))
	req.Header.Set("X-Admin-Key", key)
	req.Header.Set("X-State-Epoch", "1")
	req = req.WithContext(context.WithValue(req.Context(), tailkit.CallerContextKey{}, tailkit.CallerIdentity{UserLogin: "bob@example.com"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestAdminHandlerRejectsStaleEpoch(t *testing.T) {
	handler, _ := newAdminHandlerForTest(t)
	key, err := admin.GetAdminKey()
	if err != nil {
		t.Fatalf("GetAdminKey: %v", err)
	}

	req1 := httptest.NewRequest(http.MethodPost, "/admin/hosts/me/config", bytes.NewReader([]byte(`{"name":"node-a-tailnet"}`)))
	req1.Header.Set("X-Admin-Key", key)
	req1.Header.Set("X-State-Epoch", "0")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", rec1.Code, http.StatusOK)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/admin/hosts/me/config", bytes.NewReader([]byte(`{"name":"node-a-tailnet","role":"db"}`)))
	req2.Header.Set("X-Admin-Key", key)
	req2.Header.Set("X-State-Epoch", "0")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("second status = %d, want %d", rec2.Code, http.StatusConflict)
	}
}
