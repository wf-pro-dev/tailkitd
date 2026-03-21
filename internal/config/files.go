package config

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"strconv"
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
// WriteAsUser is the optional username to drop to when writing files.
// WriteAs is the resolved identity, populated at load time.
//
// When write_as is set but cannot be honoured (CAP_SETUID absent or username
// not found), a warning is logged at startup and the write proceeds as the
// daemon user — the write is NOT disabled.
type PathRule struct {
	// Dir is the directory this rule applies to.
	// Must be an absolute path ending with "/".
	Dir string `toml:"dir"`

	// Allow is the list of permitted operations for this directory.
	// Valid values: "read", "write".
	Allow []string `toml:"allow"`

	// WriteAsUser is the username to drop to when writing files to this path.
	// Requires the daemon to hold CAP_SETUID (granted by AmbientCapabilities
	// in the systemd unit). If absent, writes succeed as the daemon user.
	// Resolved to WriteAs at load time via os/user.Lookup.
	WriteAsUser string `toml:"write_as"`

	// WriteAs is the resolved identity for WriteAsUser.
	// Zero value (Set=false) means no privilege drop — write as daemon user.
	// Populated by LoadFilesConfig; never set directly by callers.
	WriteAs ResolvedIdentity `toml:"-"`
}

// ResolvedIdentity holds a uid/gid resolved from a username at startup.
type ResolvedIdentity struct {
	UID int
	GID int
	Set bool // true when a write_as user was successfully resolved
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
//
// write_as resolution is best-effort and never fatal:
//   - CAP_SETUID absent → warn, WriteAs.Set=false, writes proceed as daemon user.
//   - Username not found → warn, WriteAs.Set=false, writes proceed as daemon user.
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

	// Resolve write_as usernames to uid/gid.
	// CAP_SETUID is checked once; failure is degraded-but-functional:
	// writes succeed as the daemon user with a startup warning rather than
	// disabling the path rule or failing to start.

	for i := range cfg.Paths {
		rule := &cfg.Paths[i]
		if rule.WriteAsUser == "" {
			continue
		}

		id, err := resolveUser(rule.WriteAsUser)
		if err != nil {
			logger.Warn("files: write_as user not found — identity drop skipped, writing as daemon user",
				zap.String("dir", rule.Dir),
				zap.String("write_as", rule.WriteAsUser),
				zap.Error(err),
			)
			// WriteAs.Set remains false — atomicWrite uses the plain path.
			continue
		}
		rule.WriteAs = id
		logger.Info("files: write_as resolved",
			zap.String("dir", rule.Dir),
			zap.String("user", rule.WriteAsUser),
			zap.Int("uid", id.UID),
			zap.Int("gid", id.GID),
		)
	}

	cfg.Enabled = true
	logger.Info("config loaded", zap.String("file", path))
	return cfg, nil
}

// ─── Validation ───────────────────────────────────────────────────────────────

func validateFilesConfig(cfg FilesConfig) error {
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
		// write_as is only meaningful on rules that include "write".
		if r.WriteAsUser != "" && !containsWrite(r.Allow) {
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
