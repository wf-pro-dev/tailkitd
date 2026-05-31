package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	tailkit "github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkitd/internal/access"
	"github.com/wf-pro-dev/tailkitd/internal/admin"
	"github.com/wf-pro-dev/tailkitd/internal/config"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
	"github.com/wf-pro-dev/tailkitd/internal/services"
	"github.com/wf-pro-dev/tailkitd/internal/state"
	"github.com/wf-pro-dev/tailkitd/internal/utils"
	"go.uber.org/zap"
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
	log.Println("promotion request", host, newFence)
	body, err := json.Marshal(map[string]int{"new_fence": newFence})
	if err != nil {
		return err
	}
	log.Println("promotion request body", string(body))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+host+"/admin/internal/accept-promotion", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	log.Println("promotion response", req.Method, req.URL.Path)
	defer resp.Body.Close()
	log.Println("promotion response", resp.StatusCode)
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
	AccessRegistry *access.Registry
	Epoch          *state.Epoch
	Promoter       promotionClient
	Logger         *zap.Logger
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
	case r.URL.Path == "/admin/access/grants":
		h.requireAdminKey(h.handleAccessGrants)(w, r)
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
		if isMutationMethod(r.Method) && h.Epoch != nil {
			callerEpoch, err := parseCallerEpoch(r.Header.Get("X-State-Epoch"))
			if err != nil {
				helpers.WriteError(w, http.StatusBadRequest, err.Error(), "")
				return
			}
			if err := h.Epoch.Validate(callerEpoch); err != nil {
				w.Header().Set("X-State-Epoch", strconv.FormatInt(h.Epoch.Current(), 10))
				helpers.WriteError(w, http.StatusConflict, err.Error(), "")
				return
			}
		}
		if h.AccessRegistry != nil && h.AccessRegistry.HasAnyGrants() {
			caller, ok := tailkit.CallerFromContext(r.Context())
			if !ok || caller.UserLogin == "" {
				helpers.WriteError(w, http.StatusForbidden, "caller identity unavailable", "")
				return
			}
			capability, target := h.requiredCapability(r)
			if capability != "" && !h.AccessRegistry.Allow(caller.UserLogin, capability, target) {
				helpers.WriteError(w, http.StatusForbidden, "insufficient privileges for this action", "")
				return
			}
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next(rec, r)
		if isMutationMethod(r.Method) && h.Epoch != nil && rec.status >= 200 && rec.status < 300 {
			if nextEpoch, err := h.Epoch.Increment(); err == nil {
				rec.Header().Set("X-State-Epoch", strconv.FormatInt(nextEpoch, 10))
			}
		}
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func isMutationMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func parseCallerEpoch(raw string) (int64, error) {
	if raw == "" {
		return 0, fmt.Errorf("missing required X-State-Epoch header")
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid X-State-Epoch header")
	}
	return n, nil
}

func (h *AdminHandler) requiredCapability(r *http.Request) (string, string) {
	switch {
	case r.URL.Path == "/admin/transfer":
		return "admin.transfer", "*"
	case r.URL.Path == "/admin/access/grants":
		return "access.write", "*"
	case r.URL.Path == "/admin/files/write":
		return "host.write", "*"
	case strings.HasSuffix(r.URL.Path, "/config"):
		return "host.write", "*"
	case strings.Contains(r.URL.Path, "/services/"):
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/admin/hosts/"), "/")
		if len(parts) >= 3 {
			return "service.write", parts[2]
		}
	}
	return "", ""
}

func (h *AdminHandler) handleAccessGrants(w http.ResponseWriter, r *http.Request) {
	handler := &AccessHandler{
		Registry: h.AccessRegistry,
		Dir:      access.DefaultAccessDir,
	}
	if h.AccessRegistry == nil {
		helpers.WriteError(w, http.StatusInternalServerError, "access registry unavailable", "")
		return
	}
	handler.ServeHTTP(w, r)
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
	selfName := h.HostConfig.SelfName()
	req.SetDefaults(selfName)
	if req.Name != selfName {
		helpers.WriteError(w, http.StatusBadRequest, "host name must match local tailscale peer", "")
		return
	}

	hosts := h.HostConfig.All()
	replaced := false
	for i := range hosts {
		if hosts[i].Name == selfName {
			hosts[i] = req
			replaced = true
			break
		}
	}
	if !replaced {
		hosts = append(hosts, req)
	}

	var path = h.hostConfigPath()
	if err := config.WriteHostFile(path, hosts); err != nil {
		helpers.WriteError(w, http.StatusConflict, "transaction failed: "+err.Error(), "")
		return
	}
	if err := h.HostConfig.Reload(); err != nil {
		helpers.WriteError(w, http.StatusInternalServerError, "failed to reload host config", "")
		return
	}
	helpers.WriteJSON(w, http.StatusOK, map[string]string{"status": "success"})
}

func (h *AdminHandler) pushService(w http.ResponseWriter, r *http.Request, name string) {
	var req services.OutsiderServiceConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logger().Warn("admin: invalid service payload", helpers.WithRequestLogFields(r.Context(), append(h.callerFields(r.Context(), []zap.Field{
			zap.String("service", name),
		}), zap.Error(err)))...)
		helpers.WriteError(w, http.StatusBadRequest, "invalid json body", "")
		return
	}
	req.Name = name
	if err := req.Validate(); err != nil {
		h.logger().Warn("admin: service validation failed", helpers.WithRequestLogFields(r.Context(), append(h.callerFields(r.Context(), []zap.Field{
			zap.String("service", name),
			zap.String("runtime", req.Runtime),
		}), zap.Error(err)))...)
		helpers.WriteError(w, http.StatusBadRequest, "validation failed: "+err.Error(), "")
		return
	}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(&req); err != nil {
		h.logger().Error("admin: encode service config failed", helpers.WithRequestLogFields(r.Context(), append(h.callerFields(r.Context(), []zap.Field{
			zap.String("service", name),
		}), zap.Error(err)))...)
		helpers.WriteError(w, http.StatusInternalServerError, "failed to encode service config", "")
		return
	}
	path := services.FilePath(h.servicesDir(), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		h.logger().Error("admin: create services dir failed", helpers.WithRequestLogFields(r.Context(), append(h.callerFields(r.Context(), []zap.Field{
			zap.String("service", name),
			zap.String("dir", filepath.Dir(path)),
		}), zap.Error(err)))...)
		helpers.WriteError(w, http.StatusInternalServerError, "failed to create services dir", "")
		return
	}
	if err := utils.AtomicWrite(path, buf.Bytes(), 0o644); err != nil {
		h.logger().Error("admin: service config write failed", helpers.WithRequestLogFields(r.Context(), append(h.callerFields(r.Context(), []zap.Field{
			zap.String("service", name),
			zap.String("path", path),
		}), zap.Error(err)))...)
		helpers.WriteError(w, http.StatusConflict, "transaction failed: "+err.Error(), "")
		return
	}
	if err := h.Services.Reload(); err != nil {
		h.logger().Error("admin: services reload failed after update", helpers.WithRequestLogFields(r.Context(), append(h.callerFields(r.Context(), []zap.Field{
			zap.String("service", name),
		}), zap.Error(err)))...)
		helpers.WriteError(w, http.StatusInternalServerError, "failed to reload services", "")
		return
	}
	h.logger().Info("admin: service config updated", helpers.WithRequestLogFields(r.Context(), h.callerFields(r.Context(), []zap.Field{
		zap.String("service", name),
		zap.String("runtime", req.Runtime),
		zap.String("path", path),
	}))...)
	helpers.WriteJSON(w, http.StatusOK, map[string]string{"status": "success"})
}

