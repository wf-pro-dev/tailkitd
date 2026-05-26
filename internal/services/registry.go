package services

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

const (
	DefaultServicesDir      = "/etc/tailkitd/services.d"
	reloadDebounce          = 300 * time.Millisecond
	registryWatchPollPeriod = 50 * time.Millisecond
)

type OutsiderServiceConfig struct {
	Name          string   `toml:"name" json:"name"`
	Runtime       string   `toml:"runtime" json:"runtime"`
	Priority      string   `toml:"priority" json:"priority"`
	Tags          []string `toml:"tags" json:"tags"`
	ExpectedPorts []uint16 `toml:"expected_ports" json:"expected_ports"`
	SystemdUnit   string   `toml:"systemd_unit" json:"systemd_unit,omitempty"`
	BinaryPath    string   `toml:"binary_path" json:"binary_path,omitempty"`
	PidFile       string   `toml:"pid_file" json:"pid_file,omitempty"`
}

func (s *OutsiderServiceConfig) Validate() error {
	s.Runtime = strings.ToLower(strings.TrimSpace(s.Runtime))
	if s.Name == "" {
		return fmt.Errorf("service name cannot be empty")
	}

	switch s.Runtime {
	case "systemd":
		if s.SystemdUnit == "" {
			return fmt.Errorf("runtime %q requires systemd_unit", s.Runtime)
		}
		if s.BinaryPath != "" || s.PidFile != "" {
			return fmt.Errorf("runtime %q cannot set binary_path or pid_file", s.Runtime)
		}
	case "binary":
		if s.BinaryPath == "" || s.PidFile == "" {
			return fmt.Errorf("runtime %q requires binary_path and pid_file", s.Runtime)
		}
		if s.SystemdUnit != "" {
			return fmt.Errorf("runtime %q cannot set systemd_unit", s.Runtime)
		}
	case "port-only":
		if s.SystemdUnit != "" || s.BinaryPath != "" || s.PidFile != "" {
			return fmt.Errorf("runtime %q cannot set target fields", s.Runtime)
		}
	default:
		return fmt.Errorf("invalid runtime %q", s.Runtime)
	}

	if s.Tags == nil {
		s.Tags = []string{}
	}
	if s.ExpectedPorts == nil {
		s.ExpectedPorts = []uint16{}
	}
	if s.Priority == "" {
		s.Priority = "normal"
	}

	return nil
}

func cloneService(cfg OutsiderServiceConfig) OutsiderServiceConfig {
	clone := cfg
	clone.Tags = append([]string(nil), cfg.Tags...)
	clone.ExpectedPorts = append([]uint16(nil), cfg.ExpectedPorts...)
	return clone
}

func cloneServices(in map[string]OutsiderServiceConfig) map[string]OutsiderServiceConfig {
	out := make(map[string]OutsiderServiceConfig, len(in))
	for k, v := range in {
		out[k] = cloneService(v)
	}
	return out
}

func FilePath(dir, name string) string {
	return filepath.Join(dir, name+".toml")
}

func LoadDir(dirPath string, logger *zap.Logger) (map[string]OutsiderServiceConfig, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]OutsiderServiceConfig{}, nil
		}
		return nil, fmt.Errorf("services: read dir %s: %w", dirPath, err)
	}

	loaded := make(map[string]OutsiderServiceConfig)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}

		fullPath := filepath.Join(dirPath, entry.Name())
		cfg, err := loadOne(fullPath)
		if err != nil {
			logger.Warn("skipping invalid service file",
				zap.String("file", entry.Name()),
				zap.Error(err),
			)
			continue
		}
		loaded[cfg.Name] = cfg
	}

	return loaded, nil
}

func loadOne(path string) (OutsiderServiceConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return OutsiderServiceConfig{}, fmt.Errorf("read %s: %w", path, err)
	}

	var cfg OutsiderServiceConfig
	meta, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return OutsiderServiceConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		return OutsiderServiceConfig{}, fmt.Errorf("parse %s: unknown key %q", path, undecoded[0].String())
	}
	if err := cfg.Validate(); err != nil {
		return OutsiderServiceConfig{}, fmt.Errorf("validate %s: %w", path, err)
	}

	return cfg, nil
}

type Registry struct {
	dir     string
	logger  *zap.Logger
	watcher *fsnotify.Watcher

	mu       sync.RWMutex
	services map[string]OutsiderServiceConfig
}

func NewRegistry(ctx context.Context, dir string, logger *zap.Logger) (*Registry, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("services: mkdir %s: %w", dir, err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("services: watcher: %w", err)
	}
	if err := watcher.Add(dir); err != nil {
		watcher.Close()
		return nil, fmt.Errorf("services: watch %s: %w", dir, err)
	}

	r := &Registry{
		dir:      dir,
		logger:   logger.With(zap.String("component", "services.registry")),
		watcher:  watcher,
		services: map[string]OutsiderServiceConfig{},
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
	loaded, err := LoadDir(r.dir, r.logger)
	if err != nil {
		return err
	}

	r.mu.Lock()
	r.services = cloneServices(loaded)
	r.mu.Unlock()
	return nil
}

func (r *Registry) GetServices() map[string]OutsiderServiceConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneServices(r.services)
}

func (r *Registry) ListServices() []OutsiderServiceConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.services))
	for name := range r.services {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]OutsiderServiceConfig, 0, len(names))
	for _, name := range names {
		out = append(out, cloneService(r.services[name]))
	}
	return out
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
			resetRegistryTimer(timer, reloadDebounce)
		case <-timer.C:
			if err := r.Reload(); err != nil {
				r.logger.Error("services registry reload failed", zap.Error(err))
			} else {
				r.logger.Info("services registry reloaded")
			}
		case err, ok := <-r.watcher.Errors:
			if !ok {
				return
			}
			r.logger.Error("services watcher error", zap.Error(err))
		}
	}
}

func resetRegistryTimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}
