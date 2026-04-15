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
	Enabled   bool                 `json:"enabled"`
	Host      HostMetricsConfig    `json:"host" toml:"host"`
	CPU       CPUMetricsConfig     `json:"cpu" toml:"cpu"`
	Memory    MemoryMetricsConfig  `json:"memory" toml:"memory"`
	Disk      DiskMetricsConfig    `json:"disk" toml:"disk"`
	Network   NetworkMetricsConfig `json:"network" toml:"network"`
	Processes ProcessMetricsConfig `json:"processes" toml:"processes"`
	Ports     PortMetricsConfig    `json:"ports" toml:"ports"`
}

// HostMetricsConfig controls GET /integrations/metrics/host.
type HostMetricsConfig struct {
	Enabled bool `json:"enabled" toml:"enabled"`
}

// CPUMetricsConfig controls GET /integrations/metrics/cpu.
type CPUMetricsConfig struct {
	Enabled bool `json:"enabled" toml:"enabled"`
}

// MemoryMetricsConfig controls GET /integrations/metrics/memory.
type MemoryMetricsConfig struct {
	Enabled bool `json:"enabled" toml:"enabled"`
}

// DiskMetricsConfig controls GET /integrations/metrics/disk.
type DiskMetricsConfig struct {
	Enabled bool     `json:"enabled" toml:"enabled"`
	Paths   []string `json:"paths" toml:"paths"`
}

// NetworkMetricsConfig controls GET /integrations/metrics/network.
type NetworkMetricsConfig struct {
	Enabled    bool     `json:"enabled" toml:"enabled"`
	Interfaces []string `json:"interfaces" toml:"interfaces"`
}

// ProcessMetricsConfig controls GET /integrations/metrics/processes.
type ProcessMetricsConfig struct {
	Enabled bool `json:"enabled" toml:"enabled"`
	Limit   *int `json:"limit,omitempty" toml:"limit"`
}

// PortMetricsConfig controls the TCP listen port metrics endpoints.
type PortMetricsConfig struct {
	Enabled bool `json:"enabled" toml:"enabled"`
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
