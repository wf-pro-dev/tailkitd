package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"go.uber.org/zap"
)

const LoggingConfigPath = "/etc/tailkitd/logging.toml"
const DefaultAPILogFile = "/var/log/tailkitd/api.json.log"

var validLogLevels = map[string]bool{
	"debug": true,
	"info":  true,
	"warn":  true,
	"error": true,
}

var validAppLogFormats = map[string]bool{
	"text": true,
	"json": true,
}

type LoggingConfig struct {
	App AppLogConfig `toml:"app"`
	API APILogConfig `toml:"api"`
}

type AppLogConfig struct {
	Level  string `toml:"level"`
	Format string `toml:"format"`
}

type APILogConfig struct {
	Enabled  bool              `toml:"enabled"`
	Level    string            `toml:"level"`
	Format   string            `toml:"format"`
	Path     string            `toml:"path"`
	Rotation APIRotationConfig `toml:"rotation"`
}

type APIRotationConfig struct {
	MaxSizeMB  int  `toml:"max_size_mb"`
	MaxBackups int  `toml:"max_backups"`
	MaxAgeDays int  `toml:"max_age_days"`
	Compress   bool `toml:"compress"`
}

func LoadLoggingConfig(ctx context.Context, logger *zap.Logger) (LoggingConfig, error) {
	return loadLoggingConfigFrom(ctx, logger, LoggingConfigPath)
}

func loadLoggingConfigFrom(_ context.Context, logger *zap.Logger, path string) (LoggingConfig, error) {
	cfg := defaultLoggingConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Debug("config missing",
				zap.String("file", path),
				zap.String("effect", "logging defaults active"),
			)
		} else {
			return LoggingConfig{}, fmt.Errorf("config: read %s: %w", path, err)
		}
	} else {
		md, err := toml.Decode(string(data), &cfg)
		if err != nil {
			logger.Error("config invalid", zap.String("file", path), zap.Error(err))
			return LoggingConfig{}, fmt.Errorf("config: parse %s: %w", path, err)
		}
		for _, key := range md.Undecoded() {
			logger.Warn("unknown config key",
				zap.String("file", path),
				zap.String("key", key.String()),
				zap.String("hint", "check for typos"),
			)
		}
		logger.Debug("config loaded", zap.String("file", path))
	}

	if err := applyLoggingEnvOverrides(&cfg); err != nil {
		logger.Error("config invalid", zap.String("file", path), zap.Error(err))
		return LoggingConfig{}, fmt.Errorf("config: env override %s: %w", path, err)
	}
	if err := validateLoggingConfig(cfg); err != nil {
		logger.Error("config invalid", zap.String("file", path), zap.Error(err))
		return LoggingConfig{}, fmt.Errorf("config: validate %s: %w", path, err)
	}

	return cfg, nil
}

func defaultLoggingConfig() LoggingConfig {
	return LoggingConfig{
		App: AppLogConfig{
			Level:  "info",
			Format: "text",
		},
		API: APILogConfig{
			Enabled: true,
			Level:   "info",
			Format:  "json",
			Path:    DefaultAPILogFile,
			Rotation: APIRotationConfig{
				MaxSizeMB:  100,
				MaxBackups: 10,
				MaxAgeDays: 14,
				Compress:   true,
			},
		},
	}
}

func applyLoggingEnvOverrides(cfg *LoggingConfig) error {
	overrideString(&cfg.App.Level, "TAILKITD_APP_LOG_LEVEL")
	overrideString(&cfg.App.Format, "TAILKITD_APP_LOG_FORMAT")
	if err := overrideBool(&cfg.API.Enabled, "TAILKITD_API_LOG_ENABLED"); err != nil {
		return err
	}
	overrideString(&cfg.API.Level, "TAILKITD_API_LOG_LEVEL")
	overrideString(&cfg.API.Path, "TAILKITD_API_LOG_PATH")
	if err := overrideInt(&cfg.API.Rotation.MaxSizeMB, "TAILKITD_API_LOG_MAX_SIZE_MB"); err != nil {
		return err
	}
	if err := overrideInt(&cfg.API.Rotation.MaxBackups, "TAILKITD_API_LOG_MAX_BACKUPS"); err != nil {
		return err
	}
	if err := overrideInt(&cfg.API.Rotation.MaxAgeDays, "TAILKITD_API_LOG_MAX_AGE_DAYS"); err != nil {
		return err
	}
	if err := overrideBool(&cfg.API.Rotation.Compress, "TAILKITD_API_LOG_COMPRESS"); err != nil {
		return err
	}

	if raw := strings.TrimSpace(os.Getenv("TAILKITD_LOG_LEVEL")); raw != "" &&
		strings.TrimSpace(os.Getenv("TAILKITD_APP_LOG_LEVEL")) == "" {
		cfg.App.Level = raw
	}
	return nil
}

func validateLoggingConfig(cfg LoggingConfig) error {
	if !validLogLevels[cfg.App.Level] {
		return fmt.Errorf("[app] level %q is not valid; valid values are: %s", cfg.App.Level, joinKeys(validLogLevels))
	}
	if !validAppLogFormats[cfg.App.Format] {
		return fmt.Errorf("[app] format %q is not valid; valid values are: %s", cfg.App.Format, joinKeys(validAppLogFormats))
	}
	if !validLogLevels[cfg.API.Level] {
		return fmt.Errorf("[api] level %q is not valid; valid values are: %s", cfg.API.Level, joinKeys(validLogLevels))
	}
	if cfg.API.Format != "json" {
		return fmt.Errorf("[api] format %q is not valid; valid values are: json", cfg.API.Format)
	}
	if cfg.API.Enabled && !filepath.IsAbs(cfg.API.Path) {
		return fmt.Errorf("[api] path %q must be an absolute path", cfg.API.Path)
	}
	if cfg.API.Rotation.MaxSizeMB <= 0 {
		return fmt.Errorf("[api.rotation] max_size_mb must be a positive integer, got %d", cfg.API.Rotation.MaxSizeMB)
	}
	if cfg.API.Rotation.MaxBackups < 0 {
		return fmt.Errorf("[api.rotation] max_backups must be >= 0, got %d", cfg.API.Rotation.MaxBackups)
	}
	if cfg.API.Rotation.MaxAgeDays < 0 {
		return fmt.Errorf("[api.rotation] max_age_days must be >= 0, got %d", cfg.API.Rotation.MaxAgeDays)
	}
	return nil
}

func overrideString(dst *string, key string) {
	if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
		*dst = raw
	}
}

func overrideBool(dst *bool, key string) error {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fmt.Errorf("%s: parse bool %q: %w", key, raw, err)
	}
	*dst = value
	return nil
}

func overrideInt(dst *int, key string) error {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("%s: parse int %q: %w", key, raw, err)
	}
	*dst = value
	return nil
}
