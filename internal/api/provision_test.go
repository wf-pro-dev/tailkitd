package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	tailkit "github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkitd/internal/access"
	"github.com/wf-pro-dev/tailkitd/internal/provision"
	"github.com/wf-pro-dev/tailkitd/internal/services"
	"github.com/wf-pro-dev/tailkitd/internal/state"
	"go.uber.org/zap"
)

func TestProvisionHandlerStagesVerifiedArtifact(t *testing.T) {
	base := t.TempDir()
	oldBase := provision.BaseDir
	oldAccessDir := access.DefaultAccessDir
	oldEpoch := state.EpochFilePath
	provision.BaseDir = filepath.Join(base, "managed")
	access.DefaultAccessDir = filepath.Join(base, "access.d")
	state.EpochFilePath = filepath.Join(base, "state.epoch")
	defer func() {
		provision.BaseDir = oldBase
		access.DefaultAccessDir = oldAccessDir
		state.EpochFilePath = oldEpoch
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	accessReg, err := access.NewRegistry(ctx, access.DefaultAccessDir, zap.NewNop())
	if err != nil {
		t.Fatalf("NewRegistry access: %v", err)
	}
	defer accessReg.Close()
	svcReg, err := services.NewRegistry(ctx, filepath.Join(base, "services.d"), zap.NewNop())
	if err != nil {
		t.Fatalf("NewRegistry services: %v", err)
	}
	defer svcReg.Close()
	epoch, err := state.NewEpoch(state.EpochFilePath)
	if err != nil {
		t.Fatalf("NewEpoch: %v", err)
	}

	staged := filepath.Join(base, "artifact.bin")
	data := []byte("artifact")
	if err := os.WriteFile(staged, data, 0o644); err != nil {
		t.Fatalf("write staged: %v", err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	sum := sha256.Sum256(data)
	reqBody, _ := json.Marshal(provision.Request{
		ArtifactHash:    hex.EncodeToString(sum[:]),
		Signature:       base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sum[:])),
		SenderPublicKey: base64.StdEncoding.EncodeToString(pub),
		ServiceName:     "svc",
		Runtime:         "port-only",
		StagedPath:      staged,
		ExpectedPorts:   []uint16{8080},
	})

	req := httptest.NewRequest(http.MethodPost, "/services/provision", bytes.NewReader(reqBody))
	req.Header.Set("X-State-Epoch", "0")
	req = req.WithContext(context.WithValue(req.Context(), tailkit.CallerContextKey{}, tailkit.CallerIdentity{
		UserLogin: "alice@example.com",
	}))
	rec := httptest.NewRecorder()
	handler := &ProvisionHandler{
		AccessRegistry: accessReg,
		Services:       svcReg,
		ServicesDir:    filepath.Join(base, "services.d"),
		Epoch:          epoch,
	}
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}
