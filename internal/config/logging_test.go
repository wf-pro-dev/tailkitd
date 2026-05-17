package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func TestLoadLoggingConfigFromMissingFileUsesDefaults(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "missing.toml")

	cfg, err := loadLoggingConfigFrom(context.Background(), zap.NewNop(), path)
	if err != nil {
		t.Fatalf("loadLoggingConfigFrom returned error: %v", err)
	}

	if cfg.App.Level != "info" {
		t.Fatalf("App.Level = %q, want %q", cfg.App.Level, "info")
	}
	if cfg.App.Format != "text" {
		t.Fatalf("App.Format = %q, want %q", cfg.App.Format, "text")
	}
	if !cfg.API.Enabled {
		t.Fatal("API.Enabled = false, want true")
	}
	if cfg.API.Level != "info" {
		t.Fatalf("API.Level = %q, want %q", cfg.API.Level, "info")
	}
	if cfg.API.Format != "json" {
		t.Fatalf("API.Format = %q, want %q", cfg.API.Format, "json")
	}
	if cfg.API.Path != DefaultAPILogFile {
		t.Fatalf("API.Path = %q, want %q", cfg.API.Path, DefaultAPILogFile)
	}
	if cfg.API.Rotation.MaxSizeMB != 100 {
		t.Fatalf("API.Rotation.MaxSizeMB = %d, want 100", cfg.API.Rotation.MaxSizeMB)
	}
	if cfg.API.Rotation.MaxBackups != 10 {
		t.Fatalf("API.Rotation.MaxBackups = %d, want 10", cfg.API.Rotation.MaxBackups)
	}
	if cfg.API.Rotation.MaxAgeDays != 14 {
		t.Fatalf("API.Rotation.MaxAgeDays = %d, want 14", cfg.API.Rotation.MaxAgeDays)
	}
	if !cfg.API.Rotation.Compress {
		t.Fatal("API.Rotation.Compress = false, want true")
	}
}

func TestLoadLoggingConfigFromFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "logging.toml")
	data := []byte(`
[app]
level = "debug"
format = "json"

[api]
enabled = true
level = "warn"
format = "json"
path = "/tmp/tailkitd-api.json.log"

[api.rotation]
max_size_mb = 32
max_backups = 4
max_age_days = 7
compress = false
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := loadLoggingConfigFrom(context.Background(), zap.NewNop(), path)
	if err != nil {
		t.Fatalf("loadLoggingConfigFrom returned error: %v", err)
	}

	if cfg.App.Level != "debug" {
		t.Fatalf("App.Level = %q, want %q", cfg.App.Level, "debug")
	}
	if cfg.App.Format != "json" {
		t.Fatalf("App.Format = %q, want %q", cfg.App.Format, "json")
	}
	if cfg.API.Level != "warn" {
		t.Fatalf("API.Level = %q, want %q", cfg.API.Level, "warn")
	}
	if cfg.API.Path != "/tmp/tailkitd-api.json.log" {
		t.Fatalf("API.Path = %q, want %q", cfg.API.Path, "/tmp/tailkitd-api.json.log")
	}
	if cfg.API.Rotation.MaxSizeMB != 32 {
		t.Fatalf("API.Rotation.MaxSizeMB = %d, want 32", cfg.API.Rotation.MaxSizeMB)
	}
	if cfg.API.Rotation.MaxBackups != 4 {
		t.Fatalf("API.Rotation.MaxBackups = %d, want 4", cfg.API.Rotation.MaxBackups)
	}
	if cfg.API.Rotation.MaxAgeDays != 7 {
		t.Fatalf("API.Rotation.MaxAgeDays = %d, want 7", cfg.API.Rotation.MaxAgeDays)
	}
	if cfg.API.Rotation.Compress {
		t.Fatal("API.Rotation.Compress = true, want false")
	}
}

func TestLoadLoggingConfigFromEnvOverridesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logging.toml")
	data := []byte(`
[app]
level = "info"
format = "text"

[api]
enabled = true
level = "info"
format = "json"
path = "/tmp/from-file.json.log"

[api.rotation]
max_size_mb = 10
max_backups = 1
max_age_days = 2
compress = false
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("TAILKITD_APP_LOG_LEVEL", "error")
	t.Setenv("TAILKITD_APP_LOG_FORMAT", "json")
	t.Setenv("TAILKITD_API_LOG_ENABLED", "false")
	t.Setenv("TAILKITD_API_LOG_LEVEL", "debug")
	t.Setenv("TAILKITD_API_LOG_PATH", "/tmp/from-env.json.log")
	t.Setenv("TAILKITD_API_LOG_MAX_SIZE_MB", "55")
	t.Setenv("TAILKITD_API_LOG_MAX_BACKUPS", "8")
	t.Setenv("TAILKITD_API_LOG_MAX_AGE_DAYS", "9")
	t.Setenv("TAILKITD_API_LOG_COMPRESS", "true")

	cfg, err := loadLoggingConfigFrom(context.Background(), zap.NewNop(), path)
	if err != nil {
		t.Fatalf("loadLoggingConfigFrom returned error: %v", err)
	}

	if cfg.App.Level != "error" {
		t.Fatalf("App.Level = %q, want %q", cfg.App.Level, "error")
	}
	if cfg.App.Format != "json" {
		t.Fatalf("App.Format = %q, want %q", cfg.App.Format, "json")
	}
	if cfg.API.Enabled {
		t.Fatal("API.Enabled = true, want false")
	}
	if cfg.API.Level != "debug" {
		t.Fatalf("API.Level = %q, want %q", cfg.API.Level, "debug")
	}
	if cfg.API.Path != "/tmp/from-env.json.log" {
		t.Fatalf("API.Path = %q, want %q", cfg.API.Path, "/tmp/from-env.json.log")
	}
	if cfg.API.Rotation.MaxSizeMB != 55 {
		t.Fatalf("API.Rotation.MaxSizeMB = %d, want 55", cfg.API.Rotation.MaxSizeMB)
	}
	if cfg.API.Rotation.MaxBackups != 8 {
		t.Fatalf("API.Rotation.MaxBackups = %d, want 8", cfg.API.Rotation.MaxBackups)
	}
	if cfg.API.Rotation.MaxAgeDays != 9 {
		t.Fatalf("API.Rotation.MaxAgeDays = %d, want 9", cfg.API.Rotation.MaxAgeDays)
	}
	if !cfg.API.Rotation.Compress {
		t.Fatal("API.Rotation.Compress = false, want true")
	}
}

