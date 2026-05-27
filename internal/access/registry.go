package access

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"
)

var DefaultAccessDir = "/etc/tailkitd/access.d"

const accessReloadDebounce = 300 * time.Millisecond

type Grant struct {
	Identity string `toml:"identity" json:"identity"`
	Target   string `toml:"target" json:"target"`
	Role     string `toml:"role" json:"role"`
}

type fileGrants struct {
	Grants []Grant `toml:"grants"`
}

func (g *Grant) Validate() error {
	g.Identity = strings.TrimSpace(strings.ToLower(g.Identity))
	g.Target = strings.TrimSpace(strings.ToLower(g.Target))
	g.Role = strings.TrimSpace(strings.ToLower(g.Role))
	if g.Identity == "" {
		return fmt.Errorf("identity cannot be empty")
	}
	if g.Target == "" {
		return fmt.Errorf("target cannot be empty")
	}
	switch g.Role {
	case "admin", "superadmin":
	default:
		return fmt.Errorf("invalid role %q", g.Role)
	}
	return nil
}

type Registry struct {
	dir     string
	logger  *zap.Logger
	watcher *fsnotify.Watcher

	mu     sync.RWMutex
	grants []Grant
}

func NewRegistry(ctx context.Context, dir string, logger *zap.Logger) (*Registry, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("access: mkdir %s: %w", dir, err)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("access: watcher: %w", err)
	}
	if err := watcher.Add(dir); err != nil {
		watcher.Close()
		return nil, fmt.Errorf("access: watch %s: %w", dir, err)
	}
	r := &Registry{
		dir:     dir,
		logger:  logger.With(zap.String("component", "access.registry")),
		watcher: watcher,
		grants:  []Grant{},
	}
	if err := r.Reload(); err != nil {
		watcher.Close()
		return nil, err
	}
	go r.watchLoop(ctx)
	return r, nil
}

func (r *Registry) Close() error {
	if r == nil || r.watcher == nil {
		return nil
	}
	return r.watcher.Close()
}

func (r *Registry) Reload() error {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return fmt.Errorf("access: read dir %s: %w", r.dir, err)
	}
	var grants []Grant
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}
		loaded, err := loadFile(filepath.Join(r.dir, entry.Name()))
		if err != nil {
			r.logger.Warn("skipping invalid access file",
				zap.String("file", entry.Name()),
				zap.Error(err),
			)
			continue
		}
		grants = append(grants, loaded...)
	}
	sort.Slice(grants, func(i, j int) bool {
		if grants[i].Identity != grants[j].Identity {
			return grants[i].Identity < grants[j].Identity
		}
		if grants[i].Target != grants[j].Target {
			return grants[i].Target < grants[j].Target
		}
		return grants[i].Role < grants[j].Role
	})

	r.mu.Lock()
	r.grants = grants
	r.mu.Unlock()
	return nil
}

func loadFile(path string) ([]Grant, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var parsed fileGrants
	meta, err := toml.Decode(string(data), &parsed)
	if err != nil {
		return nil, err
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("unknown key %q", undecoded[0].String())
	}
	for i := range parsed.Grants {
		if err := parsed.Grants[i].Validate(); err != nil {
			return nil, err
		}
	}
	return parsed.Grants, nil
}

func (r *Registry) List() []Grant {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Grant, len(r.grants))
	copy(out, r.grants)
	return out
}

func (r *Registry) HasAnyGrants() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.grants) > 0
}

func (r *Registry) RoleFor(identity, target string) (string, bool) {
	identity = strings.ToLower(identity)
	target = strings.ToLower(target)

	r.mu.RLock()
	defer r.mu.RUnlock()

	var wildcard string
	for _, grant := range r.grants {
		if grant.Identity != identity {
			continue
		}
		if grant.Target == target {
			return grant.Role, true
		}
		if grant.Target == "*" {
			wildcard = grant.Role
		}
	}
	if wildcard != "" {
		return wildcard, true
	}
	return "", false
}

func (r *Registry) Allow(identity, capability, target string) bool {
	role, ok := r.RoleFor(identity, target)
	if !ok {
		role, ok = r.RoleFor(identity, "*")
	}
	if !ok {
		return false
	}

	switch capability {
	case "service.write":
		return role == "admin" || role == "superadmin"
	case "host.write", "access.write", "admin.transfer":
		return role == "superadmin"
	default:
		return false
	}
}

func (r *Registry) watchLoop(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-r.watcher.Events:
			if !ok {
				return
			}
			if !strings.HasSuffix(event.Name, ".toml") {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
				continue
			}
			resetAccessTimer(timer, accessReloadDebounce)
		case <-timer.C:
			if err := r.Reload(); err != nil {
				r.logger.Error("access registry reload failed", zap.Error(err))
			} else {
				r.logger.Info("access registry reloaded")
			}
		case err, ok := <-r.watcher.Errors:
			if !ok {
				return
			}
			r.logger.Error("access watcher error", zap.Error(err))
		}
	}
}

func resetAccessTimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}
