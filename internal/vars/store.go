// Package vars implements the tailkitd var store: a per-scope, concurrency-safe
// key-value store persisted as JSON files under /etc/tailkitd/vars/.
//
// Scope identity is "project/env". Each scope has its own *sync.RWMutex so
// concurrent reads across different scopes never block each other, and writes
// within a scope are serialised without blocking other scopes.
//
// Invariants:
//   - Keys starting with "_" are reserved for internal metadata and are never
//     returned to callers. Set rejects them with an error.
//   - _meta.updated_at and _meta.updated_by are written on every Set/Delete.
//   - All writes are atomic: temp file in same directory → rename.
package vars

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	defaultBaseDir = "/etc/tailkitd/vars"
	// keyPattern allows alphanumeric, underscores, hyphens, and dots.
	// Keys starting with "_" are rejected separately (reserved for _meta.*).
	keyPatternStr = `^[a-zA-Z][a-zA-Z0-9_./-]*$`
)

var keyPattern = regexp.MustCompile(keyPatternStr)

// scopeData is the on-disk JSON structure for one project/env scope.
// All keys (including _meta.*) live in a flat map.
type scopeData map[string]string

// Store is the var store. It holds per-scope mutexes in a sync.Map and
// persists data as JSON files on disk.
type Store struct {
	baseDir string
	// mu is a sync.Map of *sync.RWMutex keyed by "project/env".
	// Each scope gets its own mutex; different scopes never contend.
	mu     sync.Map
	logger *zap.Logger
}

// NewStore constructs a Store rooted at baseDir.
// Call with defaultBaseDir ("/etc/tailkitd/vars") in production.
func NewStore(baseDir string, logger *zap.Logger) *Store {
	return &Store{
		baseDir: baseDir,
		logger:  logger.With(zap.String("component", "vars")),
	}
}

// ─── Public API ───────────────────────────────────────────────────────────────

// List returns all non-reserved keys and their values for a scope.
// Keys starting with "_" are stripped before returning.
// Returns ErrScopeNotFound if the scope file does not exist.
func (s *Store) List(ctx context.Context, project, env string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mu := s.scopeMu(project, env)
	mu.RLock()
	defer mu.RUnlock()

	data, err := s.readScope(project, env)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string, len(data))
	for k, v := range data {
		if !strings.HasPrefix(k, "_") {
			result[k] = v
		}
	}
	return result, nil
}

// Get returns the value of a single key in a scope.
// Returns ("", ErrKeyNotFound) if the key does not exist.
// Returns ErrReservedKey if the key starts with "_".
func (s *Store) Get(ctx context.Context, project, env, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.HasPrefix(key, "_") {
		return "", ErrReservedKey
	}

	mu := s.scopeMu(project, env)
	mu.RLock()
	defer mu.RUnlock()

	data, err := s.readScope(project, env)
	if err != nil {
		return "", err
	}
	v, ok := data[key]
	if !ok {
		return "", ErrKeyNotFound
	}
	return v, nil
}

// Set writes a key/value pair to a scope and updates _meta.*.
// Returns ErrReservedKey if key starts with "_".
// Returns ErrInvalidKey if key does not match the allowed pattern.
func (s *Store) Set(ctx context.Context, project, env, key, value, caller string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.HasPrefix(key, "_") {
		return ErrReservedKey
	}
	if !keyPattern.MatchString(key) {
		return fmt.Errorf("%w: key %q must match %s", ErrInvalidKey, key, keyPatternStr)
	}

	mu := s.scopeMu(project, env)
	mu.Lock()
	defer mu.Unlock()

	data, err := s.readScopeOrEmpty(project, env)
	if err != nil {
		return err
	}

	data[key] = value
	data["_meta.updated_at"] = time.Now().UTC().Format(time.RFC3339)
	data["_meta.updated_by"] = caller

	if err := s.writeScope(project, env, data); err != nil {
		return err
	}

	s.logger.Info("var set",
		zap.String("project", project),
		zap.String("env", env),
		zap.String("key", key),
		zap.String("caller", caller),
		// Never log the value — it may be a secret.
	)
	return nil
}

