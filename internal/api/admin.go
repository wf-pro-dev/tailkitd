package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/wf-pro-dev/tailkitd/internal/admin"
	"github.com/wf-pro-dev/tailkitd/internal/config"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
	"github.com/wf-pro-dev/tailkitd/internal/services"
	"github.com/wf-pro-dev/tailkitd/internal/utils"
)

type promotionClient interface {
	Promote(context.Context, string, int) error
}

type HTTPPromotionClient struct {
	client *http.Client
}

func NewHTTPPromotionClient(client *http.Client) HTTPPromotionClient {
	return HTTPPromotionClient{client: client}
}

func (c HTTPPromotionClient) Promote(ctx context.Context, host string, newFence int) error {
	body, err := json.Marshal(map[string]int{"new_fence": newFence})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+host+"/admin/internal/accept-promotion", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("promotion rejected with status %d", resp.StatusCode)
	}
	return nil
}

type AdminHandler struct {
	Hostname       string
	HostConfig     *config.HostManager
	HostConfigPath string
	Services       *services.Registry
	ServicesDir    string
	AdminState     *admin.State
	AdminFencePath string
	Promoter       promotionClient
}

func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/admin/internal/accept-promotion":
		h.acceptPromotion(w, r)
		return
	case r.URL.Path == "/admin/transfer":
		h.requireAdminKey(h.transferAdmin)(w, r)
		return
	case r.URL.Path == "/admin/files/write":
		h.requireAdminKey(h.atomicWriteFile)(w, r)
		return
	case strings.HasPrefix(r.URL.Path, "/admin/hosts/"):
		h.requireAdminKey(h.handleHostMutation)(w, r)
		return
	default:
		helpers.WriteError(w, http.StatusNotFound, "not found", "")
	}
}

func (h *AdminHandler) requireAdminKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		providedKey := r.Header.Get("X-Admin-Key")
		localKey, err := admin.GetAdminKey()
		if err != nil {
			helpers.WriteError(w, http.StatusForbidden, "admin key not configured on this node", "")
			return
		}
		if subtle.ConstantTimeCompare([]byte(providedKey), []byte(localKey)) != 1 {
			helpers.WriteError(w, http.StatusUnauthorized, "invalid admin key", "")
			return
		}
		next(w, r)
	}
}

func (h *AdminHandler) handleHostMutation(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/admin/hosts/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		helpers.WriteError(w, http.StatusNotFound, "not found", "")
		return
	}

	hostname := parts[0]
	if hostname != "me" && hostname != h.Hostname {
		helpers.WriteError(w, http.StatusBadRequest, "hostname does not match local node", "")
		return
	}

	if len(parts) == 2 && parts[1] == "config" && r.Method == http.MethodPost {
		h.pushHostConfig(w, r)
		return
	}
	if len(parts) == 3 && parts[1] == "services" {
		switch r.Method {
		case http.MethodPost:
			h.pushService(w, r, parts[2])
			return
		case http.MethodDelete:
			h.deleteService(w, r, parts[2])
			return
		}
	}

	helpers.WriteError(w, http.StatusNotFound, "not found", "")
}

func (h *AdminHandler) pushHostConfig(w http.ResponseWriter, r *http.Request) {
	var req config.HostConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		helpers.WriteError(w, http.StatusBadRequest, "invalid json body", "")
		return
	}
	current := h.HostConfig.Get()
	req.SetDefaults(current.Name)

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(&req); err != nil {
		helpers.WriteError(w, http.StatusInternalServerError, "failed to encode host config", "")
		return
	}
	if err := utils.AtomicWrite(h.hostConfigPath(), buf.Bytes(), 0o644); err != nil {
		helpers.WriteError(w, http.StatusConflict, "transaction failed: "+err.Error(), "")
		return
	}
	h.HostConfig.Replace(&req)
	helpers.WriteJSON(w, http.StatusOK, map[string]string{"status": "success"})
}

func (h *AdminHandler) pushService(w http.ResponseWriter, r *http.Request, name string) {
	var req services.OutsiderServiceConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		helpers.WriteError(w, http.StatusBadRequest, "invalid json body", "")
		return
	}
	req.Name = name
	if err := req.Validate(); err != nil {
		helpers.WriteError(w, http.StatusBadRequest, "validation failed: "+err.Error(), "")
		return
	}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(&req); err != nil {
		helpers.WriteError(w, http.StatusInternalServerError, "failed to encode service config", "")
		return
	}
	path := services.FilePath(h.servicesDir(), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		helpers.WriteError(w, http.StatusInternalServerError, "failed to create services dir", "")
		return
	}
	if err := utils.AtomicWrite(path, buf.Bytes(), 0o644); err != nil {
		helpers.WriteError(w, http.StatusConflict, "transaction failed: "+err.Error(), "")
		return
	}
	if err := h.Services.Reload(); err != nil {
		helpers.WriteError(w, http.StatusInternalServerError, "failed to reload services", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, map[string]string{"status": "success"})
}