func TestLoadLoggingConfigFromLegacyEnvAlias(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logging.toml")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("TAILKITD_LOG_LEVEL", "warn")

	cfg, err := loadLoggingConfigFrom(context.Background(), zap.NewNop(), path)
	if err != nil {
		t.Fatalf("loadLoggingConfigFrom returned error: %v", err)
	}
	if cfg.App.Level != "warn" {
		t.Fatalf("App.Level = %q, want %q", cfg.App.Level, "warn")
	}
}

func TestLoadLoggingConfigFromLegacyEnvAliasDoesNotOverrideExplicitAppLevel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logging.toml")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("TAILKITD_LOG_LEVEL", "warn")
	t.Setenv("TAILKITD_APP_LOG_LEVEL", "debug")

	cfg, err := loadLoggingConfigFrom(context.Background(), zap.NewNop(), path)
	if err != nil {
		t.Fatalf("loadLoggingConfigFrom returned error: %v", err)
	}
	if cfg.App.Level != "debug" {
		t.Fatalf("App.Level = %q, want %q", cfg.App.Level, "debug")
	}
}

func TestLoadLoggingConfigFromRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
	}{
		{
			name: "invalid app level",
			data: `
[app]
level = "verbose"
format = "text"
`,
		},
		{
			name: "invalid api format",
			data: `
[api]
enabled = true
level = "info"
format = "text"
path = "/tmp/api.json.log"
`,
		},
		{
			name: "relative api path when enabled",
			data: `
[api]
enabled = true
level = "info"
format = "json"
path = "api.json.log"
`,
		},
		{
			name: "non-positive max size",
			data: `
[api]
enabled = true
level = "info"
format = "json"
path = "/tmp/api.json.log"

[api.rotation]
max_size_mb = 0
`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "logging.toml")
			if err := os.WriteFile(path, []byte(tt.data), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}

			if _, err := loadLoggingConfigFrom(context.Background(), zap.NewNop(), path); err == nil {
				t.Fatal("loadLoggingConfigFrom returned nil error, want validation failure")
			}
		})
	}
}
