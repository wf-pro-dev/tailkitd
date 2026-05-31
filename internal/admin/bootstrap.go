package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"tailscale.com/ipn/ipnstate"
)

var (
	AdminDirPath   = "/etc/tailkitd"
	AdminKeyPath   = "/etc/tailkitd/admin.key"
	AdminFencePath = "/etc/tailkitd/admin.fence"
)

const (
	adminKeyBytes   = 16
	adminProbeRoute = "/host"
)

type State struct {
	mu      sync.RWMutex
	isAdmin bool
}

func (s *State) IsAdmin() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isAdmin
}

func (s *State) SetAdmin(v bool) {
	s.mu.Lock()
	s.isAdmin = v
	s.mu.Unlock()
}

func EnsureBootstrapFiles() error {
	if err := os.MkdirAll(AdminDirPath, 0o755); err != nil {
		return fmt.Errorf("admin: mkdir %s: %w", AdminDirPath, err)
	}
	if _, err := os.Stat(AdminKeyPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("admin: stat %s: %w", AdminKeyPath, err)
		}
		key := make([]byte, adminKeyBytes)
		if _, err := rand.Read(key); err != nil {
			return fmt.Errorf("admin: generate key: %w", err)
		}
		if err := os.WriteFile(AdminKeyPath, []byte(hex.EncodeToString(key)), 0o600); err != nil {
			return fmt.Errorf("admin: write %s: %w", AdminKeyPath, err)
		}
	}
	if _, err := os.Stat(AdminFencePath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("admin: stat %s: %w", AdminFencePath, err)
		}
		if err := os.WriteFile(AdminFencePath, []byte("0\n"), 0o600); err != nil {
			return fmt.Errorf("admin: write %s: %w", AdminFencePath, err)
		}
	}
	return nil
}

func GetAdminKey() (string, error) {
	data, err := os.ReadFile(AdminKeyPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func GetFenceToken() (int, error) {
	data, err := os.ReadFile(AdminFencePath)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("admin: parse fence token: %w", err)
	}
	return n, nil
}

type PeerAdminClient interface {
	IsPeerAdmin(context.Context, string) (bool, error)
}

type HTTPPeerAdminClient struct {
	Client *http.Client
}

func (c HTTPPeerAdminClient) IsPeerAdmin(ctx context.Context, hostname string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+hostname+adminProbeRoute, nil)
	if err != nil {
		return false, err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	var body struct {
		IsAdmin bool `json:"is_admin"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false, err
	}
	return body.IsAdmin, nil
}

func daemonHostnameForTailnetPeer(hostname string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(hostname) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return "tailkitd-" + strings.Trim(b.String(), "-")
}

func PeerHostnames(status *ipnstate.Status, selfTailnetHostname string, definedHostNames []string) []string {
	if status == nil {
		return nil
	}
	allowed := make(map[string]struct{}, len(definedHostNames))
	for _, name := range definedHostNames {
		allowed[name] = struct{}{}
	}
	var hosts []string
	for _, peer := range status.Peer {
		if peer == nil || peer.HostName == "" || peer.HostName == selfTailnetHostname {
			continue
		}
		if _, ok := allowed[peer.HostName]; !ok {
			continue
		}
		hosts = append(hosts, daemonHostnameForTailnetPeer(peer.HostName))
	}
	sort.Strings(hosts)
	return hosts
}

func DetermineIsAdmin(ctx context.Context, selfHostname string, peerHostnames []string, client PeerAdminClient) bool {
	if len(peerHostnames) == 0 {
		return true
	}

	for _, peer := range peerHostnames {
		isAdmin, err := client.IsPeerAdmin(ctx, peer)
		if err != nil {
			continue
		}
		if isAdmin {
			return false
		}
	}

	all := append([]string{selfHostname}, peerHostnames...)
	sort.Strings(all)
	return all[0] == selfHostname
}

func BootstrapState(ctx context.Context, selfHostname, selfTailnetHostname string, status *ipnstate.Status, definedHostNames []string, client *http.Client, logger *zap.Logger) (*State, error) {
	if err := EnsureBootstrapFiles(); err != nil {
		return nil, err
	}
	logger.Debug("admin bootstrap files ensured",
		zap.String("admin_key_path", AdminKeyPath),
		zap.String("admin_fence_path", AdminFencePath),
	)

	probeClient := client
	if probeClient == nil {
		probeClient = &http.Client{Timeout: 5 * time.Second}
	}
	peerHostnames := PeerHostnames(status, selfTailnetHostname, definedHostNames)
	logger.Debug("admin peer set discovered",
		zap.String("self_hostname", selfHostname),
		zap.String("self_tailnet_hostname", selfTailnetHostname),
		zap.Strings("defined_host_names", definedHostNames),
		zap.Strings("peer_hostnames", peerHostnames),
	)
	state := &State{}
	isAdmin := true
	if len(peerHostnames) == 0 {
		logger.Info("admin bootstrap elected local node",
			zap.String("reason", "no peer tailkitd nodes discovered"),
			zap.String("self_hostname", selfHostname),
		)
	} else {
		foundRemoteAdmin := false
		for _, peer := range peerHostnames {
			peerIsAdmin, err := HTTPPeerAdminClient{Client: probeClient}.IsPeerAdmin(ctx, peer)
			if err != nil {
				logger.Warn("admin peer probe failed",
					zap.String("peer_hostname", peer),
					zap.Error(err),
				)
				continue
			}
			logger.Debug("admin peer probe completed",
				zap.String("peer_hostname", peer),
				zap.Bool("peer_is_admin", peerIsAdmin),
			)
			if peerIsAdmin {
				foundRemoteAdmin = true
				isAdmin = false
				logger.Info("admin bootstrap elected remote node",
					zap.String("reason", "existing admin detected"),
					zap.String("peer_hostname", peer),
				)
				break
			}
		}
		if !foundRemoteAdmin {
			all := append([]string{selfHostname}, peerHostnames...)
			sort.Strings(all)
			isAdmin = all[0] == selfHostname
			logger.Info("admin bootstrap applied deterministic election",
				zap.String("self_hostname", selfHostname),
				zap.Strings("candidates", all),
				zap.Bool("is_admin", isAdmin),
			)
		}
	}
	state.SetAdmin(isAdmin)

	logger.Info("admin state initialized",
		zap.Bool("is_admin", state.IsAdmin()),
		zap.Int("peer_count", len(peerHostnames)),
	)
	return state, nil
}