func (h *AdminHandler) deleteService(w http.ResponseWriter, _ *http.Request, name string) {
	path := services.FilePath(h.servicesDir(), name)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			helpers.WriteError(w, http.StatusNotFound, "service not found", "")
			return
		}
		helpers.WriteError(w, http.StatusInternalServerError, "delete failed", "")
		return
	}
	if err := h.Services.Reload(); err != nil {
		helpers.WriteError(w, http.StatusInternalServerError, "failed to reload services", "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AdminHandler) transferAdmin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use POST")
		return
	}
	if !h.AdminState.IsAdmin() {
		helpers.WriteError(w, http.StatusForbidden, "this node is not the admin", "")
		return
	}

	var req struct {
		TargetHost string `json:"target_host"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TargetHost == "" {
		helpers.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}

	currentFence, err := admin.GetFenceToken()
	if err != nil {
		helpers.WriteError(w, http.StatusInternalServerError, "failed to read fence token", "")
		return
	}
	newFence := currentFence + 1
	if err := utils.AtomicWrite(h.adminFencePath(), []byte(fmt.Sprintf("%d\n", newFence)), 0o600); err != nil {
		helpers.WriteError(w, http.StatusInternalServerError, "failed to update fence token", "")
		return
	}
	if err := h.Promoter.Promote(r.Context(), req.TargetHost, newFence); err != nil {
		_ = utils.AtomicWrite(h.adminFencePath(), []byte(fmt.Sprintf("%d\n", currentFence)), 0o600)
		helpers.WriteError(w, http.StatusConflict, "transfer failed: "+err.Error(), "")
		return
	}

	h.AdminState.SetAdmin(false)
	helpers.WriteJSON(w, http.StatusOK, map[string]string{
		"status":    "success",
		"new_admin": req.TargetHost,
	})
}

func (h *AdminHandler) acceptPromotion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use POST")
		return
	}
	var req struct {
		NewFence int `json:"new_fence"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NewFence < 0 {
		helpers.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}
	if err := utils.AtomicWrite(h.adminFencePath(), []byte(fmt.Sprintf("%d\n", req.NewFence)), 0o600); err != nil {
		helpers.WriteError(w, http.StatusInternalServerError, "failed to write fence token", "")
		return
	}
	h.AdminState.SetAdmin(true)
	helpers.WriteJSON(w, http.StatusOK, map[string]string{"status": "success"})
}

func (h *AdminHandler) atomicWriteFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use POST")
		return
	}
	var req struct {
		Path     string `json:"path"`
		Perm     uint32 `json:"perm"`
		Encoding string `json:"encoding"`
		Data     string `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		helpers.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}
	if !h.allowedAtomicWritePath(req.Path) {
		helpers.WriteError(w, http.StatusForbidden, "path is not allowlisted", "")
		return
	}

	var data []byte
	var err error
	switch req.Encoding {
	case "", "utf-8":
		data = []byte(req.Data)
	case "base64":
		data, err = base64.StdEncoding.DecodeString(req.Data)
		if err != nil {
			helpers.WriteError(w, http.StatusBadRequest, "invalid base64 data", "")
			return
		}
	default:
		helpers.WriteError(w, http.StatusBadRequest, "invalid encoding", "")
		return
	}

	perm := os.FileMode(req.Perm)
	if perm == 0 {
		perm = 0o644
	}
	if err := os.MkdirAll(filepath.Dir(req.Path), 0o755); err != nil {
		helpers.WriteError(w, http.StatusInternalServerError, "failed to create parent dir", "")
		return
	}
	if err := utils.AtomicWrite(req.Path, data, perm); err != nil {
		helpers.WriteError(w, http.StatusConflict, "transaction failed: "+err.Error(), "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, map[string]string{"status": "success"})
}

func (h *AdminHandler) hostConfigPath() string {
	if h.HostConfigPath != "" {
		return h.HostConfigPath
	}
	return config.HostConfigPath
}

func (h *AdminHandler) servicesDir() string {
	if h.ServicesDir != "" {
		return h.ServicesDir
	}
	return services.DefaultServicesDir
}

func (h *AdminHandler) adminFencePath() string {
	if h.AdminFencePath != "" {
		return h.AdminFencePath
	}
	return admin.AdminFencePath
}

func (h *AdminHandler) allowedAtomicWritePath(path string) bool {
	if path == h.hostConfigPath() || path == h.adminFencePath() {
		return true
	}
	if strings.HasPrefix(path, h.servicesDir()+string(os.PathSeparator)) && strings.HasSuffix(path, ".toml") {
		return true
	}
	return false
}
