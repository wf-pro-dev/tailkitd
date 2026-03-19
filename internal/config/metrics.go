package config

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
	"go.uber.org/zap"
)

const MetricsConfigPath = "/etc/tailkitd/integrations/metrics.toml"

const (
	defaultProcessLimit = 20
	maxProcessLimit     = 100
)

// MetricsConfig is the parsed and validated representation of metrics.toml.
//
// Each sub-section maps to one metrics endpoint group. Sections are
// independent — enabling disk does not require enabling cpu, and so on.
type MetricsConfig struct {
	Enabled   bool
	Host      HostMetricsConfig    `toml:"host"`
	CPU       CPUMetricsConfig     `toml:"cpu"`
	Memory    MemoryMetricsConfig  `toml:"memory"`
	Disk      DiskMetricsConfig    `toml:"disk"`
	Network   NetworkMetricsConfig `toml:"network"`
	Processes ProcessMetricsConfig `toml:"processes"`
}

// HostMetricsConfig controls GET /integrations/metrics/host.
type HostMetricsConfig struct {
	Enabled bool `toml:"enabled"`
}

// CPUMetricsConfig controls GET /integrations/metrics/cpu.
type CPUMetricsConfig struct {
	Enabled bool `toml:"enabled"`
}

// MemoryMetricsConfig controls GET /integrations/metrics/memory.
type MemoryMetricsConfig struct {
	Enabled bool `toml:"enabled"`
}

// DiskMetricsConfig controls GET /integrations/metrics/disk.
type DiskMetricsConfig struct {
	Enabled bool `toml:"enabled"`

	// Paths restricts disk stats to specific mount points.
	// All entries must be absolute paths.
	// If empty, all mounted filesystems are reported.
	Paths []string `toml:"paths"`
}

// NetworkMetricsConfig controls GET /integrations/metrics/network.
type NetworkMetricsConfig struct {
	Enabled bool `toml:"enabled"`

	// Interfaces restricts stats to specific network interfaces by name.
	// If empty, all interfaces are reported.
	Interfaces []string `toml:"interfaces"`
}

// ProcessMetricsConfig controls GET /integrations/metrics/processes.
type ProcessMetricsConfig struct {
	Enabled bool `toml:"enabled"`

	// Limit caps the number of processes returned, sorted by CPU usage desc.
	// Must be a positive integer, maximum 100.
	// Uses a pointer so we can distinguish "omitted" (nil → default 20)
	// from "explicitly set to 0" (→ validation error).
	Limit *int `toml:"limit"`
}

// ProcessLimit returns the effective process limit.
// Safe to call on a zero MetricsConfig — returns the default.
func (c MetricsConfig) ProcessLimit() int {
	if c.Processes.Limit == nil {
		return defaultProcessLimit
	}
	return *c.Processes.Limit
}

// LoadMetricsConfig loads and validates metrics.toml from the default path.
//
// Missing file → Enabled=false, nil error (integration disabled, 503).
// Present but invalid → non-nil error (startup failure).
func LoadMetricsConfig(ctx context.Context, logger *zap.Logger) (MetricsConfig, error) {
	return loadMetricsConfigFrom(ctx, logger, MetricsConfigPath)
}

func loadMetricsConfigFrom(_ context.Context, logger *zap.Logger, path string) (MetricsConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Warn("config missing",
				zap.String("file", path),
				zap.String("effect", "metrics integration disabled"),
			)
			return MetricsConfig{}, nil
		}
		return MetricsConfig{}, fmt.Errorf("config: read %s: %w", path, err)
	}

	var cfg MetricsConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		logger.Error("config invalid", zap.String("file", path), zap.Error(err))
		return MetricsConfig{}, fmt.Errorf("config: parse %s: %w", path, err)
	}

	// Apply defaults before validation so validation sees the effective values.
	if cfg.Processes.Limit == nil {
		lim := defaultProcessLimit
		cfg.Processes.Limit = &lim
	}

	if err := validateMetricsConfig(cfg); err != nil {
		logger.Error("config invalid", zap.String("file", path), zap.Error(err))
		return MetricsConfig{}, fmt.Errorf("config: validate %s: %w", path, err)
	}

	cfg.Enabled = true
	logger.Info("config loaded", zap.String("file", path))
	return cfg, nil
}

func validateMetricsConfig(cfg MetricsConfig) error {
	for i, p := range cfg.Disk.Paths {
		if !strings.HasPrefix(p, "/") {
			return fmt.Errorf("disk.paths[%d] %q must be an absolute path", i, p)
		}
	}

	limit := *cfg.Processes.Limit
	if limit <= 0 {
		return fmt.Errorf("processes.limit must be a positive integer, got %d", limit)
	}
	if limit > maxProcessLimit {
		return fmt.Errorf("processes.limit must be ≤ %d, got %d", maxProcessLimit, limit)
	}
	return nil
}
