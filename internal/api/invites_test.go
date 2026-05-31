package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	tailkit "github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkitd/internal/identity"
	"github.com/wf-pro-dev/tailkitd/internal/invite"
	"go.uber.org/zap"
)

func TestInviteClaimHandlerClaimsOnceForMatchingIdentity(t *testing.T) {
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

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := writeBase64Key(identity.ArtifactPrivateKeyPath, base64.StdEncoding.EncodeToString(priv)); err != nil {
		t.Fatalf("write priv: %v", err)
	}
	if err := writeBase64Key(identity.ArtifactPublicKeyPath, base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))); err != nil {
		t.Fatalf("write pub: %v", err)
	}

	store, err := invite.NewStore(filepath.Join(t.TempDir(), "claims.json"), zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	handler := &InviteClaimHandler{Store: store}

	payload, err := invite.NewPayload("alice@example.com", "bob@example.com", "tailkitd-a", "svc", "hash", time.Now().Add(time.Hour), true)
	if err != nil {
		t.Fatalf("NewPayload: %v", err)
	}
	token, err := invite.Sign(payload, priv)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	reqBody, _ := json.Marshal(map[string]string{"token": token})
	req := httptest.NewRequest(http.MethodPost, "/services/claim", bytes.NewReader(reqBody))
	req = req.WithContext(context.WithValue(req.Context(), tailkit.CallerContextKey{}, tailkit.CallerIdentity{UserLogin: "bob@example.com"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/services/claim", bytes.NewReader(reqBody))
	req2 = req2.WithContext(context.WithValue(req2.Context(), tailkit.CallerContextKey{}, tailkit.CallerIdentity{UserLogin: "bob@example.com"}))
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusGone {
		t.Fatalf("second status = %d, want %d", rec2.Code, http.StatusGone)
	}
}

func TestInviteClaimHandlerRejectsWrongIdentity(t *testing.T) {
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

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := writeBase64Key(identity.ArtifactPrivateKeyPath, base64.StdEncoding.EncodeToString(priv)); err != nil {
		t.Fatalf("write priv: %v", err)
	}
	if err := writeBase64Key(identity.ArtifactPublicKeyPath, base64.StdEncoding.EncodeToString(pub)); err != nil {
		t.Fatalf("write pub: %v", err)
	}

	store, err := invite.NewStore(filepath.Join(t.TempDir(), "claims.json"), zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	handler := &InviteClaimHandler{Store: store}
	payload, _ := invite.NewPayload("alice@example.com", "bob@example.com", "tailkitd-a", "svc", "hash", time.Now().Add(time.Hour), true)
	token, _ := invite.Sign(payload, priv)
	reqBody, _ := json.Marshal(map[string]string{"token": token})
	req := httptest.NewRequest(http.MethodPost, "/services/claim", bytes.NewReader(reqBody))
	req = req.WithContext(context.WithValue(req.Context(), tailkit.CallerContextKey{}, tailkit.CallerIdentity{UserLogin: "eve@example.com"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func writeBase64Key(path, data string) error {
	return os.WriteFile(path, []byte(data+"\n"), 0o600)
}
