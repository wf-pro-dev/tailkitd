// Package metrics implements the tailkitd metrics integration, exposing
// host, CPU, memory, disk, network, and process metrics via gopsutil.
//
// No persistent connection is needed — gopsutil reads /proc directly on Linux.
// Missing metrics.toml → 503 on every endpoint (invariant 5).
package metrics

import (
	"context"
	"net/http"
	"sort"
	"time"

	gopsutilcpu "github.com/shirou/gopsutil/v4/cpu"
	gopsutildisk "github.com/shirou/gopsutil/v4/disk"
	gopsutilhost "github.com/shirou/gopsutil/v4/host"
	gopsutilmem "github.com/shirou/gopsutil/v4/mem"
	gopsutilnet "github.com/shirou/gopsutil/v4/net"
	gopsutilproc "github.com/shirou/gopsutil/v4/process"
	"go.uber.org/zap"

	"github.com/wf-pro-dev/tailkitd/internal/config"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
)

// Handler serves all /integrations/metrics/* endpoints.
type Handler struct {
	cfg               config.MetricsConfig
	logger            *zap.Logger
	streamInterval    time.Duration
	heartbeatInterval time.Duration
	portSnapshotter   portSnapshotter
	cpuSampler        func(context.Context) (CPUResult, error)
	memorySampler     func(context.Context) (MemoryResult, error)
	networkSampler    func(context.Context) ([]gopsutilnet.IOCountersStat, error)
	processSampler    func(context.Context) ([]ProcessStat, error)
	allSampler        func(context.Context) (AllMetrics, error)
}

// NewHandler constructs a metrics Handler.
func NewHandler(cfg config.MetricsConfig, logger *zap.Logger) *Handler {
	return &Handler{
		cfg:               cfg,
		logger:            logger.With(zap.String("component", "metrics")),
		streamInterval:    2 * time.Second,
		heartbeatInterval: 15 * time.Second,
		portSnapshotter:   newProcPortSnapshotter("/proc"),
	}
}

// Register mounts all metrics endpoints onto mux.
//
//	GET /integrations/metrics/available
//	GET /integrations/metrics/config
//	GET /integrations/metrics/host
//	GET /integrations/metrics/cpu
//	GET /integrations/metrics/memory
//	GET /integrations/metrics/disk
//	GET /integrations/metrics/network
//	GET /integrations/metrics/processes
//	GET /integrations/metrics/all
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/integrations/metrics/available", h.handleAvailable)
	mux.HandleFunc("/integrations/metrics/config", h.handleConfig)
	mux.HandleFunc("/integrations/metrics/host", h.handleHost)
	mux.HandleFunc("/integrations/metrics/cpu", h.handleCPU)
	mux.HandleFunc("/integrations/metrics/memory", h.handleMemory)
	mux.HandleFunc("/integrations/metrics/disk", h.handleDisk)
	mux.HandleFunc("/integrations/metrics/network", h.handleNetwork)
	mux.HandleFunc("/integrations/metrics/processes", h.handleProcesses)
	mux.HandleFunc("/integrations/metrics/all", h.handleAll)
	mux.HandleFunc("/integrations/metrics/cpu/stream", h.handleCPUStream)
	mux.HandleFunc("/integrations/metrics/memory/stream", h.handleMemoryStream)
	mux.HandleFunc("/integrations/metrics/network/stream", h.handleNetworkStream)
	mux.HandleFunc("/integrations/metrics/processes/stream", h.handleProcessesStream)
	mux.HandleFunc("/integrations/metrics/all/stream", h.handleAllStream)
	mux.HandleFunc("/integrations/metrics/ports/available", h.handlePortsAvailable)
	mux.HandleFunc("/integrations/metrics/ports", h.handlePorts)
	mux.HandleFunc("/integrations/metrics/ports/stream", h.handlePortsStream)
}

// ─── Guards ───────────────────────────────────────────────────────────────────

