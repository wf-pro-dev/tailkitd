package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	tailkit "github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkitd/internal/identity"
	"go.uber.org/zap"
)

func TestIdentityHandlerServesPublicKey(t *testing.T) {
	dir := t.TempDir()
	oldDir := identity.IdentityDirPath
	oldPriv := identity.ArtifactPrivateKeyPath
	oldPub := identity.ArtifactPublicKeyPath
	identity.IdentityDirPath = dir
	identity.ArtifactPrivateKeyPath, identity.ArtifactPublicKeyPath = identity.PathsFor(dir)
	defer func() {
		identity.IdentityDirPath = oldDir
		identity.ArtifactPrivateKeyPath = oldPriv
		identity.ArtifactPublicKeyPath = oldPub
	}()

	if err := identity.EnsureArtifactKeys(context.Background(), zap.NewNop()); err != nil {
		t.Fatalf("EnsureArtifactKeys: %v", err)
	}

	handler := &IdentityHandler{NodeHostname: "tailkitd-node-a"}
	req := httptest.NewRequest(http.MethodGet, "/identity/pubkey", nil)
	req = req.WithContext(context.WithValue(req.Context(), tailkit.CallerContextKey{}, tailkit.CallerIdentity{
		UserLogin: "alice@example.com",
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["node_hostname"] != "tailkitd-node-a" {
		t.Fatalf("node_hostname = %q, want %q", resp["node_hostname"], "tailkitd-node-a")
	}
	if resp["tailscale_identity"] != "alice@example.com" {
		t.Fatalf("tailscale_identity = %q, want %q", resp["tailscale_identity"], "alice@example.com")
	}
	if resp["artifact_public_key"] == "" {
		t.Fatal("artifact_public_key is empty")
	}
}
