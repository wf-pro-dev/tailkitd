package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestLoadHostConfigRejectsUnknownField(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "host.toml")
	data := []byte(`
name = "node-1"
region = "us-east-1"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := LoadHostConfig(path)
	if err == nil {
		t.Fatal("LoadHostConfig returned nil error, want unknown key failure")
	}
}

func TestEnsureHostConfigCreatesDefaults(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "host.toml")
	cfg, err := EnsureHostConfig(path, "ts-host")
	if err != nil {
		t.Fatalf("EnsureHostConfig returned error: %v", err)
	}

	if cfg.Name != "ts-host" {
		t.Fatalf("Name = %q, want %q", cfg.Name, "ts-host")
	}
	if cfg.Role != "unclassified" {
		t.Fatalf("Role = %q, want %q", cfg.Role, "unclassified")
	}
	if cfg.Environment != "default" {
		t.Fatalf("Environment = %q, want %q", cfg.Environment, "default")
	}
}

func TestEnsureHostConfigAppliesDefaultsToExistingFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "host.toml")
	data := []byte(`
name = "custom"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := EnsureHostConfig(path, "ts-host")
	if err != nil {
		t.Fatalf("EnsureHostConfig returned error: %v", err)
	}

	if cfg.Name != "custom" {
		t.Fatalf("Name = %q, want %q", cfg.Name, "custom")
	}
	if cfg.Role != "unclassified" {
		t.Fatalf("Role = %q, want %q", cfg.Role, "unclassified")
	}
}

func TestHostManagerReloadKeepsLastGoodOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.toml")
	if err := os.WriteFile(path, []byte(`name = "node-a"`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr, err := NewHostManager(ctx, path, "ts-host", zap.NewNop())
	if err != nil {
		t.Fatalf("NewHostManager returned error: %v", err)
	}
	defer mgr.Close()

	if err := os.WriteFile(path, []byte(`name = "node-b"`), 0o644); err != nil {
		t.Fatalf("write valid config: %v", err)
	}
	waitForHostName(t, mgr, "node-b")

	if err := os.WriteFile(path, []byte("name = \"node-c\"\nregion = \"x\"\n"), 0o644); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}

	time.Sleep(2 * hostReloadDebounce)

	got := mgr.Get()
	if got.Name != "node-b" {
		t.Fatalf("Name after invalid reload = %q, want %q", got.Name, "node-b")
	}
}

func waitForHostName(t *testing.T, mgr *HostManager, want string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := mgr.Get(); got != nil && got.Name == want {
			return
		}
		time.Sleep(hostWatchPollInterval)
	}

	got := mgr.Get()
	if got == nil {
		t.Fatalf("host manager config is nil, want name %q", want)
	}
	t.Fatalf("host manager name = %q, want %q", got.Name, want)
}
