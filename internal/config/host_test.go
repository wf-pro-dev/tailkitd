package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func TestNewHostManagerResolvesLocalHostFromFleetFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.toml")
	data := []byte(`
[[hosts]]
name = "peer-a"
role = "database"

[[hosts]]
name = "peer-b"
role = "worker"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr, err := NewHostManager(ctx, path, "peer-b", zap.NewNop())
	if err != nil {
		t.Fatalf("NewHostManager: %v", err)
	}
	defer mgr.Close()

	if got := mgr.Get().Role; got != "worker" {
		t.Fatalf("role = %q, want %q", got, "worker")
	}
}

func TestNewHostManagerRejectsMissingLocalHostEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.toml")
	if err := os.WriteFile(path, []byte("[[hosts]]\nname = \"peer-a\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := NewHostManager(ctx, path, "peer-b", zap.NewNop()); err == nil {
		t.Fatal("NewHostManager returned nil error, want missing local host failure")
	}
}

func TestLoadHostFileRejectsDuplicateNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.toml")
	data := []byte(`
[[hosts]]
name = "peer-a"

[[hosts]]
name = "peer-a"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := LoadHostFile(path); err == nil {
		t.Fatal("LoadHostFile returned nil error, want duplicate name failure")
	}
}
