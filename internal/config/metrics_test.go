package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func TestLoadMetricsConfigFromAppliesDefaultsAndPortsSection(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.toml")

	const content = `
[host]
enabled = true

[processes]
enabled = true

[ports]
enabled = true
`

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := loadMetricsConfigFrom(context.Background(), zap.NewNop(), path)
	if err != nil {
		t.Fatalf("loadMetricsConfigFrom() error = %v", err)
	}

	if !cfg.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if !cfg.Host.Enabled {
		t.Fatal("Host.Enabled = false, want true")
	}
	if !cfg.Processes.Enabled {
		t.Fatal("Processes.Enabled = false, want true")
	}
	if cfg.Processes.Limit == nil || *cfg.Processes.Limit != defaultProcessLimit {
		t.Fatalf("Processes.Limit = %v, want %d", cfg.Processes.Limit, defaultProcessLimit)
	}
	if !cfg.Ports.Enabled {
		t.Fatal("Ports.Enabled = false, want true")
	}
}

func TestValidateMetricsConfigRejectsInvalidProcessLimit(t *testing.T) {
	t.Parallel()

	limit := 0
	cfg := MetricsConfig{
		Processes: ProcessMetricsConfig{
			Enabled: true,
			Limit:   &limit,
		},
	}

	if err := validateMetricsConfig(cfg); err == nil {
		t.Fatal("validateMetricsConfig() error = nil, want non-nil")
	}
}
