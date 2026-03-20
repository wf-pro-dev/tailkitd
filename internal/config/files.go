package config

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
	"go.uber.org/zap"
)

const FilesConfigPath = "/etc/tailkitd/integrations/files.toml"

// validFileOps is the closed set of permitted values for
// allow in each [[path]] entry of files.toml.
var validFileOps = map[string]bool{
	"read":  true,
	"write": true,
}

// FilesConfig is the parsed and validated representation of files.toml.
type FilesConfig struct {
	Enabled bool
	Paths   []PathRule `toml:"path"`
}

// PathRule defines access permissions for a single directory.
//
// Dir must be an absolute path ending with "/".
// Allow contains the permitted operations for that directory.
// PostRecv lists exec-registry commands to run after a successful write;
// validated against the exec registry after tools are loaded.
type PathRule struct {
	// Dir is the directory this rule applies to.
	// Must be an absolute path ending with "/".
	Dir string `toml:"dir"`

	// Allow is the list of permitted operations for this directory.
	// Valid values: "read", "write".
	Allow []string `toml:"allow"`
}

// Permits returns true if op ("read" or "write") is in the allow list.
func (r PathRule) Permits(op string) bool {
	for _, a := range r.Allow {
		if a == op {
			return true
		}
	}
	return false
}

// FindPath returns the PathRule whose Dir matches the given directory path,
// and a bool indicating whether a match was found.
// Callers use this to resolve a requested path to its rule before checking Permits.
func (c FilesConfig) FindPath(dir string) (PathRule, bool) {
	for _, r := range c.Paths {
		if r.Dir == dir {
			return r, true
		}
	}
	return PathRule{}, false
}

// LoadFilesConfig loads and validates files.toml from the default path.
//
// Missing file → Enabled=false, nil error (integration disabled, 503).
// Present but invalid → non-nil error (startup failure).
func LoadFilesConfig(ctx context.Context, logger *zap.Logger) (FilesConfig, error) {
	return loadFilesConfigFrom(ctx, logger, FilesConfigPath)
}

func loadFilesConfigFrom(_ context.Context, logger *zap.Logger, path string) (FilesConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Warn("config missing",
				zap.String("file", path),
				zap.String("effect", "files integration disabled"),
			)
			return FilesConfig{}, nil
		}
		return FilesConfig{}, fmt.Errorf("config: read %s: %w", path, err)
	}

	var cfg FilesConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		logger.Error("config invalid", zap.String("file", path), zap.Error(err))
		return FilesConfig{}, fmt.Errorf("config: parse %s: %w", path, err)
	}

	if err := validateFilesConfig(cfg); err != nil {
		logger.Error("config invalid", zap.String("file", path), zap.Error(err))
		return FilesConfig{}, fmt.Errorf("config: validate %s: %w", path, err)
	}

	cfg.Enabled = true
	logger.Info("config loaded", zap.String("file", path))
	return cfg, nil
}

func validateFilesConfig(cfg FilesConfig) error {
	if len(cfg.Paths) == 0 {
		return fmt.Errorf("at least one [[path]] entry is required")
	}

	seen := make(map[string]bool)
	for i, r := range cfg.Paths {
		// Dir must be an absolute path ending with "/".
		if err := validateDirPath(r.Dir); err != nil {
			return fmt.Errorf("path[%d].dir %q: %w", i, r.Dir, err)
		}

		// Duplicate dirs are a config error — the second rule would be silently ignored.
		if seen[r.Dir] {
			return fmt.Errorf("path[%d].dir %q: duplicate entry", i, r.Dir)
		}
		seen[r.Dir] = true

		// Allow must be non-empty and contain only valid values.
		if len(r.Allow) == 0 {
			return fmt.Errorf("path[%d].dir %q: allow must not be empty; valid values are: read, write", i, r.Dir)
		}
		if err := validateAllowList(fmt.Sprintf("path[%d]", i), r.Allow, validFileOps); err != nil {
			return err
		}

	}
	return nil
}

func validateDirPath(p string) error {
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("must be an absolute path")
	}
	if !strings.HasSuffix(p, "/") {
		return fmt.Errorf("must end with /")
	}
	return nil
}