// Delete removes a key from a scope and updates _meta.*.
// Returns ErrReservedKey if key starts with "_".
// Returns nil (not an error) if the key did not exist.
func (s *Store) Delete(ctx context.Context, project, env, key, caller string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.HasPrefix(key, "_") {
		return ErrReservedKey
	}

	mu := s.scopeMu(project, env)
	mu.Lock()
	defer mu.Unlock()

	data, err := s.readScopeOrEmpty(project, env)
	if err != nil {
		return err
	}

	delete(data, key)
	data["_meta.updated_at"] = time.Now().UTC().Format(time.RFC3339)
	data["_meta.updated_by"] = caller

	if err := s.writeScope(project, env, data); err != nil {
		return err
	}

	s.logger.Info("var deleted",
		zap.String("project", project),
		zap.String("env", env),
		zap.String("key", key),
		zap.String("caller", caller),
	)
	return nil
}

// DeleteScope removes the entire scope file.
// Returns nil if the scope did not exist.
func (s *Store) DeleteScope(ctx context.Context, project, env string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	mu := s.scopeMu(project, env)
	mu.Lock()
	defer mu.Unlock()

	path := s.scopePath(project, env)
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("vars: delete scope %s/%s: %w", project, env, err)
	}
	return nil
}

// RenderEnv returns vars as sorted KEY=VALUE lines, shell-quoted.
// Used by GET /vars/{project}/{env}?format=env.
func (s *Store) RenderEnv(ctx context.Context, project, env string) (string, error) {
	vars, err := s.List(ctx, project, env)
	if err != nil {
		return "", err
	}

	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(shellQuote(vars[k]))
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

// ─── Errors ───────────────────────────────────────────────────────────────────

type storeError string

func (e storeError) Error() string { return string(e) }

const (
	ErrScopeNotFound = storeError("vars: scope not found")
	ErrKeyNotFound   = storeError("vars: key not found")
	ErrReservedKey   = storeError("vars: key is reserved (_-prefixed keys are internal)")
	ErrInvalidKey    = storeError("vars: invalid key format")
)

// ─── Internals ────────────────────────────────────────────────────────────────

// scopeMu returns (creating if necessary) the *sync.RWMutex for a scope.
func (s *Store) scopeMu(project, env string) *sync.RWMutex {
	key := project + "/" + env
	v, _ := s.mu.LoadOrStore(key, &sync.RWMutex{})
	return v.(*sync.RWMutex)
}

// scopePath returns the filesystem path for a scope's JSON file.
func (s *Store) scopePath(project, env string) string {
	return filepath.Join(s.baseDir, project, env+".json")
}

// readScope reads and parses the scope JSON file.
// Returns ErrScopeNotFound if the file does not exist.
func (s *Store) readScope(project, env string) (scopeData, error) {
	path := s.scopePath(project, env)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrScopeNotFound
		}
		return nil, fmt.Errorf("vars: read scope %s/%s: %w", project, env, err)
	}
	var sd scopeData
	if err := json.Unmarshal(data, &sd); err != nil {
		return nil, fmt.Errorf("vars: parse scope %s/%s: %w", project, env, err)
	}
	return sd, nil
}

// readScopeOrEmpty reads the scope, returning an empty map if it doesn't exist.
func (s *Store) readScopeOrEmpty(project, env string) (scopeData, error) {
	sd, err := s.readScope(project, env)
	if err == ErrScopeNotFound {
		return make(scopeData), nil
	}
	return sd, err
}

// writeScope atomically writes sd to the scope's JSON file.
func (s *Store) writeScope(project, env string, sd scopeData) error {
	path := s.scopePath(project, env)
	dir := filepath.Dir(path)

	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("vars: mkdir %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(sd, "", "  ")
	if err != nil {
		return fmt.Errorf("vars: marshal scope %s/%s: %w", project, env, err)
	}

	// Atomic write: temp in same dir → rename (invariant 2).
	tmp, err := os.CreateTemp(dir, ".tailkitd-vars-*")
	if err != nil {
		return fmt.Errorf("vars: create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("vars: write temp file: %w", err)
	}
	if err := tmp.Chmod(0640); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("vars: chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("vars: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("vars: rename %s → %s: %w", tmpName, path, err)
	}
	return nil
}

// shellQuote wraps a value in single quotes and escapes any literal single
// quotes within it, producing a safely shell-quotable string.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
