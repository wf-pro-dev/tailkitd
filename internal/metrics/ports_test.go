package metrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wf-pro-dev/tailkit"
	"go.uber.org/zap"
	"tailscale.com/portlist"

	"github.com/wf-pro-dev/tailkit/types"
	"github.com/wf-pro-dev/tailkitd/internal/config"
)

type staticPortSnapshotter struct {
	snapshots [][]types.Port
	call      int
}

func (s *staticPortSnapshotter) Snapshot(_ context.Context) ([]types.Port, error) {
	if len(s.snapshots) == 0 {
		return nil, nil
	}
	if s.call >= len(s.snapshots) {
		return s.snapshots[len(s.snapshots)-1], nil
	}
	snapshot := s.snapshots[s.call]
	s.call++
	return snapshot, nil
}

type staticPortlistPoller struct {
	snapshots [][]portlist.Port
	changed   []bool
	call      int
}

func (s *staticPortlistPoller) Poll() ([]portlist.Port, bool, error) {
	if s.call >= len(s.snapshots) {
		return nil, false, nil
	}
	snapshot := s.snapshots[s.call]
	changed := true
	if s.call < len(s.changed) {
		changed = s.changed[s.call]
	}
	s.call++
	return snapshot, changed, nil
}

func TestProcPortSnapshotterSnapshot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "net"), 0o755); err != nil {
		t.Fatalf("MkdirAll(net) error = %v", err)
	}

	const tcp = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12345 1 0000000000000000 100 0 0 10 0
`
	const udp = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
   1: 00000000:14E9 00000000:0000 07 00000000:00000000 00:00000000 00000000  1000        0 54321 2 0000000000000000 0
`
	if err := os.WriteFile(filepath.Join(root, "net", "tcp"), []byte(tcp), 0o644); err != nil {
		t.Fatalf("WriteFile(tcp) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "net", "tcp6"), []byte("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(tcp6) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "net", "udp"), []byte(udp), 0o644); err != nil {
		t.Fatalf("WriteFile(udp) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "net", "udp6"), []byte("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(udp6) error = %v", err)
	}

	snapshotter := &procPortSnapshotter{
		procRoot: root,
		poller: &staticPortlistPoller{
			snapshots: [][]portlist.Port{{
				{Proto: "udp", Port: 5353, Pid: 123, Process: "nginx"},
				{Proto: "tcp", Port: 8080, Pid: 123, Process: "nginx"},
			}},
		},
	}

	snapshot, err := snapshotter.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snapshot) != 2 {
		t.Fatalf("len(snapshot) = %d, want 2", len(snapshot))
	}
	if got := snapshot[0]; got.Addr != "0.0.0.0" || got.Port != 5353 || got.PID != 123 || got.Process != "nginx" || got.Proto != "udp" {
		t.Fatalf("snapshot[0] = %#v, want addr=0.0.0.0 proto=udp port=5353 pid=123 process=nginx", got)
	}
	if got := snapshot[1]; got.Addr != "127.0.0.1" || got.Port != 8080 || got.PID != 123 || got.Process != "nginx" || got.Proto != "tcp" {
		t.Fatalf("snapshot[1] = %#v, want addr=127.0.0.1 proto=tcp port=8080 pid=123 process=nginx", got)
	}
}

func TestProcPortSnapshotterPrefersIPv4Address(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "net"), 0o755); err != nil {
		t.Fatalf("MkdirAll(net) error = %v", err)
	}

	const tcp = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12345 1 0000000000000000 100 0 0 10 0
