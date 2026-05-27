package identity

import (
	"context"
	"crypto/ed25519"
	"os"
	"testing"

	"go.uber.org/zap"
)

func TestEnsureArtifactKeysCreatesKeypair(t *testing.T) {
	dir := t.TempDir()
	oldDir := IdentityDirPath
	oldPriv := ArtifactPrivateKeyPath
	oldPub := ArtifactPublicKeyPath
	IdentityDirPath = dir
	ArtifactPrivateKeyPath, ArtifactPublicKeyPath = PathsFor(dir)
	defer func() {
		IdentityDirPath = oldDir
		ArtifactPrivateKeyPath = oldPriv
		ArtifactPublicKeyPath = oldPub
	}()

	if err := EnsureArtifactKeys(context.Background(), zap.NewNop()); err != nil {
		t.Fatalf("EnsureArtifactKeys returned error: %v", err)
	}

	privInfo, err := os.Stat(ArtifactPrivateKeyPath)
	if err != nil {
		t.Fatalf("stat private key: %v", err)
	}
	if privInfo.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode = %o, want 600", privInfo.Mode().Perm())
	}

	pubInfo, err := os.Stat(ArtifactPublicKeyPath)
	if err != nil {
		t.Fatalf("stat public key: %v", err)
	}
	if pubInfo.Mode().Perm() != 0o644 {
		t.Fatalf("public key mode = %o, want 644", pubInfo.Mode().Perm())
	}

	publicKey, err := LoadPublicKey()
	if err != nil {
		t.Fatalf("LoadPublicKey: %v", err)
	}
	privateKey, err := LoadPrivateKey()
	if err != nil {
		t.Fatalf("LoadPrivateKey: %v", err)
	}
	msg := []byte("artifact")
	sig := ed25519.Sign(privateKey, msg)
	if !ed25519.Verify(publicKey, msg, sig) {
		t.Fatal("generated keypair failed sign/verify roundtrip")
	}
}
