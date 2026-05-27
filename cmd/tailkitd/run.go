package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/daemon"
	"go.uber.org/zap"
	"tailscale.com/client/local"

	tailkit "github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkitd/internal/access"
	"github.com/wf-pro-dev/tailkitd/internal/admin"
	"github.com/wf-pro-dev/tailkitd/internal/api"
	"github.com/wf-pro-dev/tailkitd/internal/config"
	"github.com/wf-pro-dev/tailkitd/internal/docker"
	"github.com/wf-pro-dev/tailkitd/internal/exec"
	"github.com/wf-pro-dev/tailkitd/internal/files"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
	"github.com/wf-pro-dev/tailkitd/internal/identity"
	"github.com/wf-pro-dev/tailkitd/internal/invite"
	tailkitdlogger "github.com/wf-pro-dev/tailkitd/internal/logger"
	"github.com/wf-pro-dev/tailkitd/internal/metrics"
	"github.com/wf-pro-dev/tailkitd/internal/services"
	"github.com/wf-pro-dev/tailkitd/internal/systemd"
	"github.com/wf-pro-dev/tailkitd/internal/tools"
	"github.com/wf-pro-dev/tailkitd/internal/vars"
)

const toolsDir = "/etc/tailkitd/tools"

func cmdRun() int {
	bootstrapLogger, _ := zap.NewDevelopment()
	defer bootstrapLogger.Sync() //nolint:errcheck

	logCfg, err := config.LoadLoggingConfig(context.Background(), bootstrapLogger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tailkitd: failed to load logging config: %v\n", err)
		return 1
	}

	loggers, err := tailkitdlogger.Build(logCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tailkitd: failed to initialise logger: %v\n", err)
		return 1
	}
	logger := serviceLogger(loggers.App, "tailkitd", "main")
	apiLogger := loggers.API
	defer logger.Sync()    //nolint:errcheck
	defer apiLogger.Sync() //nolint:errcheck

	logger.Info("tailkitd starting", zap.String("env", os.Getenv("TAILKITD_ENV")))
	logger.Debug("logging configured",
		zap.String("app_format", logCfg.App.Format),
		zap.String("app_level", logCfg.App.Level),
		zap.Bool("api_enabled", logCfg.API.Enabled),
		zap.String("api_format", logCfg.API.Format),
		zap.String("api_path", logCfg.API.Path),
		zap.String("api_level", logCfg.API.Level),
	)

	// ── Step 2: Resolve this node's own Tailscale hostname. ──────────────────
	tsnetHostname, err := resolveHostname(logger)
	if err != nil {
		logger.Error("fatal: could not determine node hostname", zap.Error(err))
		return 1
	}
	logger.Debug("resolved node hostname", zap.String("tsnet_hostname", tsnetHostname))

	// ── Step 3: Load all integration configs. ────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := identity.EnsureArtifactKeys(ctx, logger); err != nil {
		logger.Error("fatal: failed to initialize artifact identity", zap.Error(err))
		return 1
	}

	filesCfg, err := config.LoadFilesConfig(ctx, logger)
	if err != nil {
		logger.Error("fatal: files config invalid", zap.Error(err))
		return 1
	}
	varsCfg, err := config.LoadVarsConfig(ctx, logger)
	if err != nil {
		logger.Error("fatal: vars config invalid", zap.Error(err))
		return 1
	}
	dockerCfg, err := config.LoadDockerConfig(ctx, logger)
	if err != nil {
		logger.Error("fatal: docker config invalid", zap.Error(err))
		return 1
	}
	systemdCfg, err := config.LoadSystemdConfig(ctx, logger)
	if err != nil {
		logger.Error("fatal: systemd config invalid", zap.Error(err))
		return 1
	}
	metricsCfg, err := config.LoadMetricsConfig(ctx, logger)
	if err != nil {
		logger.Error("fatal: metrics config invalid", zap.Error(err))
		return 1
	}
	hostManager, err := config.NewHostManager(ctx, config.HostConfigPath, tsnetHostname, logger)
	if err != nil {
		logger.Error("fatal: host config invalid", zap.Error(err))
		return 1
	}
	defer hostManager.Close() //nolint:errcheck

	logger.Info("integrations enabled",
		zap.Bool("files", filesCfg.Enabled),
		zap.Bool("vars", varsCfg.Enabled),
		zap.Bool("docker", dockerCfg.Enabled),
		zap.Bool("systemd", systemdCfg.Enabled),
		zap.Bool("metrics", metricsCfg.Enabled),
	)
	logger.Debug("host config loaded", zap.String("host_name", hostManager.Get().Name))

	// ── Step 4: Build per-subsystem child loggers. ───────────────────────────
	toolsLogger := serviceLogger(loggers.App, "tailkitd/tools", "tools")
	execLogger := serviceLogger(loggers.App, "tailkitd/exec", "exec")
	filesLogger := serviceLogger(loggers.App, "tailkitd/files", "files")
	varsLogger := serviceLogger(loggers.App, "tailkitd/vars", "vars")
	dockerLogger := serviceLogger(loggers.App, "tailkitd/docker", "docker")
	systemdLogger := serviceLogger(loggers.App, "tailkitd/systemd", "systemd")
	metricsLogger := serviceLogger(loggers.App, "tailkitd/metrics", "metrics")
	servicesLogger := serviceLogger(loggers.App, "tailkitd/services", "services")
	accessLogger := serviceLogger(loggers.App, "tailkitd/access", "access")

	// ── Step 5: Build tool registry (for GET /tools). ────────────────────────
	toolsRegistry := tools.NewRegistry(toolsDir, toolsLogger)

	outsiderRegistry, err := services.NewRegistry(ctx, services.DefaultServicesDir, servicesLogger)
	if err != nil {
		logger.Error("fatal: failed to start services registry", zap.Error(err))
		return 1
	}
	defer outsiderRegistry.Close() //nolint:errcheck

	accessRegistry, err := access.NewRegistry(ctx, access.DefaultAccessDir, accessLogger)
	if err != nil {
		logger.Error("fatal: failed to start access registry", zap.Error(err))
		return 1
	}
	defer accessRegistry.Close() //nolint:errcheck

	// ── Step 6: Build exec subsystem. ────────────────────────────────────────
	execRegistry, err := exec.NewRegistry(ctx, toolsDir, execLogger)
	if err != nil {
		logger.Error("fatal: failed to start exec registry", zap.Error(err))
		return 1
	}

	execJobs := exec.NewJobStore(execLogger)
	execJobs.StartEviction(ctx)
	execHandler := exec.NewHandler(execRegistry, execJobs, execLogger)

	// ── Step 7: Build files handler. ─────────────────────────────────────────
	filesHandler := files.NewHandler(filesCfg, execRegistry, execJobs, filesLogger)

	// ── Step 8: Build vars handler. ──────────────────────────────────────────
	varsStore := vars.NewStore("/etc/tailkitd/vars", varsLogger)
	varsHandler := vars.NewHandler(varsCfg, varsStore, varsLogger)

	// ── Step 9: Build docker handler. ────────────────────────────────────────
	dockerClient, err := docker.NewClient(ctx, dockerLogger)
	if err != nil {
		logger.Error("fatal: failed to start docker client", zap.Error(err))
		return 1
	}
	dockerHandler := docker.NewHandler(dockerCfg, dockerClient, execJobs, dockerLogger)

	// ── Step 10: Build metrics handler. ────────────────────────────────────────
	metricsHandler := metrics.NewHandler(metricsCfg, metricsLogger)

	// ── Step 11: Build systemd handler. ──────────────────────────────────────
	systemdClient, err := systemd.NewClient(ctx, systemdCfg, systemdLogger)
	if err != nil {
		logger.Error("fatal: failed to start systemd client", zap.Error(err))
		return 1
	}
	systemdHandler := systemd.NewHandler(systemdClient, execJobs, systemdLogger)

	hostname, err := os.Hostname()
	if err != nil {
		logger.Error("fatal: could not determine hostname", zap.Error(err))
		return 1
	}
	// ── Step 12: Start tsnet server. ──────────────────────────────────────────
	srv, err := tailkit.NewServer(tailkit.ServerConfig{
		Hostname: tsnetHostname,
		AuthKey:  os.Getenv("TS_AUTHKEY"),
		StateDir: "/var/lib/tailkitd",
	})
	if err != nil {
		logger.Error("fatal: failed to start tsnet server", zap.Error(err))
		return 1
	}
	defer srv.Close()

	logger.Info("tsnet server started", zap.String("hostname", tsnetHostname))

	localClient, err := srv.LocalClient()
	if err != nil {
		logger.Error("fatal: failed to create tailscale local client", zap.Error(err))
		return 1
	}
	status, err := localClient.Status(ctx)
	if err != nil {
		logger.Error("fatal: failed to read tailscale status", zap.Error(err))
		return 1
	}
	adminState, err := admin.BootstrapState(ctx, tsnetHostname, status, srv.HTTPClient(), logger)
	if err != nil {
		logger.Error("fatal: failed to initialize admin state", zap.Error(err))
		return 1
	}

	// ── Step 13: Wire router. ────────────────────────────────────────────────
	mux := http.NewServeMux()
	var handler http.Handler = mux
	handler = helpers.RequestLogger(apiLogger)(handler)
	handler = tailkit.AuthMiddleware(srv)(handler)

	hostHandler := &api.HostHandler{
		LocalClient: localClient,
		HostManager: hostManager,
		AdminState:  adminState,
	}
	servicesHandler := &api.ServicesHandler{
		Outsiders: outsiderRegistry,
		Tools:     toolsRegistry,
	}
	identityHandler := &api.IdentityHandler{
		NodeHostname: tsnetHostname,
	}
	inviteStore, err := invite.NewStore(invite.ClaimsStorePath)
	if err != nil {
		logger.Error("fatal: failed to initialize invite claim store", zap.Error(err))
		return 1
	}
	inviteClaimHandler := &api.InviteClaimHandler{
		Store: inviteStore,
	}
	adminHandler := &api.AdminHandler{
		Hostname:       tsnetHostname,
		HostConfig:     hostManager,
		HostConfigPath: config.HostConfigPath,
		Services:       outsiderRegistry,
		ServicesDir:    services.DefaultServicesDir,
		AdminState:     adminState,
		AdminFencePath: admin.AdminFencePath,
		AccessRegistry: accessRegistry,
		Promoter:       api.NewHTTPPromotionClient(srv.HTTPClient()),
	}

	mux.Handle("/host", hostHandler)
	mux.Handle("/identity/pubkey", identityHandler)
	mux.Handle("/services/claim", inviteClaimHandler)
	mux.Handle("/services", servicesHandler)
	mux.Handle("/admin/", adminHandler)
	mux.Handle("/tools", toolsRegistry.Handler())
	execHandler.Register(mux)
	filesHandler.Register(mux)
	varsHandler.Register(mux)
	dockerHandler.Register(mux)
	metricsHandler.Register(mux)
	systemdHandler.Register(mux)

	type Health struct {
		TailkitName string `json:"tailkit_name"`
		Hostname    string `json:"hostname"`
		TailkitIP   string `json:"tailkit_ip"`
		HostIP      string `json:"host_ip"`
	}

	ip4, _ := srv.TailscaleIPs()

	hostIP := os.Getenv("HOST_TAILSCALE_IP")
	if hostIP == "" {
		logger.Error("fatal: could not determine host IP", zap.Error(err))
		return 1
	}

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		helpers.WriteJSON(w, http.StatusOK, Health{
			TailkitName: tsnetHostname,
			Hostname:    hostname,
			TailkitIP:   ip4.String(),
			HostIP:      hostIP,
		})
	})

	// ── Step 14: Start HTTP server in a goroutine. ───────────────────────────
	// We need the main goroutine free to send READY=1 and then wait for signals.
	addr := ":80"
	if p := os.Getenv("TAILKITD_PORT"); p != "" {
		addr = ":" + p
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("tailkitd listening",
			zap.String("addr", addr),
			zap.String("hostname", tsnetHostname),
		)
		if err := srv.ListenAndServe(addr, handler); err != nil && err != http.ErrServerClosed {
			serveErr <- err
		}
		close(serveErr)
	}()

	// ── Step 15: Notify systemd the service is ready. ────────────────────────
	// daemon.SdNotify is a no-op when NOTIFY_SOCKET is not set (i.e. not running
	// under systemd), so it is safe to call unconditionally.
	// This satisfies Type=notify in the unit file — systemd will not mark the
	// service as active until this call returns successfully.
	sent, err := daemon.SdNotify(false, daemon.SdNotifyReady)
	if err != nil {
		// Non-fatal: log and continue. The service will still run correctly;
		// systemd will just time out waiting for READY and restart us.
		logger.Warn("sd_notify READY failed", zap.Error(err))
	} else if sent {
		logger.Debug("sd_notify: READY=1 sent")
	}

	// ── Step 16: Watchdog loop. ───────────────────────────────────────────────
	// If WatchdogSec is set in the unit file, systemd expects a WATCHDOG=1 ping
	// at least once every WatchdogSec interval. We ping at half the interval.
	// SdWatchdogEnabled returns 0 when the watchdog is not configured — the
	// goroutine exits immediately in that case.
	go func() {
		interval, err := daemon.SdWatchdogEnabled(false)
		if err != nil || interval == 0 {
			return
		}
		logger.Debug("watchdog enabled", zap.Duration("interval", interval))
		ticker := time.NewTicker(interval / 2)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				daemon.SdNotify(false, daemon.SdNotifyWatchdog) //nolint:errcheck
			case <-ctx.Done():
				return
			}
		}
	}()

	// ── Step 17: Wait for shutdown signal or server error. ───────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)

	select {
	case sig := <-quit:
		logger.Info("received signal, shutting down", zap.String("signal", sig.String()))
	case err := <-serveErr:
		if err != nil {
			logger.Error("server exited with error", zap.Error(err))
			return 1
		}
	}

	// ── Step 18: Graceful shutdown. ───────────────────────────────────────────
	// Stop watchdog and bound work, then close the real tsnet listener.
	cancel()
	if err := srv.Close(); err != nil {
		logger.Warn("server close failed", zap.Error(err))
	}

	logger.Info("tailkitd stopped cleanly")
	return 0
}