func (h *Handler) guard(w http.ResponseWriter) bool {
	if !h.cfg.Enabled {
		helpers.WriteError(w, http.StatusServiceUnavailable,
			"metrics integration not configured on this node",
			"create /etc/tailkitd/integrations/metrics.toml to enable")
		return false
	}
	return true
}

func methodGet(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET")
		return false
	}
	return true
}

// ─── GET /integrations/metrics/available ─────────────────────────────────────

func (h *Handler) handleAvailable(w http.ResponseWriter, r *http.Request) {
	if !methodGet(w, r) {
		return
	}
	helpers.WriteJSON(w, http.StatusOK, map[string]bool{"available": h.cfg.Enabled})
}

// --- GET /integrations/metrics/config ───────────────────────────────────────
func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request) {
	helpers.WriteJSON(w, http.StatusOK, h.cfg)
}

// ─── GET /integrations/metrics/host ──────────────────────────────────────────

func (h *Handler) handleHost(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w) || !methodGet(w, r) {
		return
	}
	if !h.cfg.Host.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"host metrics not enabled in metrics.toml", "")
		return
	}

	info, err := gopsutilhost.InfoWithContext(r.Context())
	if err != nil {
		h.logger.Error("metrics: host info failed", zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError, "failed to get host info", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, info)
}

// ─── GET /integrations/metrics/cpu ───────────────────────────────────────────

// CPUResult bundles per-CPU usage percentages with static CPU info.
type CPUResult struct {
	Info    []gopsutilcpu.InfoStat `json:"info"`
	Percent []float64              `json:"percent_per_cpu"`
	Total   float64                `json:"percent_total"`
}

func (h *Handler) handleCPU(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w) || !methodGet(w, r) {
		return
	}
	if !h.cfg.CPU.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"cpu metrics not enabled in metrics.toml", "")
		return
	}

	result, err := h.sampleCPU(r.Context())
	if err != nil {
		h.logger.Error("metrics: cpu sample failed", zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError, "failed to get CPU metrics", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, result)
}

// ─── GET /integrations/metrics/memory ────────────────────────────────────────

// MemoryResult bundles virtual and swap memory stats.
type MemoryResult struct {
	Virtual *gopsutilmem.VirtualMemoryStat `json:"virtual"`
	Swap    *gopsutilmem.SwapMemoryStat    `json:"swap"`
}

func (h *Handler) handleMemory(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w) || !methodGet(w, r) {
		return
	}
	if !h.cfg.Memory.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"memory metrics not enabled in metrics.toml", "")
		return
	}

	result, err := h.sampleMemory(r.Context())
	if err != nil {
		h.logger.Error("metrics: memory sample failed", zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError, "failed to get memory metrics", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, result)
}

// ─── GET /integrations/metrics/disk ──────────────────────────────────────────

func (h *Handler) handleDisk(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w) || !methodGet(w, r) {
		return
	}
	if !h.cfg.Disk.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"disk metrics not enabled in metrics.toml", "")
		return
	}

	paths := h.cfg.Disk.Paths
	if len(paths) == 0 {
		// No paths configured → discover all mounted partitions.
		partitions, err := gopsutildisk.PartitionsWithContext(r.Context(), false)
		if err != nil {
			h.logger.Error("metrics: disk partitions failed", zap.Error(err))
			helpers.WriteError(w, http.StatusInternalServerError,
				"failed to list disk partitions", "")
			return
		}
		for _, p := range partitions {
			paths = append(paths, p.Mountpoint)
		}
	}

	results := make([]*gopsutildisk.UsageStat, 0, len(paths))
	for _, path := range paths {
		usage, err := gopsutildisk.UsageWithContext(r.Context(), path)
		if err != nil {
			h.logger.Warn("metrics: disk usage failed",
				zap.String("path", path), zap.Error(err))
			continue
		}
		results = append(results, usage)
	}
	helpers.WriteJSON(w, http.StatusOK, results)
}