func (h *AdminHandler) deleteService(w http.ResponseWriter, r *http.Request, name string) {
	path := services.FilePath(h.servicesDir(), name)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			h.logger().Warn("admin: service delete target missing", helpers.WithRequestLogFields(r.Context(), h.callerFields(r.Context(), []zap.Field{
				zap.String("service", name),
				zap.String("path", path),
			}))...)
			helpers.WriteError(w, http.StatusNotFound, "service not found", "")
			return
		}
		h.logger().Error("admin: service delete failed", helpers.WithRequestLogFields(r.Context(), append(h.callerFields(r.Context(), []zap.Field{
			zap.String("service", name),
			zap.String("path", path),
		}), zap.Error(err)))...)
		helpers.WriteError(w, http.StatusInternalServerError, "delete failed", "")
		return
	}
	if err := h.Services.Reload(); err != nil {
		h.logger().Error("admin: services reload failed after delete", helpers.WithRequestLogFields(r.Context(), append(h.callerFields(r.Context(), []zap.Field{
			zap.String("service", name),
		}), zap.Error(err)))...)
		helpers.WriteError(w, http.StatusInternalServerError, "failed to reload services", "")
		return
	}
	h.logger().Info("admin: service config deleted", helpers.WithRequestLogFields(r.Context(), h.callerFields(r.Context(), []zap.Field{
		zap.String("service", name),
		zap.String("path", path),
	}))...)
	w.WriteHeader(http.StatusNoContent)
}