func serviceLogger(logger *zap.Logger, service, component string) *zap.Logger {
	return logger.With(
		zap.String("service", service),
		zap.String("component", component),
	)
}

// resolveHostname returns the tsnet hostname tailkitd should register as.
// See hostname.go for the resolution logic and sanitizeHostname.
func resolveHostname(logger *zap.Logger) (string, error) {
	if h := os.Getenv("TAILKITD_HOSTNAME"); h != "" {
		logger.Debug("using explicit TAILKITD_HOSTNAME", zap.String("hostname", h))
		return h, nil
	}

	lc := &local.Client{}
	status, err := lc.Status(context.Background())
	if err == nil && status.Self != nil && status.Self.HostName != "" {
		h := "tailkitd-" + SanitizeHostname(status.Self.HostName)
		logger.Debug("resolved hostname from system tailscaled",
			zap.String("host_hostname", status.Self.HostName),
			zap.String("tsnet_hostname", h),
		)
		return h, nil
	}
	if err != nil {
		logger.Warn("could not reach system tailscaled, falling back to OS hostname",
			zap.Error(err),
		)
	}

	osHost, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("could not determine hostname from env, tailscaled, or OS: %w", err)
	}
	h := "tailkitd-" + SanitizeHostname(osHost)
	logger.Debug("resolved hostname from OS hostname",
		zap.String("os_hostname", osHost),
		zap.String("tsnet_hostname", h),
	)
	return h, nil
}
