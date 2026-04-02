package config

import (
	"context"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"go.uber.org/zap"

	types "github.com/wf-pro-dev/tailkit/types/integrations"
)

const SystemdConfigPath = "/etc/tailkitd/integrations/systemd.toml"

// validUnitOps is the closed set of permitted values for
// [units] allow in systemd.toml.
var validUnitOps = map[string]bool{
	"list":      true,
	"inspect":   true,
	"unit_file": true,
	"logs":      true,
	"start":     true,
	"stop":      true,
	"restart":   true,
	"reload":    true,
	"enable":    true,
	"disable":   true,
}

// validJournalPriorities is the closed set of permitted values for
// journal.priority in systemd.toml, ordered most-to-least severe.
var validJournalPriorities = map[string]bool{
	"emerg":   true,
	"alert":   true,
	"crit":    true,
	"err":     true,
	"warning": true,
	"notice":  true,
	"info":    true,
	"debug":   true,
}

const defaultJournalPriority = "info"
const defaultJournalLines = 100

// SystemdConfig is the parsed and validated representation of systemd.toml.
type SystemdConfig types.SystemdConfig

// UnitConfig controls which systemd unit operations are permitted.
type UnitConfig types.UnitConfig

// JournalConfig controls journal retrieval behaviour.
// It applies to both per-unit journal endpoints and the system-wide journal.
type JournalConfig struct {
	// Enabled gates the per-unit journal endpoint
	// (GET /integrations/systemd/units/{unit}/journal).
	Enabled bool `toml:"enabled"`

	// Priority is the minimum log severity to return.
	// Valid values: emerg, alert, crit, err, warning, notice, info, debug.
	// Defaults to "info" if omitted.
	Priority string `toml:"priority"`

	// Lines is the default number of journal lines returned per request.
	// Must be a positive integer. Defaults to 100 if omitted.
	Lines int `toml:"lines"`

	// SystemJournal permits GET /integrations/systemd/journal (system-wide).
	// Kept as a dedicated bool because it is a distinct endpoint, not an
	// operation variant of the per-unit journal.
	SystemJournal bool `toml:"system_journal"`
}

// LoadSystemdConfig loads and validates systemd.toml from the default path.
//
// Missing file → Enabled=false, nil error (integration disabled, 503).
// Present but invalid → non-nil error (startup failure).
func LoadSystemdConfig(ctx context.Context, logger *zap.Logger) (SystemdConfig, error) {
	return loadSystemdConfigFrom(ctx, logger, SystemdConfigPath)
}

func loadSystemdConfigFrom(_ context.Context, logger *zap.Logger, path string) (SystemdConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Warn("config missing",
				zap.String("file", path),
				zap.String("effect", "systemd integration disabled"),
			)
			return SystemdConfig{}, nil
		}
		return SystemdConfig{}, fmt.Errorf("config: read %s: %w", path, err)
	}

	var cfg SystemdConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		logger.Error("config invalid", zap.String("file", path), zap.Error(err))
		return SystemdConfig{}, fmt.Errorf("config: parse %s: %w", path, err)
	}

	// Apply defaults before validation so validation sees the effective values.
	if cfg.Journal.Priority == "" {
		cfg.Journal.Priority = defaultJournalPriority
	}
	if cfg.Journal.Lines == 0 {
		cfg.Journal.Lines = defaultJournalLines
	}

	if err := validateSystemdConfig(cfg); err != nil {
		logger.Error("config invalid", zap.String("file", path), zap.Error(err))
		return SystemdConfig{}, fmt.Errorf("config: validate %s: %w", path, err)
	}

	cfg.Enabled = true
	logger.Info("config loaded", zap.String("file", path))
	return cfg, nil
}

func validateSystemdConfig(cfg SystemdConfig) error {
	if err := validateAllowList("units", cfg.Units.Allow, validUnitOps); err != nil {
		return err
	}
	if !validJournalPriorities[cfg.Journal.Priority] {
		return fmt.Errorf("journal.priority %q is not valid; valid values are: %s",
			cfg.Journal.Priority, joinKeys(validJournalPriorities))
	}
	if cfg.Journal.Lines <= 0 {
		return fmt.Errorf("journal.lines must be a positive integer, got %d", cfg.Journal.Lines)
	}
	return nil
}
