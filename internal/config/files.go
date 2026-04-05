package config

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	types "github.com/wf-pro-dev/tailkit/types/integrations"
	"go.uber.org/zap"
)

const FilesConfigPath = "/etc/tailkitd/integrations/files.toml"

// validFileOps is the closed set of permitted values for
// allow in each [[path]] entry of files.toml.
var validFileOps = map[string]bool{
	"read":  true,
	"write": true,
}

type PathRule types.PathRule
type ResolvedIdentity types.ResolvedIdentity

// LoadFilesConfig loads and validates files.toml from the default path.
//
// Missing file → Enabled=false, nil error (integration disabled, 503).
// Present but invalid → non-nil error (startup failure).
//
// write_as resolution is best-effort and never fatal:
//   - CAP_SETUID absent → warn, WriteAs.Set=false, writes proceed as daemon user.
//   - Username not found → warn, WriteAs.Set=false, writes proceed as daemon user.
func LoadFilesConfig(ctx context.Context, logger *zap.Logger) (types.FilesConfig, error) {
	return loadFilesConfigFrom(ctx, logger, FilesConfigPath)
}

func loadFilesConfigFrom(_ context.Context, logger *zap.Logger, path string) (types.FilesConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Warn("config missing",
				zap.String("file", path),
				zap.String("effect", "files integration disabled"),
			)
			return types.FilesConfig{}, nil
		}
		return types.FilesConfig{}, fmt.Errorf("config: read %s: %w", path, err)
	}

	var cfg types.FilesConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		logger.Error("config invalid", zap.String("file", path), zap.Error(err))
		return types.FilesConfig{}, fmt.Errorf("config: parse %s: %w", path, err)
	}

	if err := validateFilesConfig(cfg); err != nil {
		logger.Error("config invalid", zap.String("file", path), zap.Error(err))
		return types.FilesConfig{}, fmt.Errorf("config: validate %s: %w", path, err)
	}

	// Resolve write_as usernames to uid/gid.
	// CAP_SETUID is checked once; failure is degraded-but-functional:
	// writes succeed as the daemon user with a startup warning rather than
	// disabling the path rule or failing to start.

	for i := range cfg.Paths {
		rule := &cfg.Paths[i]
		if rule.UseAsUser == "" {
			continue
		}

		id, err := resolveUser(rule.UseAsUser)
		if err != nil {
			logger.Warn("files: write_as user not found — identity drop skipped, writing as daemon user",
				zap.String("dir", rule.Dir),
				zap.String("write_as", rule.UseAsUser),
				zap.Bool("share", rule.Share),
				zap.Error(err),
			)
			// WriteAs.Set remains false — atomicWrite uses the plain path.
			continue
		}
		rule.UseAs = types.ResolvedIdentity{UID: id.UID, GID: id.GID, Set: true}
		logger.Info("files: write_as resolved",
			zap.String("dir", rule.Dir),
			zap.String("user", rule.UseAsUser),
			zap.Int("uid", id.UID),
			zap.Int("gid", id.GID),
		)
	}

	cfg.Enabled = true
	logger.Info("config loaded", zap.String("file", path))
	return cfg, nil
}

// ─── Validation ───────────────────────────────────────────────────────────────

func validateFilesConfig(cfg types.FilesConfig) error {
	if len(cfg.Paths) == 0 {
		return fmt.Errorf("at least one [[path]] entry is required")
	}

	seen := make(map[string]bool)
	for i, r := range cfg.Paths {
		if err := validateDirPath(r.Dir); err != nil {
			return fmt.Errorf("path[%d].dir %q: %w", i, r.Dir, err)
		}
		if seen[r.Dir] {
			return fmt.Errorf("path[%d].dir %q: duplicate entry", i, r.Dir)
		}
		seen[r.Dir] = true

		if len(r.Allow) == 0 {
			return fmt.Errorf("path[%d].dir %q: allow must not be empty; valid values are: read, write", i, r.Dir)
		}
		if err := validateAllowList(fmt.Sprintf("path[%d]", i), r.Allow, validFileOps); err != nil {
			return err
		}
		// use_as is only meaningful on rules that include "write".
		if r.UseAsUser != "" && !containsWrite(r.Allow) {
			return fmt.Errorf("path[%d].dir %q: write_as requires \"write\" in allow", i, r.Dir)
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

// ─── write_as helpers ─────────────────────────────────────────────────────────

// resolveUser looks up a username and returns its uid and primary gid.
func resolveUser(username string) (ResolvedIdentity, error) {
	u, err := user.Lookup(username)
	if err != nil {
		return ResolvedIdentity{}, fmt.Errorf("user %q not found: %w", username, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return ResolvedIdentity{}, fmt.Errorf("invalid uid %q for user %q", u.Uid, username)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return ResolvedIdentity{}, fmt.Errorf("invalid gid %q for user %q", u.Gid, username)
	}
	return ResolvedIdentity{UID: uid, GID: gid, Set: true}, nil
}

func containsWrite(allow []string) bool {
	for _, a := range allow {
		if a == "write" {
			return true
		}
	}
	return false
}
