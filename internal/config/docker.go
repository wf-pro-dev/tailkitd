package config

import (
	"context"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"go.uber.org/zap"
)

const DockerConfigPath = "/etc/tailkitd/integrations/docker.toml"

// validContainerOps is the closed set of permitted values for
// [containers] allow in docker.toml.
var validContainerOps = map[string]bool{
	"list":    true,
	"inspect": true,
	"logs":    true,
	"stats":   true,
	"start":   true,
	"stop":    true,
	"restart": true,
	"remove":  true,
}

// validImageOps is the closed set of permitted values for
// [images] allow in docker.toml.
var validImageOps = map[string]bool{
	"list": true,
	"pull": true,
}

// validComposeOps is the closed set of permitted values for
// [compose] allow in docker.toml.
var validComposeOps = map[string]bool{
	"list":    true,
	"up":      true,
	"down":    true,
	"pull":    true,
	"restart": true,
	"build":   true,
}

// validSwarmOps is the closed set of permitted values for
// [swarm] allow in docker.toml.
var validSwarmOps = map[string]bool{
	"read":  true,
	"write": true,
}

// DockerConfig is the parsed and validated representation of docker.toml.
// Enabled is set to true only after a successful load — absent file means
// the docker integration is disabled (503), not an error.
type DockerConfig struct {
	Enabled    bool
	Containers DockerSectionConfig `toml:"containers"`
	Images     DockerSectionConfig `toml:"images"`
	Compose    DockerSectionConfig `toml:"compose"`
	Swarm      DockerSectionConfig `toml:"swarm"`
}

// DockerSectionConfig is the common shape for every docker.toml section.
// Enabled gates the entire section. Allow is the set of permitted operations
// within that section — validated at load time against the section's closed
// set of valid values.
type DockerSectionConfig struct {
	// Enabled gates all operations in this section.
	// If false, all endpoints in the section return 403 regardless of Allow.
	Enabled bool `toml:"enabled"`

	// Allow is the list of permitted operations within this section.
	// Valid values differ per section and are validated at startup.
	// An unknown value causes a fatal config error with the valid set listed.
	Allow []string `toml:"allow"`
}

// Permits returns true if op is both in the allow list and the section
// is enabled. Callers use this instead of inspecting Allow directly.
func (s DockerSectionConfig) Permits(op string) bool {
	if !s.Enabled {
		return false
	}
	for _, a := range s.Allow {
		if a == op {
			return true
		}
	}
	return false
}

// LoadDockerConfig loads and validates docker.toml from the default path.
//
// Missing file → Enabled=false, nil error (integration disabled, 503).
// Present but invalid → non-nil error (startup failure).
func LoadDockerConfig(ctx context.Context, logger *zap.Logger) (DockerConfig, error) {
	return loadDockerConfigFrom(ctx, logger, DockerConfigPath)
}

func loadDockerConfigFrom(_ context.Context, logger *zap.Logger, path string) (DockerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Warn("config missing",
				zap.String("file", path),
				zap.String("effect", "docker integration disabled"),
			)
			return DockerConfig{}, nil
		}
		return DockerConfig{}, fmt.Errorf("config: read %s: %w", path, err)
	}

	var cfg DockerConfig
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		logger.Error("config invalid", zap.String("file", path), zap.Error(err))
		return DockerConfig{}, fmt.Errorf("config: parse %s: %w", path, err)
	}

	// Warn on unknown keys — almost always a typo.
	for _, key := range md.Undecoded() {
		logger.Warn("unknown config key",
			zap.String("file", path),
			zap.String("key", key.String()),
			zap.String("hint", "check for typos"),
		)
	}

	if err := validateDockerConfig(cfg); err != nil {
		logger.Error("config invalid", zap.String("file", path), zap.Error(err))
		return DockerConfig{}, fmt.Errorf("config: validate %s: %w", path, err)
	}

	cfg.Enabled = true
	logger.Info("config loaded", zap.String("file", path))
	return cfg, nil
}

func validateDockerConfig(cfg DockerConfig) error {
	if err := validateAllowList("containers", cfg.Containers.Allow, validContainerOps); err != nil {
		return err
	}
	if err := validateAllowList("images", cfg.Images.Allow, validImageOps); err != nil {
		return err
	}
	if err := validateAllowList("compose", cfg.Compose.Allow, validComposeOps); err != nil {
		return err
	}
	if err := validateAllowList("swarm", cfg.Swarm.Allow, validSwarmOps); err != nil {
		return err
	}
	return nil
}
