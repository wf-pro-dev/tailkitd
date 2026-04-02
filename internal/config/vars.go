package config

import (
	"context"
	"fmt"
	"os"
	"regexp"

	"github.com/BurntSushi/toml"
	"go.uber.org/zap"

	types "github.com/wf-pro-dev/tailkit/types/integrations"
)

const VarsConfigPath = "/etc/tailkitd/integrations/vars.toml"

var projectNameRE = regexp.MustCompile(`^[a-z0-9_-]+$`)

// validVarOps is the closed set of permitted values for
// allow in each [[scope]] entry of vars.toml.
var validVarOps = map[string]bool{
	"read":  true,
	"write": true,
}

// VarsConfig is the parsed and validated representation of vars.toml.
type VarsConfig types.VarsConfig
type VarScope types.VarScope

// LoadVarsConfig loads and validates vars.toml from the default path.
//
// Missing file → Enabled=false, nil error (integration disabled, 503).
// Present but invalid → non-nil error (startup failure).
func LoadVarsConfig(ctx context.Context, logger *zap.Logger) (VarsConfig, error) {
	return loadVarsConfigFrom(ctx, logger, VarsConfigPath)
}

func loadVarsConfigFrom(_ context.Context, logger *zap.Logger, path string) (VarsConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Warn("config missing",
				zap.String("file", path),
				zap.String("effect", "vars integration disabled"),
			)
			return VarsConfig{}, nil
		}
		return VarsConfig{}, fmt.Errorf("config: read %s: %w", path, err)
	}

	var cfg VarsConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		logger.Error("config invalid", zap.String("file", path), zap.Error(err))
		return VarsConfig{}, fmt.Errorf("config: parse %s: %w", path, err)
	}

	if err := validateVarsConfig(cfg); err != nil {
		logger.Error("config invalid", zap.String("file", path), zap.Error(err))
		return VarsConfig{}, fmt.Errorf("config: validate %s: %w", path, err)
	}

	cfg.Enabled = true
	logger.Info("config loaded", zap.String("file", path))
	return cfg, nil
}

func validateVarsConfig(cfg VarsConfig) error {
	if len(cfg.Scopes) == 0 {
		return fmt.Errorf("at least one [[scope]] entry is required")
	}

	seen := make(map[string]bool)
	for i, s := range cfg.Scopes {
		if s.Project == "" {
			return fmt.Errorf("scope[%d]: project must not be empty", i)
		}
		if !projectNameRE.MatchString(s.Project) {
			return fmt.Errorf("scope[%d]: project %q must match ^[a-z0-9_-]+$", i, s.Project)
		}
		if s.Env == "" {
			return fmt.Errorf("scope[%d]: env must not be empty", i)
		}
		if !projectNameRE.MatchString(s.Env) {
			return fmt.Errorf("scope[%d]: env %q must match ^[a-z0-9_-]+$", i, s.Env)
		}

		// Allow must be non-empty and contain only valid values.
		if len(s.Allow) == 0 {
			return fmt.Errorf("scope[%d] %q/%q: allow must not be empty; valid values are: read, write",
				i, s.Project, s.Env)
		}
		if err := validateAllowList(fmt.Sprintf("scope[%d]", i), s.Allow, validVarOps); err != nil {
			return err
		}

		// Duplicate project/env pairs would cause silent shadowing.
		key := s.Project + "/" + s.Env
		if seen[key] {
			return fmt.Errorf("scope[%d]: duplicate project/env pair %q", i, key)
		}
		seen[key] = true
	}
	return nil
}
