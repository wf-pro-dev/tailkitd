package api

import (
	"context"
	"net/http"

	"github.com/wf-pro-dev/tailkitd/internal/config"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
	"tailscale.com/ipn/ipnstate"
)

// HostResponse is the unified identity of a tailkitd node.
type HostResponse struct {
	Name         string            `json:"name"`
	Role         string            `json:"role"`
	Environment  string            `json:"environment"`
	Provider     string            `json:"provider"`
	InstanceType string            `json:"instance_type"`
	Tags         []string          `json:"tags"`
	Metadata     map[string]string `json:"metadata"`
	TSHostname   string            `json:"ts_hostname"`
	TSDNSName    string            `json:"ts_dns_name"`
	TSIPs        []string          `json:"ts_ips"`
	OS           string            `json:"os"`
	Arch         string            `json:"arch"`
	Online       bool              `json:"online"`
}

type statusClient interface {
	Status(context.Context) (*ipnstate.Status, error)
}

type HostHandler struct {
	LocalClient statusClient
	HostManager *config.HostManager
}

func (h *HostHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET")
		return
	}

	status, err := h.LocalClient.Status(r.Context())
	if err != nil {
		helpers.WriteError(w, http.StatusInternalServerError, "failed to get tailscale status", "")
		return
	}

	hostCfg := h.HostManager.Get()
	resp := HostResponse{
		Name:         hostCfg.Name,
		Role:         hostCfg.Role,
		Environment:  hostCfg.Environment,
		Provider:     hostCfg.Provider,
		InstanceType: hostCfg.InstanceType,
		Tags:         hostCfg.Tags,
		Metadata:     hostCfg.Metadata,
		TSHostname:   status.Self.HostName,
		TSDNSName:    status.Self.DNSName,
		OS:           status.Self.OS,
		Online:       true,
	}
	for _, ip := range status.Self.TailscaleIPs {
		resp.TSIPs = append(resp.TSIPs, ip.String())
	}
	if len(resp.TSIPs) == 0 {
		resp.TSIPs = []string{}
	}
	if status.Self.OS == "" {
		resp.OS = "unknown"
	}
	if status.Version != "" {
		resp.Arch = status.Version
	}
	if resp.Arch == "" {
		resp.Arch = "unknown"
	}

	helpers.WriteJSON(w, http.StatusOK, resp)
}
