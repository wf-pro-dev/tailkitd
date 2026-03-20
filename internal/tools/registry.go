// Package tools provides the tailkitd tool registry. It reads tool registration
// files from /etc/tailkitd/tools/ on every GET /tools request so that installs
// and upgrades are reflected immediately without a tailkitd restart.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	tailkit "github.com/wf-pro-dev/tailkit"
)

const DefaultToolsDir = "/etc/tailkitd/tools"

// Registry reads tool registration files and serves the GET /tools endpoint.
// It reads from disk on every request so installs and upgrades are live.
type Registry struct {
	dir    string
	logger *zap.Logger
}

// NewRegistry constructs a Registry that reads from the given directory.
// Pass defaultToolsDir ("/etc/tailkitd/tools") in production.
func NewRegistry(dir string, logger *zap.Logger) *Registry {
	return &Registry{
		dir:    dir,
		logger: logger.With(zap.String("component", "tools")),
	}
}

// List reads all *.json files in the tools directory and returns the parsed
// tools. Malformed files are logged as warnings and skipped — one bad file
// must not prevent other tools from being listed.
func (r *Registry) List(ctx context.Context) ([]tailkit.Tool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(r.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []tailkit.Tool{}, nil
		}
		return nil, fmt.Errorf("tools: read dir %s: %w", r.dir, err)
	}

	var tools []tailkit.Tool
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		tool, err := r.readOne(filepath.Join(r.dir, entry.Name()))
		if err != nil {
			r.logger.Warn("malformed tool file",
				zap.String("file", entry.Name()),
				zap.Error(err),
			)
			continue
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

// HasTool reports whether a tool with the given name (and at least the given
// minimum version) is registered. An empty minVersion matches any version.
func (r *Registry) HasTool(ctx context.Context, name, minVersion string) (bool, error) {
	tools, err := r.List(ctx)
	if err != nil {
		return false, err
	}
	for _, t := range tools {
		if t.Name != name {
			continue
		}
		if minVersion == "" {
			return true, nil
		}
		if versionAtLeast(t.Version, minVersion) {
			return true, nil
		}
	}
	return false, nil
}

// Handler returns an http.HandlerFunc for GET /tools.
// It lists all registered tools and responds with JSON.
func (r *Registry) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		tools, err := r.List(req.Context())
		if err != nil {
			r.logger.Error("failed to list tools", zap.Error(err))
			http.Error(w, `{"error":"failed to list tools"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(tools); err != nil {
			r.logger.Error("failed to encode tools response", zap.Error(err))
		}
	}
}

// ─── internals ───────────────────────────────────────────────────────────────

func (r *Registry) readOne(path string) (tailkit.Tool, error) {
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

// versionAtLeast does a simple lexicographic semver comparison sufficient for
// the "at least version X" check used in HasTool. For true semver ordering use
// golang.org/x/mod/semver — but that dependency is not worth adding just for this.
// The comparison works correctly for versions of the form "vMAJOR.MINOR.PATCH"
// or "MAJOR.MINOR.PATCH" as long as all components have the same number of digits,
// which is the case for all tools in this platform.
func versionAtLeast(have, want string) bool {
	// Normalise: strip leading "v".
	have = strings.TrimPrefix(have, "v")
	want = strings.TrimPrefix(want, "v")
	// Split into [major, minor, patch].
	hp := splitVer(have)
	wp := splitVer(want)
	// Pad to same length.
	for len(hp) < len(wp) {
		hp = append(hp, "0")
	}
	for len(wp) < len(hp) {
		wp = append(wp, "0")
	}
	for i := range hp {
		h := zeroPad(hp[i], len(wp[i]))
		w := zeroPad(wp[i], len(hp[i]))
		if h > w {
			return true
		}
		if h < w {
			return false
		}
	}
	return true // equal
}

func splitVer(v string) []string {
	return strings.Split(v, ".")
}

func zeroPad(s string, width int) string {
	for len(s) < width {
		s = "0" + s
	}
	return s
}
