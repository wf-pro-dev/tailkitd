package config

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/fsnotify/fsnotify"
	"github.com/wf-pro-dev/tailkitd/internal/utils"
	"go.uber.org/zap"
)

const (
	HostsConfigPath       = "/etc/tailkitd/hosts.toml"
	HostConfigPath        = HostsConfigPath
	hostReloadDebounce    = 300 * time.Millisecond
	hostWatchPollInterval = 50 * time.Millisecond
)

// HostConfig is one fleet host entry keyed by Tailscale peer name.
type HostConfig struct {
	Name         string            `toml:"name" json:"name"`
	Role         string            `toml:"role" json:"role"`
	Environment  string            `toml:"environment" json:"environment"`
	Provider     string            `toml:"provider" json:"provider"`
	InstanceType string            `toml:"instance_type" json:"instance_type"`
	Tags         []string          `toml:"tags" json:"tags"`
	Metadata     map[string]string `toml:"metadata" json:"metadata"`
}

type hostFile struct {
	Hosts []HostConfig `toml:"hosts"`
}

func LoadHostFile(path string) ([]HostConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var raw hostFile
	meta, err := toml.Decode(string(data), &raw)
	if err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("config: parse %s: unknown key %q", path, undecoded[0].String())
	}
	if len(raw.Hosts) == 0 {
		return nil, fmt.Errorf("config: parse %s: no hosts defined", path)
	}

	seen := make(map[string]struct{}, len(raw.Hosts))
	for i := range raw.Hosts {
		raw.Hosts[i].SetDefaults(raw.Hosts[i].Name)
		if raw.Hosts[i].Name == "" {
			return nil, fmt.Errorf("config: parse %s: hosts[%d].name is required", path, i)
		}
		if _, ok := seen[raw.Hosts[i].Name]; ok {
			return nil, fmt.Errorf("config: parse %s: duplicate host name %q", path, raw.Hosts[i].Name)
		}
		seen[raw.Hosts[i].Name] = struct{}{}
	}

	return raw.Hosts, nil
}

func (h *HostConfig) SetDefaults(name string) {
	if h.Name == "" {
		h.Name = name
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

func cloneHostConfigs(cfgs []HostConfig) []HostConfig {
	if cfgs == nil {
		return nil
	}
	clones := make([]HostConfig, len(cfgs))
	for i := range cfgs {
		clones[i] = *cloneHostConfig(&cfgs[i])
	}
	return clones
}

func WriteHostFile(path string, hosts []HostConfig) error {
	var buf bytes.Buffer
	payload := hostFile{Hosts: cloneHostConfigs(hosts)}
	if err := toml.NewEncoder(&buf).Encode(payload); err != nil {
		return fmt.Errorf("config: encode %s: %w", path, err)
	}
	return utils.AtomicWrite(path, buf.Bytes(), 0o644)
}

func EnsureHostConfig(path, selfTailnetName string) ([]HostConfig, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("config: mkdir %s: %w", filepath.Dir(path), err)
	}

	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("config: stat %s: %w", path, err)
		}

		cfg := HostConfig{}
		cfg.SetDefaults(selfTailnetName)
		hosts := []HostConfig{cfg}
		if err := WriteHostFile(path, hosts); err != nil {
			return nil, fmt.Errorf("config: generate %s: %w", path, err)
		}
		return hosts, nil
	}

	return LoadHostFile(path)
}

type HostManager struct {
	mu              sync.RWMutex
	hosts           []HostConfig
	self            *HostConfig
	path            string
	selfTailnetName string
	logger          *zap.Logger
	watcher         *fsnotify.Watcher
}

func NewHostManager(ctx context.Context, path, selfTailnetName string, logger *zap.Logger) (*HostManager, error) {
	hosts, err := EnsureHostConfig(path, selfTailnetName)
	if err != nil {
		return nil, err
	}
	self, err := resolveSelfHost(hosts, selfTailnetName)
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
		hosts:           cloneHostConfigs(hosts),
		self:            cloneHostConfig(self),
		path:            path,
		selfTailnetName: selfTailnetName,
		logger:          logger.With(zap.String("component", "config.host")),
		watcher:         watcher,
	}

	go mgr.watchLoop(ctx)
	return mgr, nil
}

func resolveSelfHost(hosts []HostConfig, selfTailnetName string) (*HostConfig, error) {
	for i := range hosts {
		if hosts[i].Name == selfTailnetName {
			return cloneHostConfig(&hosts[i]), nil
		}
	}
	return nil, fmt.Errorf("config: no host entry found for local tailscale peer %q", selfTailnetName)
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
	return cloneHostConfig(m.self)
}

func (m *HostManager) All() []HostConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneHostConfigs(m.hosts)
}

func (m *HostManager) Names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.hosts))
	for _, host := range m.hosts {
		names = append(names, host.Name)
	}
	slices.Sort(names)
	return names
}

func (m *HostManager) SelfName() string {
	return m.selfTailnetName
}

func (m *HostManager) ReplaceSelf(cfg *HostConfig) error {
	if cfg == nil {
		return fmt.Errorf("config: self host is required")
	}
	cfg = cloneHostConfig(cfg)
	cfg.SetDefaults(m.selfTailnetName)
	if cfg.Name != m.selfTailnetName {
		return fmt.Errorf("config: host name %q does not match local tailscale peer %q", cfg.Name, m.selfTailnetName)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	replaced := false
	next := cloneHostConfigs(m.hosts)
	for i := range next {
		if next[i].Name == m.selfTailnetName {
			next[i] = *cfg
			replaced = true
			break
		}
	}
	if !replaced {
		next = append(next, *cfg)
	}
	m.hosts = next
	m.self = cloneHostConfig(cfg)
	return nil
}

func (m *HostManager) Reload() error {
	hosts, err := LoadHostFile(m.path)
	if err != nil {
		return err
	}
	self, err := resolveSelfHost(hosts, m.selfTailnetName)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.hosts = cloneHostConfigs(hosts)
	m.self = cloneHostConfig(self)
	m.mu.Unlock()
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