func (h *AdminHandler) transferAdmin(w http.ResponseWriter, r *http.Request) {
	log.Println("transferAdmin", r.Method, r.Method != http.MethodPost)
	if r.Method != http.MethodPost {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use POST")
		return
	}
	log.Println("transferAdmin", h.AdminState.IsAdmin())
	if !h.AdminState.IsAdmin() {
		h.logger().Warn("admin: transfer rejected because node is not admin", helpers.WithRequestLogFields(r.Context(), h.callerFields(r.Context(), nil))...)
		helpers.WriteError(w, http.StatusForbidden, "this node is not the admin", "")
		return
	}

	var req struct {
		TargetHost string `json:"target_host"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TargetHost == "" {
		fields := h.callerFields(r.Context(), nil)
		if err != nil {
			fields = append(fields, zap.Error(err))
		}
		h.logger().Warn("admin: invalid transfer payload", helpers.WithRequestLogFields(r.Context(), fields)...)
		helpers.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}
	log.Println("transferAdmin", req.TargetHost)

	currentFence, err := admin.GetFenceToken()
	if err != nil {
		h.logger().Error("admin: read fence token failed", helpers.WithRequestLogFields(r.Context(), append(h.callerFields(r.Context(), nil), zap.Error(err)))...)
		helpers.WriteError(w, http.StatusInternalServerError, "failed to read fence token", "")
		return
	}
	newFence := currentFence + 1
	log.Println("transferAdmin currentFence", currentFence, "newFence", newFence)
	if err := utils.AtomicWrite(h.adminFencePath(), []byte(fmt.Sprintf("%d\n", newFence)), 0o600); err != nil {
		h.logger().Error("admin: write new fence token failed", helpers.WithRequestLogFields(r.Context(), append(h.callerFields(r.Context(), []zap.Field{
			zap.String("target_host", req.TargetHost),
			zap.Int("previous_fence", currentFence),
			zap.Int("new_fence", newFence),
		}), zap.Error(err)))...)
		helpers.WriteError(w, http.StatusInternalServerError, "failed to update fence token", "")
		return
	}
	log.Println("transferAdmin promoter", req.TargetHost, newFence)
	if err := h.Promoter.Promote(r.Context(), req.TargetHost, newFence); err != nil {
		if rollbackErr := utils.AtomicWrite(h.adminFencePath(), []byte(fmt.Sprintf("%d\n", currentFence)), 0o600); rollbackErr != nil {
			h.logger().Error("admin: fence rollback failed after transfer failure", helpers.WithRequestLogFields(r.Context(), append(h.callerFields(r.Context(), []zap.Field{
				zap.String("target_host", req.TargetHost),
				zap.Int("rollback_fence", currentFence),
			}), zap.Error(rollbackErr)))...)
		}
		h.logger().Warn("admin: transfer failed", helpers.WithRequestLogFields(r.Context(), append(h.callerFields(r.Context(), []zap.Field{
			zap.String("target_host", req.TargetHost),
			zap.Int("previous_fence", currentFence),
			zap.Int("new_fence", newFence),
		}), zap.Error(err)))...)
		helpers.WriteError(w, http.StatusConflict, "transfer failed: "+err.Error(), "")
		return
	}
	log.Println("transferAdmin promoter success", req.TargetHost, newFence)

	h.AdminState.SetAdmin(false)
	h.logger().Info("admin: transfer completed", helpers.WithRequestLogFields(r.Context(), h.callerFields(r.Context(), []zap.Field{
		zap.String("target_host", req.TargetHost),
		zap.Int("previous_fence", currentFence),
		zap.Int("new_fence", newFence),
	}))...)
	helpers.WriteJSON(w, http.StatusOK, map[string]string{
		"status":    "success",
		"new_admin": req.TargetHost,
	})
}

func (h *AdminHandler) acceptPromotion(w http.ResponseWriter, r *http.Request) {
	log.Println("acceptPromotion", r.Method, r.Method != http.MethodPost)
	if r.Method != http.MethodPost {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use POST")
		return
	}
	var req struct {
		NewFence int `json:"new_fence"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NewFence < 0 {
		fields := h.callerFields(r.Context(), nil)
		if err != nil {
			fields = append(fields, zap.Error(err))
		}
		h.logger().Warn("admin: invalid promotion payload", helpers.WithRequestLogFields(r.Context(), fields)...)
		helpers.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}
	log.Println("acceptPromotion", req.NewFence)
	if err := utils.AtomicWrite(h.adminFencePath(), []byte(fmt.Sprintf("%d\n", req.NewFence)), 0o600); err != nil {
		h.logger().Error("admin: write promoted fence token failed", helpers.WithRequestLogFields(r.Context(), append(h.callerFields(r.Context(), []zap.Field{
			zap.Int("new_fence", req.NewFence),
		}), zap.Error(err)))...)
		helpers.WriteError(w, http.StatusInternalServerError, "failed to write fence token", "")
		return
	}
	h.AdminState.SetAdmin(true)
	h.logger().Info("admin: promotion accepted", helpers.WithRequestLogFields(r.Context(), h.callerFields(r.Context(), []zap.Field{
		zap.Int("new_fence", req.NewFence),
	}))...)
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
		h.logger().Warn("admin: invalid atomic write payload", helpers.WithRequestLogFields(r.Context(), append(h.callerFields(r.Context(), nil), zap.Error(err)))...)
		helpers.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}
	if !h.allowedAtomicWritePath(req.Path) {
		h.logger().Warn("admin: atomic write rejected for non-allowlisted path", helpers.WithRequestLogFields(r.Context(), h.callerFields(r.Context(), []zap.Field{
			zap.String("path", req.Path),
		}))...)
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
			h.logger().Warn("admin: invalid base64 data for atomic write", helpers.WithRequestLogFields(r.Context(), append(h.callerFields(r.Context(), []zap.Field{
				zap.String("path", req.Path),
			}), zap.Error(err)))...)
			helpers.WriteError(w, http.StatusBadRequest, "invalid base64 data", "")
			return
		}
	default:
		h.logger().Warn("admin: invalid atomic write encoding", helpers.WithRequestLogFields(r.Context(), h.callerFields(r.Context(), []zap.Field{
			zap.String("path", req.Path),
			zap.String("encoding", req.Encoding),
		}))...)
		helpers.WriteError(w, http.StatusBadRequest, "invalid encoding", "")
		return
	}

	perm := os.FileMode(req.Perm)
	if perm == 0 {
		perm = 0o644
	}
	if err := os.MkdirAll(filepath.Dir(req.Path), 0o755); err != nil {
		h.logger().Error("admin: create parent dir for atomic write failed", helpers.WithRequestLogFields(r.Context(), append(h.callerFields(r.Context(), []zap.Field{
			zap.String("path", req.Path),
			zap.String("dir", filepath.Dir(req.Path)),
		}), zap.Error(err)))...)
		helpers.WriteError(w, http.StatusInternalServerError, "failed to create parent dir", "")
		return
	}
	if err := utils.AtomicWrite(req.Path, data, perm); err != nil {
		h.logger().Error("admin: atomic write failed", helpers.WithRequestLogFields(r.Context(), append(h.callerFields(r.Context(), []zap.Field{
			zap.String("path", req.Path),
			zap.Uint32("perm", uint32(perm)),
			zap.String("encoding", req.Encoding),
			zap.Int("size", len(data)),
		}), zap.Error(err)))...)
		helpers.WriteError(w, http.StatusConflict, "transaction failed: "+err.Error(), "")
		return
	}
	h.logger().Info("admin: atomic write completed", helpers.WithRequestLogFields(r.Context(), h.callerFields(r.Context(), []zap.Field{
		zap.String("path", req.Path),
		zap.Uint32("perm", uint32(perm)),
		zap.String("encoding", req.Encoding),
		zap.Int("size", len(data)),
	}))...)
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

func (h *AdminHandler) logger() *zap.Logger {
	if h.Logger != nil {
		return h.Logger.With(zap.String("component", "admin"))
	}
	return zap.NewNop()
}

func (h *AdminHandler) callerFields(ctx context.Context, fields []zap.Field) []zap.Field {
	caller, ok := tailkit.CallerFromContext(ctx)
	if !ok {
		return fields
	}
	fields = append(fields,
		zap.String("caller", caller.Hostname),
		zap.String("caller_login", caller.UserLogin),
	)
	return fields
}
