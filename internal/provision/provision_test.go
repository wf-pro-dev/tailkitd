package provision

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestProvisionStagesArtifactAndWritesService(t *testing.T) {
	base := t.TempDir()
	oldBase := BaseDir
	BaseDir = filepath.Join(base, "services")
	defer func() { BaseDir = oldBase }()

	staged := filepath.Join(base, "artifact.bin")
	data := []byte("hello")
	if err := os.WriteFile(staged, data, 0o644); err != nil {
		t.Fatalf("write staged: %v", err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	sum := sha256.Sum256(data)
	req := Request{
		ArtifactHash:    hex.EncodeToString(sum[:]),
		Signature:       base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sum[:])),
		SenderPublicKey: base64.StdEncoding.EncodeToString(pub),
		ServiceName:     "svc",
		Runtime:         "port-only",
		StagedPath:      staged,
		ExpectedPorts:   []uint16{8080},
	}

	servicesDir := filepath.Join(base, "services.d")
	meta, err := Provision(context.Background(), req, servicesDir)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if meta.ServiceName != "svc" {
		t.Fatalf("ServiceName = %q, want %q", meta.ServiceName, "svc")
	}
	if _, err := os.Stat(filepath.Join(BaseDir, "svc", "meta.json")); err != nil {
		t.Fatalf("stat meta.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(servicesDir, "svc.toml")); err != nil {
		t.Fatalf("stat service file: %v", err)
	}
}
