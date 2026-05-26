package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestValidateRejectsWrongOneOfFields(t *testing.T) {
	t.Parallel()

	cfg := OutsiderServiceConfig{
		Name:        "nginx",
		Runtime:     "systemd",
		SystemdUnit: "nginx.service",
		BinaryPath:  "/usr/sbin/nginx",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate returned nil error, want oneof failure")
	}
}

func TestLoadDirSkipsInvalidFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good.toml"), []byte(`
name = "nginx"
runtime = "systemd"
systemd_unit = "nginx.service"
`), 0o644); err != nil {
		t.Fatalf("write good file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.toml"), []byte(`
name = "bad"
runtime = "binary"
systemd_unit = "oops.service"
`), 0o644); err != nil {
		t.Fatalf("write bad file: %v", err)
	}

	loaded, err := LoadDir(dir, zap.NewNop())
	if err != nil {
		t.Fatalf("LoadDir returned error: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("len(loaded) = %d, want 1", len(loaded))
	}
	if _, ok := loaded["nginx"]; !ok {
		t.Fatal("loaded missing nginx service")
	}
}

func TestRegistryReloadsOnFileChange(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reg, err := NewRegistry(ctx, dir, zap.NewNop())
	if err != nil {
		t.Fatalf("NewRegistry returned error: %v", err)
	}
	defer reg.Close()

	if err := os.WriteFile(filepath.Join(dir, "nginx.toml"), []byte(`
name = "nginx"
runtime = "systemd"
systemd_unit = "nginx.service"
`), 0o644); err != nil {
		t.Fatalf("write service file: %v", err)
	}

	waitForService(t, reg, "nginx")
}

func waitForService(t *testing.T, reg *Registry, want string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		services := reg.GetServices()
		if _, ok := services[want]; ok {
			return
		}
		time.Sleep(registryWatchPollPeriod)
	}

	t.Fatalf("service %q not found after watcher reload", want)
}
