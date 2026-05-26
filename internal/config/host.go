package config

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"
)

const (
	HostConfigPath        = "/etc/tailkitd/host.toml"
	hostReloadDebounce    = 300 * time.Millisecond
	hostWatchPollInterval = 50 * time.Millisecond
)

// HostConfig is the strict v0.1.0 host identity schema.
type HostConfig struct {
	Name         string            `toml:"name" json:"name"`
	Role         string            `toml:"role" json:"role"`
	Environment  string            `toml:"environment" json:"environment"`
	Provider     string            `toml:"provider" json:"provider"`
	InstanceType string            `toml:"instance_type" json:"instance_type"`
	Tags         []string          `toml:"tags" json:"tags"`
	Metadata     map[string]string `toml:"metadata" json:"metadata"`
}

func LoadHostConfig(path string) (*HostConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg HostConfig
	meta, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("config: parse %s: unknown key %q", path, undecoded[0].String())
	}

	return &cfg, nil
}

func (h *HostConfig) SetDefaults(tsHostname string) {
	if h.Name == "" {
		h.Name = tsHostname
	}
	if h.Role == "" {
		h.Role = "unclassified"
	}
	if h.Environment == "" {
		h.Environment = "default"
	}
	if h.Provider == "" {
		h.Provider = "unknown"
	}
	if h.Tags == nil {
		h.Tags = []string{}
	}
	if h.Metadata == nil {
		h.Metadata = make(map[string]string)
	}
}

func cloneHostConfig(cfg *HostConfig) *HostConfig {
	if cfg == nil {
		return nil
	}
	clone := *cfg
	clone.Tags = append([]string(nil), cfg.Tags...)
	clone.Metadata = make(map[string]string, len(cfg.Metadata))
	for k, v := range cfg.Metadata {
		clone.Metadata[k] = v
	}
	return &clone
}

func WriteHostConfig(path string, cfg *HostConfig) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("config: encode %s: %w", path, err)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func EnsureHostConfig(path, tsHostname string) (*HostConfig, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("config: mkdir %s: %w", filepath.Dir(path), err)
	}

	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("config: stat %s: %w", path, err)
		}

		cfg := &HostConfig{}
		cfg.SetDefaults(tsHostname)
		if err := WriteHostConfig(path, cfg); err != nil {
			return nil, fmt.Errorf("config: generate %s: %w", path, err)
		}
		return cfg, nil
	}

	cfg, err := LoadHostConfig(path)
	if err != nil {
		return nil, err
	}
	cfg.SetDefaults(tsHostname)
	return cfg, nil
}

type HostManager struct {
	mu         sync.RWMutex
	cfg        *HostConfig
	path       string
	tsHostname string
	logger     *zap.Logger
	watcher    *fsnotify.Watcher
}

func NewHostManager(ctx context.Context, path, tsHostname string, logger *zap.Logger) (*HostManager, error) {
	cfg, err := EnsureHostConfig(path, tsHostname)
	if err != nil {
		return nil, err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("config: host watcher: %w", err)
	}

	dir := filepath.Dir(path)
	if err := watcher.Add(dir); err != nil {
		watcher.Close()
		return nil, fmt.Errorf("config: watch %s: %w", dir, err)
	}

	mgr := &HostManager{
		cfg:        cloneHostConfig(cfg),
		path:       path,
		tsHostname: tsHostname,
		logger:     logger.With(zap.String("component", "config.host")),
		watcher:    watcher,
	}

	go mgr.watchLoop(ctx)
	return mgr, nil
}

func (m *HostManager) Close() error {
	if m == nil || m.watcher == nil {
		return nil
	}
	return m.watcher.Close()
}

func (m *HostManager) Get() *HostConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneHostConfig(m.cfg)
}

func (m *HostManager) Replace(cfg *HostConfig) {
	cfg = cloneHostConfig(cfg)
	m.mu.Lock()
	m.cfg = cfg
	m.mu.Unlock()
}

func (m *HostManager) Reload() error {
	cfg, err := LoadHostConfig(m.path)
	if err != nil {
		return err
	}
	cfg.SetDefaults(m.tsHostname)
	m.Replace(cfg)
	return nil
}

func (m *HostManager) watchLoop(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-m.watcher.Events:
			if !ok {
				return
			}
			if filepath.Clean(event.Name) != filepath.Clean(m.path) {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
				continue
			}
			resetTimer(timer, hostReloadDebounce)
		case <-timer.C:
			if err := m.Reload(); err != nil {
				m.logger.Error("host config reload failed",
					zap.String("file", m.path),
					zap.Error(err),
				)
			} else {
				m.logger.Info("host config reloaded", zap.String("file", m.path))
			}
		case err, ok := <-m.watcher.Errors:
			if !ok {
				return
			}
			m.logger.Error("host config watcher error", zap.Error(err))
		}
	}
}

func resetTimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}
