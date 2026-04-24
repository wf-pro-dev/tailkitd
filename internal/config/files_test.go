package config

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"testing"

	types "github.com/wf-pro-dev/tailkit/types/integrations"
	"go.uber.org/zap"
)

func TestValidateFilesConfigRejectsWriteAsWithoutWrite(t *testing.T) {
	t.Parallel()

	cfg := types.FilesConfig{
		Paths: []types.PathRule{
			{
				Dir:       "/tmp/",
				Allow:     []string{"read"},
				UseAsUser: "nobody",
			},
		},
	}

	err := validateFilesConfig(cfg)
	if err == nil {
		t.Fatal("validateFilesConfig returned nil error, want validation failure")
	}
}

func TestLoadFilesConfigFromMissingFileDisablesIntegration(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "missing.toml")

	cfg, err := loadFilesConfigFrom(context.Background(), zap.NewNop(), path)
	if err != nil {
		t.Fatalf("loadFilesConfigFrom returned error: %v", err)
	}
	if cfg.Enabled {
		t.Fatalf("Enabled = true, want false")
	}
	if len(cfg.Paths) != 0 {
		t.Fatalf("len(Paths) = %d, want 0", len(cfg.Paths))
	}
}

func TestLoadFilesConfigFromResolvesExistingUser(t *testing.T) {
	t.Parallel()

	currentUser, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current: %v", err)
	}

	path := filepath.Join(t.TempDir(), "files.toml")
	data := []byte(`
[[path]]
dir = "/tmp/"
allow = ["write"]
use_as = "` + currentUser.Username + `"
share = true
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := loadFilesConfigFrom(context.Background(), zap.NewNop(), path)
	if err != nil {
		t.Fatalf("loadFilesConfigFrom returned error: %v", err)
	}

	if !cfg.Enabled {
		t.Fatalf("Enabled = false, want true")
	}
	if len(cfg.Paths) != 1 {
		t.Fatalf("len(Paths) = %d, want 1", len(cfg.Paths))
	}
	if cfg.Paths[0].UseAsUser != currentUser.Username {
		t.Fatalf("UseAsUser = %q, want %q", cfg.Paths[0].UseAsUser, currentUser.Username)
	}
	if !cfg.Paths[0].UseAs.Set {
		t.Fatal("UseAs.Set = false, want true")
	}
}