// ─── GET /integrations/metrics/network ───────────────────────────────────────

func (h *Handler) handleNetwork(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w) || !methodGet(w, r) {
		return
	}
	if !h.cfg.Network.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"network metrics not enabled in metrics.toml", "")
		return
	}

	counters, err := h.sampleNetwork(r.Context())
	if err != nil {
		h.logger.Error("metrics: network sample failed", zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError,
			"failed to get network metrics", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, counters)
}

// ─── GET /integrations/metrics/processes ─────────────────────────────────────

// ProcessStat is the per-process summary returned in the processes response.
// We define our own struct to control exactly which fields are exposed and
// to avoid serialising the full gopsutil Process object with its internal state.
type ProcessStat struct {
	PID        int32   `json:"pid"`
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	CPUPercent float64 `json:"cpu_percent"`
	MemoryRSS  uint64  `json:"memory_rss_bytes"`
	Cmdline    string  `json:"cmdline"`
}

func (h *Handler) handleProcesses(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w) || !methodGet(w, r) {
		return
	}
	if !h.cfg.Processes.Enabled {
		helpers.WriteError(w, http.StatusForbidden,
			"process metrics not enabled in metrics.toml", "")
		return
	}

	stats, err := h.sampleProcesses(r.Context())
	if err != nil {
		h.logger.Error("metrics: process sample failed", zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError,
			"failed to list processes", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, stats)
}

// collectProcessStats gathers stats for each process, skipping those that
// error (e.g. permission denied on /proc/<pid>/status for kernel threads).
func collectProcessStats(ctx context.Context, procs []*gopsutilproc.Process) []ProcessStat {
	stats := make([]ProcessStat, 0, len(procs))
	for _, p := range procs {
		name, err := p.NameWithContext(ctx)
		if err != nil {
			continue // skip unreadable processes (kernel threads, etc.)
		}

		cpuPct, _ := p.CPUPercentWithContext(ctx)

		var memRSS uint64
		if memInfo, err := p.MemoryInfoWithContext(ctx); err == nil && memInfo != nil {
			memRSS = memInfo.RSS
		}

		status := ""
		if statuses, err := p.StatusWithContext(ctx); err == nil && len(statuses) > 0 {
			status = statuses[0]
		}

		cmdline, _ := p.CmdlineWithContext(ctx)

		stats = append(stats, ProcessStat{
			PID:        p.Pid,
			Name:       name,
			Status:     status,
			CPUPercent: cpuPct,
			MemoryRSS:  memRSS,
			Cmdline:    cmdline,
		})
	}
	return stats
}

// ─── GET /integrations/metrics/all ───────────────────────────────────────────

// AllMetrics bundles every enabled metric into a single response.
// This lets callers fetch everything in one round trip.
type AllMetrics struct {
	Host      *gopsutilhost.InfoStat       `json:"host,omitempty"`
	CPU       *CPUResult                   `json:"cpu,omitempty"`
	Memory    *MemoryResult                `json:"memory,omitempty"`
	Disk      []*gopsutildisk.UsageStat    `json:"disk,omitempty"`
	Network   []gopsutilnet.IOCountersStat `json:"network,omitempty"`
	Processes []ProcessStat                `json:"processes,omitempty"`
	Ports     []ListenPort                 `json:"ports,omitempty"`
}

func (h *Handler) handleAll(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w) || !methodGet(w, r) {
		return
	}
	result, _ := h.sampleAll(r.Context())
	helpers.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) sampleCPU(ctx context.Context) (CPUResult, error) {
	if h.cpuSampler != nil {
		return h.cpuSampler(ctx)
	}

	percents, err := gopsutilcpu.PercentWithContext(ctx, 0, true)
	if err != nil {
		return CPUResult{}, err
	}

	info, err := gopsutilcpu.InfoWithContext(ctx)
	if err != nil {
		h.logger.Warn("metrics: cpu info unavailable", zap.Error(err))
		info = nil
	}

	total := 0.0
	for _, p := range percents {
		total += p
	}
	if len(percents) > 0 {
		total /= float64(len(percents))
	}
	return CPUResult{Info: info, Percent: percents, Total: total}, nil
}

