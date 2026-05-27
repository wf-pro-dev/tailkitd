package identity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

var (
	IdentityDirPath        = "/etc/tailkitd"
	ArtifactPrivateKeyPath = "/etc/tailkitd/artifact.key"
	ArtifactPublicKeyPath  = "/etc/tailkitd/artifact.pub"
)

func EnsureArtifactKeys(_ context.Context, logger *zap.Logger) error {
	if logger == nil {
		logger = zap.NewNop()
	}
	if err := os.MkdirAll(IdentityDirPath, 0o755); err != nil {
		return fmt.Errorf("identity: mkdir %s: %w", IdentityDirPath, err)
	}

	if _, err := os.Stat(ArtifactPrivateKeyPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("identity: stat %s: %w", ArtifactPrivateKeyPath, err)
	}

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("identity: generate artifact keypair: %w", err)
	}

	if err := os.WriteFile(ArtifactPrivateKeyPath, encodePrivateKey(privateKey), 0o600); err != nil {
		return fmt.Errorf("identity: write %s: %w", ArtifactPrivateKeyPath, err)
	}
	if err := os.WriteFile(ArtifactPublicKeyPath, encodePublicKey(publicKey), 0o644); err != nil {
		return fmt.Errorf("identity: write %s: %w", ArtifactPublicKeyPath, err)
	}

	logger.Info("artifact identity keys generated",
		zap.String("private_key_path", ArtifactPrivateKeyPath),
		zap.String("public_key_path", ArtifactPublicKeyPath),
	)
	return nil
}

func LoadPublicKey() (ed25519.PublicKey, error) {
	data, err := os.ReadFile(ArtifactPublicKeyPath)
	if err != nil {
		return nil, err
	}
	return parsePublicKey(data)
}

func LoadPrivateKey() (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(ArtifactPrivateKeyPath)
	if err != nil {
		return nil, err
	}
	return parsePrivateKey(data)
}

func ReadPublicKeyString() (string, error) {
	data, err := os.ReadFile(ArtifactPublicKeyPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func encodePublicKey(key ed25519.PublicKey) []byte {
	return []byte(base64.StdEncoding.EncodeToString(key) + "\n")
}

func encodePrivateKey(key ed25519.PrivateKey) []byte {
	return []byte(base64.StdEncoding.EncodeToString(key) + "\n")
}

func parsePublicKey(data []byte) (ed25519.PublicKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("identity: decode public key: %w", err)
	}
	if len(decoded) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("identity: invalid public key size %d", len(decoded))
	}
	return ed25519.PublicKey(decoded), nil
}

func parsePrivateKey(data []byte) (ed25519.PrivateKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("identity: decode private key: %w", err)
	}
	if len(decoded) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("identity: invalid private key size %d", len(decoded))
	}
	return ed25519.PrivateKey(decoded), nil
}

func PathsFor(dir string) (string, string) {
	return filepath.Join(dir, "artifact.key"), filepath.Join(dir, "artifact.pub")
}