`
	const tcp6 = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:0050 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12346 1 0000000000000000 100 0 0 10 0
`
	if err := os.WriteFile(filepath.Join(root, "net", "tcp"), []byte(tcp), 0o644); err != nil {
		t.Fatalf("WriteFile(tcp) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "net", "tcp6"), []byte(tcp6), 0o644); err != nil {
		t.Fatalf("WriteFile(tcp6) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "net", "udp"), []byte("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(udp) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "net", "udp6"), []byte("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(udp6) error = %v", err)
	}

	snapshotter := &procPortSnapshotter{
		procRoot: root,
		poller: &staticPortlistPoller{
			snapshots: [][]portlist.Port{{
				{Proto: "tcp", Port: 80, Pid: 123, Process: "docker-proxy"},
			}},
		},
	}

	snapshot, err := snapshotter.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snapshot) != 1 {
		t.Fatalf("len(snapshot) = %d, want 1", len(snapshot))
	}
	if got := snapshot[0]; got.Addr != "0.0.0.0" || got.Port != 80 || got.PID != 123 || got.Process != "docker-proxy" || got.Proto != "tcp" {
		t.Fatalf("snapshot[0] = %#v, want addr=0.0.0.0 proto=tcp port=80 pid=123 process=docker-proxy", got)
	}
}

func TestProcPortSnapshotterUsesCachedPortlistSnapshot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "net"), 0o755); err != nil {
		t.Fatalf("MkdirAll(net) error = %v", err)
	}

	const tcp = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12345 1 0000000000000000 100 0 0 10 0
`
	if err := os.WriteFile(filepath.Join(root, "net", "tcp"), []byte(tcp), 0o644); err != nil {
		t.Fatalf("WriteFile(tcp) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "net", "tcp6"), []byte("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(tcp6) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "net", "udp"), []byte("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(udp) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "net", "udp6"), []byte("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(udp6) error = %v", err)
	}

	snapshotter := &procPortSnapshotter{
		procRoot: root,
		poller: &staticPortlistPoller{
			snapshots: [][]portlist.Port{
				{{Proto: "tcp", Port: 8080, Pid: 123, Process: "nginx"}},
				nil,
			},
			changed: []bool{true, false},
		},
	}

	first, err := snapshotter.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("first Snapshot() error = %v", err)
	}
	second, err := snapshotter.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("second Snapshot() error = %v", err)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("unexpected snapshot sizes: first=%d second=%d", len(first), len(second))
	}
	if second[0].Addr != "127.0.0.1" || second[0].PID != 123 || second[0].Process != "nginx" {
		t.Fatalf("second snapshot = %#v, want cached enrichment", second[0])
	}
}

func TestProcPortSnapshotterFallsBackToIPv6Address(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "net"), 0o755); err != nil {
		t.Fatalf("MkdirAll(net) error = %v", err)
	}

	const tcp6 = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:1F90 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12346 1 0000000000000000 100 0 0 10 0
`
	if err := os.WriteFile(filepath.Join(root, "net", "tcp"), []byte("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(tcp) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "net", "tcp6"), []byte(tcp6), 0o644); err != nil {
		t.Fatalf("WriteFile(tcp6) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "net", "udp"), []byte("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(udp) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "net", "udp6"), []byte("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(udp6) error = %v", err)
	}

	snapshotter := &procPortSnapshotter{
		procRoot: root,
		poller: &staticPortlistPoller{
			snapshots: [][]portlist.Port{{
				{Proto: "tcp", Port: 8080, Pid: 123, Process: "nginx"},
			}},
		},
	}

	snapshot, err := snapshotter.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snapshot) != 1 {
		t.Fatalf("len(snapshot) = %d, want 1", len(snapshot))
	}
	if got := snapshot[0]; got.Addr != "::" || got.Port != 8080 || got.PID != 123 || got.Process != "nginx" || got.Proto != "tcp" {
		t.Fatalf("snapshot[0] = %#v, want addr=:: proto=tcp port=8080 pid=123 process=nginx", got)
	}
}

func TestMetricsPortsEndpoints(t *testing.T) {
	t.Parallel()

	handler := NewHandler(config.MetricsConfig{
		Enabled: true,
		Ports: config.PortMetricsConfig{
			Enabled: true,
		},
	}, zap.NewNop())
	handler.portSnapshotter = &staticPortSnapshotter{
		snapshots: [][]types.Port{{
			{Addr: "0.0.0.0", Port: 80, Proto: "tcp", PID: 1234, Process: "nginx"},
		}},
	}

	mux := http.NewServeMux()
	handler.Register(mux)

	t.Run("available", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/integrations/metrics/ports/available", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("snapshot", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/integrations/metrics/ports", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}

		var ports []types.Port
		if err := json.Unmarshal(rec.Body.Bytes(), &ports); err != nil {
			t.Fatalf("Unmarshal() error = %v", err)
		}
		if len(ports) != 1 || ports[0].Port != 80 {
			t.Fatalf("ports = %#v, want one port 80", ports)
		}
	})
}