func (h *Handler) sampleMemory(ctx context.Context) (MemoryResult, error) {
	if h.memorySampler != nil {
		return h.memorySampler(ctx)
	}

	vmem, err := gopsutilmem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return MemoryResult{}, err
	}
	swap, err := gopsutilmem.SwapMemoryWithContext(ctx)
	if err != nil {
		h.logger.Warn("metrics: swap memory unavailable", zap.Error(err))
		swap = nil
	}
	return MemoryResult{Virtual: vmem, Swap: swap}, nil
}

func (h *Handler) sampleNetwork(ctx context.Context) ([]gopsutilnet.IOCountersStat, error) {
	if h.networkSampler != nil {
		return h.networkSampler(ctx)
	}

	counters, err := gopsutilnet.IOCountersWithContext(ctx, true)
	if err != nil {
		return nil, err
	}
	ifaces := h.cfg.Network.Interfaces
	if len(ifaces) == 0 {
		return counters, nil
	}

	ifaceSet := make(map[string]bool, len(ifaces))
	for _, iface := range ifaces {
		ifaceSet[iface] = true
	}
	filtered := counters[:0]
	for _, c := range counters {
		if ifaceSet[c.Name] {
			filtered = append(filtered, c)
		}
	}
	return filtered, nil
}

func (h *Handler) sampleProcesses(ctx context.Context) ([]ProcessStat, error) {
	if h.processSampler != nil {
		return h.processSampler(ctx)
	}

	procs, err := gopsutilproc.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}
	stats := collectProcessStats(ctx, procs)
	sort.Slice(stats, func(i, j int) bool {
		return stats[i].CPUPercent > stats[j].CPUPercent
	})
	limit := h.cfg.ProcessLimit()
	if len(stats) > limit {
		stats = stats[:limit]
	}
	return stats, nil
}

func (h *Handler) samplePorts(ctx context.Context) ([]ListenPort, error) {
	if h.portSnapshotter == nil {
		return nil, nil
	}
	return h.portSnapshotter.Snapshot(ctx)
}

func (h *Handler) sampleAll(ctx context.Context) (AllMetrics, error) {
	if h.allSampler != nil {
		return h.allSampler(ctx)
	}

	result := AllMetrics{}
	if h.cfg.Host.Enabled {
		if info, err := gopsutilhost.InfoWithContext(ctx); err == nil {
			result.Host = info
		}
	}
	if h.cfg.CPU.Enabled {
		if cpuResult, err := h.sampleCPU(ctx); err == nil {
			result.CPU = &cpuResult
		}
	}
	if h.cfg.Memory.Enabled {
		if memoryResult, err := h.sampleMemory(ctx); err == nil {
			result.Memory = &memoryResult
		}
	}
	if h.cfg.Disk.Enabled {
		paths := h.cfg.Disk.Paths
		if len(paths) == 0 {
			if partitions, err := gopsutildisk.PartitionsWithContext(ctx, false); err == nil {
				for _, p := range partitions {
					paths = append(paths, p.Mountpoint)
				}
			}
		}
		for _, path := range paths {
			if usage, err := gopsutildisk.UsageWithContext(ctx, path); err == nil {
				result.Disk = append(result.Disk, usage)
			}
		}
	}
	if h.cfg.Network.Enabled {
		if counters, err := h.sampleNetwork(ctx); err == nil {
			result.Network = counters
		}
	}
	if h.cfg.Processes.Enabled {
		if stats, err := h.sampleProcesses(ctx); err == nil {
			result.Processes = stats
		}
	}
	if h.cfg.Ports.Enabled {
		if ports, err := h.samplePorts(ctx); err == nil {
			result.Ports = ports
		}
	}
	return result, nil
}
