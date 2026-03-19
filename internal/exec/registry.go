// Package exec implements the tailkitd exec integration: an in-memory registry
// of tool commands watched for live updates, a safe runner, a job store, and
// the HTTP handlers for POST /exec/{tool}/{cmd} and GET /exec/jobs/{id}.
package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"

	tailkit "github.com/wf-pro-dev/tailkit"
)

const defaultToolsDir = "/etc/tailkitd/tools"

// ExecEntry is a resolved, ready-to-run entry in the command registry.
// It pairs the Tool metadata with the specific Command so the runner has
// everything it needs without a second lookup.
type ExecEntry struct {
	Tool    tailkit.Tool
	Command tailkit.Command
}

// Registry maintains an in-memory index of all registered tool commands,
// keyed by "tool/command". It watches the tools directory with fsnotify and
// rebuilds the index automatically when files are created, modified, or deleted.
//
// The index is rebuilt atomically — readers always see either the old or the
// new complete index, never a partial rebuild.
type Registry struct {
	dir    string
	logger *zap.Logger

	mu    sync.RWMutex
	index map[string]ExecEntry // key: "toolName/cmdName"
}

// NewRegistry constructs an exec Registry and performs an initial load from dir.
// It starts a background goroutine that watches dir with fsnotify and rebuilds
// the index on any change. The goroutine exits when ctx is cancelled.
func NewRegistry(ctx context.Context, dir string, logger *zap.Logger) (*Registry, error) {
	r := &Registry{
		dir:    dir,
		logger: logger.With(zap.String("component", "exec.registry")),
		index:  make(map[string]ExecEntry),
	}

	// Initial load — not fatal if the dir doesn't exist yet.
	if err := r.rebuild(); err != nil {
		r.logger.Warn("initial exec registry load failed", zap.Error(err))
	}

	// Start file watcher.
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("exec registry: failed to create watcher: %w", err)
	}

	// Watch the tools dir if it exists; if not, we'll still serve (empty index).
	if _, statErr := os.Stat(dir); statErr == nil {
		if err := watcher.Add(dir); err != nil {
			r.logger.Warn("could not watch tools dir", zap.String("dir", dir), zap.Error(err))
		}
	}

	go r.watchLoop(ctx, watcher)

	return r, nil
}

// Lookup finds an ExecEntry by tool and command name.
// Returns (entry, true) if found, (zero, false) if the tool or command is not registered.
func (r *Registry) Lookup(toolName, cmdName string) (ExecEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.index[toolName+"/"+cmdName]
	return entry, ok
}

// Commands returns all registered commands, grouped by tool name.
// Used by GET /tools to include exec-registered commands.
func (r *Registry) Commands(toolName string) []tailkit.Command {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var cmds []tailkit.Command
	for key, entry := range r.index {
		if strings.HasPrefix(key, toolName+"/") {
			cmds = append(cmds, entry.Command)
		}
	}
	return cmds
}

// ─── Internal ─────────────────────────────────────────────────────────────────

func (r *Registry) watchLoop(ctx context.Context, watcher *fsnotify.Watcher) {
	defer watcher.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			// Only care about JSON files.
			if !strings.HasSuffix(event.Name, ".json") {
				continue
			}
			r.logger.Info("tools dir changed, rebuilding exec registry",
				zap.String("file", filepath.Base(event.Name)),
				zap.String("op", event.Op.String()),
			)
			if err := r.rebuild(); err != nil {
				r.logger.Error("exec registry rebuild failed", zap.Error(err))
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			r.logger.Error("fsnotify watcher error", zap.Error(err))
		}
	}
}

// rebuild reads all *.json files from the tools directory and atomically
// replaces the in-memory index. One bad file is logged and skipped.
func (r *Registry) rebuild() error {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		if os.IsNotExist(err) {
			// Dir not created yet — reset to empty index.
			r.mu.Lock()
			r.index = make(map[string]ExecEntry)
			r.mu.Unlock()
			return nil
		}
		return fmt.Errorf("read tools dir %s: %w", r.dir, err)
	}

	newIndex := make(map[string]ExecEntry)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		tool, err := r.readTool(filepath.Join(r.dir, entry.Name()))
		if err != nil {
			r.logger.Warn("skipping malformed tool file",
				zap.String("file", entry.Name()),
				zap.Error(err),
			)
			continue
		}
		for _, cmd := range tool.Commands {
			key := tool.Name + "/" + cmd.Name
			newIndex[key] = ExecEntry{Tool: tool, Command: cmd}
		}
		r.logger.Info("loaded tool into exec registry",
			zap.String("tool", tool.Name),
			zap.String("version", tool.Version),
			zap.Int("commands", len(tool.Commands)),
		)
	}

	r.mu.Lock()
	r.index = newIndex
	r.mu.Unlock()

	r.logger.Info("exec registry rebuilt", zap.Int("entries", len(newIndex)))
	return nil
}

func (r *Registry) readTool(path string) (tailkit.Tool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return tailkit.Tool{}, fmt.Errorf("read %s: %w", path, err)
	}
	var tool tailkit.Tool
	if err := json.Unmarshal(data, &tool); err != nil {
		return tailkit.Tool{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if tool.Name == "" {
		return tailkit.Tool{}, fmt.Errorf("tool in %s has empty name", path)
	}
	return tool, nil
}