func TestMetricsPortsStream(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	handler := NewHandler(config.MetricsConfig{
		Enabled: true,
		Ports: config.PortMetricsConfig{
			Enabled: true,
		},
	}, zap.NewNop())
	handler.streamInterval = 5 * time.Millisecond
	handler.heartbeatInterval = 50 * time.Millisecond
	handler.portSnapshotter = &staticPortSnapshotter{
		snapshots: [][]types.Port{
			{{Addr: "0.0.0.0", Port: 80, Proto: "tcp", PID: 1234, Process: "nginx"}},
			{
				{Addr: "0.0.0.0", Port: 80, Proto: "tcp", PID: 1234, Process: "nginx"},
				{Addr: "127.0.0.1", Port: 3000, Proto: "tcp", PID: 5678, Process: "node"},
			},
		},
	}
	time.AfterFunc(20*time.Millisecond, cancel)

	req := httptest.NewRequest(http.MethodGet, "/integrations/metrics/ports/stream", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.handlePortsStream(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "event: "+tailkit.EventPortsSnapshot) {
		t.Fatalf("snapshot event missing from body: %q", body)
	}
	if !strings.Contains(body, "\"kind\":\"snapshot\"") {
		t.Fatalf("snapshot payload missing from body: %q", body)
	}
	if !strings.Contains(body, "event: "+tailkit.EventPortBound) || !strings.Contains(body, "\"port\":{\"addr\":\"127.0.0.1\",\"port\":3000") {
		t.Fatalf("bound event missing from body: %q", body)
	}
}

func TestMetricsPortsStreamIgnoresPIDOnlyChanges(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	handler := NewHandler(config.MetricsConfig{
		Enabled: true,
		Ports: config.PortMetricsConfig{
			Enabled: true,
		},
	}, zap.NewNop())
	handler.streamInterval = 5 * time.Millisecond
	handler.heartbeatInterval = 50 * time.Millisecond
	handler.portSnapshotter = &staticPortSnapshotter{
		snapshots: [][]types.Port{
			{{Addr: "", Port: 9323, Proto: "tcp", PID: 158693, Process: "docker-proxy"}},
			{{Addr: "", Port: 9323, Proto: "tcp", PID: 158700, Process: "docker-proxy"}},
			{{Addr: "", Port: 9323, Proto: "tcp", PID: 158700, Process: "docker-proxy"}},
			{{Addr: "", Port: 9323, Proto: "tcp", PID: 158700, Process: "docker-proxy"}},
		},
	}
	time.AfterFunc(20*time.Millisecond, cancel)

	req := httptest.NewRequest(http.MethodGet, "/integrations/metrics/ports/stream", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.handlePortsStream(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "event: "+tailkit.EventPortBound) {
		t.Fatalf("unexpected bound event for PID-only change: %q", body)
	}
	if strings.Contains(body, "event: "+tailkit.EventPortReleased) {
		t.Fatalf("unexpected released event for PID-only change: %q", body)
	}
}

func TestMetricsAllIncludesPorts(t *testing.T) {
	t.Parallel()

	handler := NewHandler(config.MetricsConfig{
		Enabled: true,
		Ports: config.PortMetricsConfig{
			Enabled: true,
		},
	}, zap.NewNop())
	handler.portSnapshotter = &staticPortSnapshotter{
		snapshots: [][]types.Port{{
			{Addr: "0.0.0.0", Port: 443, Proto: "tcp", PID: 4321, Process: "caddy"},
		}},
	}

	req := httptest.NewRequest(http.MethodGet, "/integrations/metrics/all", nil)
	rec := httptest.NewRecorder()
	handler.handleAll(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var got types.Metrics
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(got.Ports) != 1 || got.Ports[0].Port != 443 {
		t.Fatalf("Ports = %#v, want one port 443", got.Ports)
	}
}
