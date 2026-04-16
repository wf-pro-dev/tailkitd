package metrics

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"go.uber.org/zap"

	"github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkit/types"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
	"github.com/wf-pro-dev/tailkitd/internal/sse"
)

func (h *Handler) handleCPUStream(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w) || !methodGet(w, r) {
		return
	}
	if !h.cfg.CPU.Enabled {
		helpers.WriteError(w, http.StatusForbidden, "cpu metrics not enabled in metrics.toml", "")
		return
	}
	sse.Handler(h.heartbeatInterval, func(ctx context.Context, sw *sse.Writer) error {
		return h.streamSnapshot(ctx, sw, tailkit.EventCPU, func(ctx context.Context) (any, error) {
			return h.sampleCPU(ctx)
		})
	})(w, r)
}

func (h *Handler) handleMemoryStream(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w) || !methodGet(w, r) {
		return
	}
	if !h.cfg.Memory.Enabled {
		helpers.WriteError(w, http.StatusForbidden, "memory metrics not enabled in metrics.toml", "")
		return
	}
	sse.Handler(h.heartbeatInterval, func(ctx context.Context, sw *sse.Writer) error {
		return h.streamSnapshot(ctx, sw, tailkit.EventMemory, func(ctx context.Context) (any, error) {
			return h.sampleMemory(ctx)
		})
	})(w, r)
}

func (h *Handler) handleNetworkStream(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w) || !methodGet(w, r) {
		return
	}
	if !h.cfg.Network.Enabled {
		helpers.WriteError(w, http.StatusForbidden, "network metrics not enabled in metrics.toml", "")
		return
	}
	sse.Handler(h.heartbeatInterval, func(ctx context.Context, sw *sse.Writer) error {
		return h.streamSnapshot(ctx, sw, tailkit.EventNetwork, func(ctx context.Context) (any, error) {
			return h.sampleNetwork(ctx)
		})
	})(w, r)
}

func (h *Handler) handleProcessesStream(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w) || !methodGet(w, r) {
		return
	}
	if !h.cfg.Processes.Enabled {
		helpers.WriteError(w, http.StatusForbidden, "process metrics not enabled in metrics.toml", "")
		return
	}
	sse.Handler(h.heartbeatInterval, func(ctx context.Context, sw *sse.Writer) error {
		return h.streamSnapshot(ctx, sw, tailkit.EventProcesses, func(ctx context.Context) (any, error) {
			return h.sampleProcesses(ctx)
		})
	})(w, r)
}

func (h *Handler) handleAllStream(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w) || !methodGet(w, r) {
		return
	}
	sse.Handler(h.heartbeatInterval, func(ctx context.Context, sw *sse.Writer) error {
		return h.streamSnapshot(ctx, sw, tailkit.EventAll, func(ctx context.Context) (any, error) {
			return h.sampleAll(ctx)
		})
	})(w, r)
}

func (h *Handler) streamSnapshot(ctx context.Context, sw *sse.Writer, eventName string, sample func(context.Context) (any, error)) error {
	send := func() error {
		data, err := sample(ctx)
		if err != nil {
			return err
		}
		return sw.Send(eventName, data)
	}

	if err := send(); err != nil {
		return err
	}

	ticker := time.NewTicker(h.streamInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := send(); err != nil {
				return err
			}
		}
	}
}

func (h *Handler) handlePortsAvailable(w http.ResponseWriter, r *http.Request) {
	if !methodGet(w, r) {
		return
	}
	if !h.guard(w) {
		return
	}
	if !h.cfg.Ports.Enabled {
		helpers.WriteError(w, http.StatusServiceUnavailable, "ports metrics not enabled in metrics.toml", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, map[string]bool{"available": true})
}

func (h *Handler) handlePorts(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w) || !methodGet(w, r) {
		return
	}
	if !h.cfg.Ports.Enabled {
		helpers.WriteError(w, http.StatusServiceUnavailable, "ports metrics not enabled in metrics.toml", "")
		return
	}
	ports, err := h.samplePorts(r.Context())
	if err != nil {
		h.logger.Error("metrics: ports snapshot failed", zap.Error(err))
		helpers.WriteError(w, http.StatusInternalServerError, "failed to get port metrics", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, ports)
}

func (h *Handler) handlePortsStream(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w) || !methodGet(w, r) {
		return
	}
	if !h.cfg.Ports.Enabled {
		helpers.WriteError(w, http.StatusServiceUnavailable, "ports metrics not enabled in metrics.toml", "")
		return
	}
	sse.Handler(h.heartbeatInterval, func(ctx context.Context, sw *sse.Writer) error {

		current, err := h.samplePorts(ctx)
		if err != nil {
			return err
		}
		if err := sw.Send(tailkit.EventPortsSnapshot, types.PortEvent{
			Kind:  "snapshot",
			Ports: current,
		}); err != nil {
			return err
		}

		ticker := time.NewTicker(h.streamInterval)
		defer ticker.Stop()
		previous := current
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				current, err := h.samplePorts(ctx)
				if err != nil {
					return err
				}
				for _, port := range diffPorts(previous, current) {
					if err := sw.Send("port.bound", types.PortEvent{Kind: "bound", Port: port}); err != nil {
						return err
					}
				}
				for _, port := range diffPorts(current, previous) {
					if err := sw.Send("port.released", types.PortEvent{Kind: "released", Port: port}); err != nil {
						return err
					}
				}
				previous = current
			}
		}
	})(w, r)
}

func diffPorts(before, after []types.ListenPort) []types.ListenPort {
	known := make(map[string]struct{}, len(before))
	for _, port := range before {
		known[portIdentity(port)] = struct{}{}
	}

	var diff []types.ListenPort
	for _, port := range after {
		if _, ok := known[portIdentity(port)]; ok {
			continue
		}
		diff = append(diff, port)
	}
	sort.Slice(diff, func(i, j int) bool {
		if diff[i].Port != diff[j].Port {
			return diff[i].Port < diff[j].Port
		}
		if diff[i].Addr != diff[j].Addr {
			return diff[i].Addr < diff[j].Addr
		}
		if diff[i].PID != diff[j].PID {
			return diff[i].PID < diff[j].PID
		}
		return diff[i].Process < diff[j].Process
	})
	return diff
}

func portIdentity(port types.ListenPort) string {
	return fmt.Sprintf("%s|%d|%s|%d", port.Addr, port.Port, port.Proto, port.PID)
}
