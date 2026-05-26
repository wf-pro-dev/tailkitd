package api

import (
	"context"
	"net/http"

	"github.com/wf-pro-dev/tailkit/types"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
	"github.com/wf-pro-dev/tailkitd/internal/services"
)

type ServiceResponse struct {
	Name          string   `json:"name"`
	Source        string   `json:"source"`
	Runtime       string   `json:"runtime"`
	Priority      string   `json:"priority"`
	Tags          []string `json:"tags"`
	ExpectedPorts []uint16 `json:"expected_ports"`
	SystemdUnit   string   `json:"systemd_unit,omitempty"`
	BinaryPath    string   `json:"binary_path,omitempty"`
	PidFile       string   `json:"pid_file,omitempty"`
	ToolVersion   string   `json:"tool_version,omitempty"`
	ToolTsHost    string   `json:"tool_ts_host,omitempty"`
}

type outsiderLister interface {
	ListServices() []services.OutsiderServiceConfig
}

type toolLister interface {
	List(context.Context) ([]types.Tool, error)
}

type ServicesHandler struct {
	Outsiders outsiderLister
	Tools     toolLister
}

func (h *ServicesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET")
		return
	}

	unified := make([]ServiceResponse, 0)
	for _, cfg := range h.Outsiders.ListServices() {
		unified = append(unified, ServiceResponse{
			Name:          cfg.Name,
			Source:        "outsider",
			Runtime:       cfg.Runtime,
			Priority:      cfg.Priority,
			Tags:          cfg.Tags,
			ExpectedPorts: cfg.ExpectedPorts,
			SystemdUnit:   cfg.SystemdUnit,
			BinaryPath:    cfg.BinaryPath,
			PidFile:       cfg.PidFile,
		})
	}

	tools, err := h.Tools.List(r.Context())
	if err != nil {
		helpers.WriteError(w, http.StatusInternalServerError, "failed to list tools", "")
		return
	}
	for _, tool := range tools {
		unified = append(unified, ServiceResponse{
			Name:          tool.Name,
			Source:        "tool",
			Runtime:       "tsnet",
			Priority:      "normal",
			Tags:          []string{},
			ExpectedPorts: []uint16{},
			ToolVersion:   tool.Version,
			ToolTsHost:    tool.TsnetHost,
		})
	}

	helpers.WriteJSON(w, http.StatusOK, unified)
}
