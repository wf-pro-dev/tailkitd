package provision

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/wf-pro-dev/tailkitd/internal/services"
	"github.com/wf-pro-dev/tailkitd/internal/utils"
)

var BaseDir = "/var/lib/tailkitd/services"

type Request struct {
	ArtifactHash    string   `json:"artifact_hash"`
	Signature       string   `json:"signature"`
	SenderPublicKey string   `json:"sender_public_key"`
	ServiceName     string   `json:"service_name"`
	Runtime         string   `json:"runtime"`
	StagedPath      string   `json:"staged_path"`
	ExpectedPorts   []uint16 `json:"expected_ports"`
}

type Metadata struct {
	ServiceName   string    `json:"service_name"`
	Runtime       string    `json:"runtime"`
	ArtifactHash  string    `json:"artifact_hash"`
	ArtifactPath  string    `json:"artifact_path"`
	ProvisionedAt time.Time `json:"provisioned_at"`
}

func VerifyArtifact(req Request) error {
	data, err := os.ReadFile(req.StagedPath)
	if err != nil {
		return fmt.Errorf("read staged artifact: %w", err)
	}
	sum := sha256.Sum256(data)
	actualHash := hex.EncodeToString(sum[:])
	if actualHash != req.ArtifactHash {
		return fmt.Errorf("artifact hash mismatch")
	}

	publicKeyBytes, err := base64.StdEncoding.DecodeString(req.SenderPublicKey)
	if err != nil {
		return fmt.Errorf("decode sender public key: %w", err)
	}
	signatureBytes, err := base64.StdEncoding.DecodeString(req.Signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKeyBytes), sum[:], signatureBytes) {
		return fmt.Errorf("artifact signature invalid")
	}
	return nil
}

func Provision(_ context.Context, req Request, servicesDir string) (*Metadata, error) {
	switch req.Runtime {
	case "port-only", "restricted-systemd":
	default:
		return nil, fmt.Errorf("unsupported runtime %q", req.Runtime)
	}

	if err := VerifyArtifact(req); err != nil {
		return nil, err
	}

	serviceDir := filepath.Join(BaseDir, req.ServiceName)
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		return nil, err
	}

	artifactPath := filepath.Join(serviceDir, "artifact")
	data, err := os.ReadFile(req.StagedPath)
	if err != nil {
		return nil, err
	}
	if err := utils.AtomicWrite(artifactPath, data, 0o550); err != nil {
		return nil, err
	}

	meta := &Metadata{
		ServiceName:   req.ServiceName,
		Runtime:       req.Runtime,
		ArtifactHash:  req.ArtifactHash,
		ArtifactPath:  artifactPath,
		ProvisionedAt: time.Now().UTC(),
	}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := utils.AtomicWrite(filepath.Join(serviceDir, "meta.json"), metaBytes, 0o644); err != nil {
		return nil, err
	}

	svc := services.OutsiderServiceConfig{
		Name:          req.ServiceName,
		Priority:      "normal",
		Tags:          []string{"provisioned"},
		ExpectedPorts: req.ExpectedPorts,
	}
	switch req.Runtime {
	case "port-only":
		svc.Runtime = "port-only"
	case "restricted-systemd":
		svc.Runtime = "binary"
		svc.BinaryPath = artifactPath
		svc.PidFile = filepath.Join(serviceDir, "service.pid")
	}
	if err := svc.Validate(); err != nil {
		return nil, err
	}

	serviceFile := services.FilePath(servicesDir, req.ServiceName)
	var encoded []byte
	{
		buf, err := encodeService(svc)
		if err != nil {
			return nil, err
		}
		encoded = buf
	}
	if err := os.MkdirAll(filepath.Dir(serviceFile), 0o755); err != nil {
		return nil, err
	}
	if err := utils.AtomicWrite(serviceFile, encoded, 0o644); err != nil {
		return nil, err
	}

	return meta, nil
}

func encodeService(svc services.OutsiderServiceConfig) ([]byte, error) {
	var buf bytes.Buffer
	if err := services.EncodeTOML(&buf, svc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
