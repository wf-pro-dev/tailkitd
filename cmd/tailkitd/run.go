package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"go.uber.org/zap"
	"tailscale.com/client/local"

	tailkit "github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkitd/internal/config"
	"github.com/wf-pro-dev/tailkitd/internal/exec"
	"github.com/wf-pro-dev/tailkitd/internal/files"
	tailkitdlogger "github.com/wf-pro-dev/tailkitd/internal/logger"
	"github.com/wf-pro-dev/tailkitd/internal/tools"
	"github.com/wf-pro-dev/tailkitd/internal/vars"
)

const toolsDir = "/etc/tailkitd/tools"

func run() int {
	// ── Step 1: Logger first, before anything else. ───────────────────────────
	logger, err := tailkitdlogger.Build(os.Getenv("TAILKITD_ENV"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "tailkitd: failed to initialise logger: %v\n", err)
		return 1
	}
	defer logger.Sync() //nolint:errcheck

	logger.Info("tailkitd starting", zap.String("env", os.Getenv("TAILKITD_ENV")))

	// ── Step 2: Resolve this node's own Tailscale hostname. ──────────────────
	tsnetHostname, err := resolveHostname(logger)
	if err != nil {
		logger.Error("fatal: could not determine node hostname", zap.Error(err))
		return 1
	}
	logger.Info("resolved node hostname", zap.String("tsnet_hostname", tsnetHostname))

	// ── Step 3: Load all integration configs. ────────────────────────────────
	ctx := context.Background()

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

	logger.Info("integrations enabled",
		zap.Bool("files", filesCfg.Enabled),
		zap.Bool("vars", varsCfg.Enabled),
		zap.Bool("docker", dockerCfg.Enabled),
		zap.Bool("systemd", systemdCfg.Enabled),
		zap.Bool("metrics", metricsCfg.Enabled),
	)

	// ── Step 4: Build per-subsystem child loggers. ───────────────────────────
	toolsLogger := logger.With(zap.String("component", "tools"))
	execLogger := logger.With(zap.String("component", "exec"))
	filesLogger := logger.With(zap.String("component", "files"))
	varsLogger := logger.With(zap.String("component", "vars"))
	dockerLogger := logger.With(zap.String("component", "docker"))   //nolint:unused
	systemdLogger := logger.With(zap.String("component", "systemd")) //nolint:unused
	metricsLogger := logger.With(zap.String("component", "metrics")) //nolint:unused

	_ = dockerLogger
	_ = systemdLogger
	_ = metricsLogger

	// ── Step 5: Build tool registry (for GET /tools). ────────────────────────
	toolsRegistry := tools.NewRegistry(toolsDir, toolsLogger)

	// ── Step 6: Build exec subsystem. ────────────────────────────────────────
	// The exec registry watches toolsDir with fsnotify — installs and upgrades
	// are reflected immediately without a tailkitd restart.
	execRegistry, err := exec.NewRegistry(ctx, toolsDir, execLogger)
	if err != nil {
		logger.Error("fatal: failed to start exec registry", zap.Error(err))
		return 1
	}
	execRunner := exec.NewRunner(execLogger)
	execJobs := exec.NewJobStore(execLogger)
	execJobs.StartEviction(ctx)
	execHandler := exec.NewHandler(execRegistry, execRunner, execJobs, execLogger)

	// ── Step 7: Build files handler. ───────────────────────────────────────────
	filesHandler := files.NewHandler(filesCfg, execRegistry, execRunner, execJobs, filesLogger)

	// ── Step 8: Build vars handler. ────────────────────────────────────────────
	varsStore := vars.NewStore("/etc/tailkitd/vars", varsLogger)
	varsHandler := vars.NewHandler(varsCfg, varsStore, varsLogger)

	// ── Step 9: Start tsnet server. ──────────────────────────────────────────
	srv, err := tailkit.NewServer(tailkit.ServerConfig{
		Hostname: tsnetHostname,
		AuthKey:  os.Getenv("TS_AUTHKEY"),
	})
	if err != nil {
		logger.Error("fatal: failed to start tsnet server", zap.Error(err))
		return 1
	}
	defer srv.Close()

	logger.Info("tsnet server started", zap.String("hostname", tsnetHostname))

	// ── Step 10: Wire router. ────────────────────────────────────────────────
	mux := http.NewServeMux()
	var handler http.Handler = mux
	handler = tailkit.AuthMiddleware(srv)(handler)

	// GET /tools — live tool registry listing
	mux.Handle("/tools", toolsRegistry.Handler())

	// POST /exec/{tool}/{cmd} — fire-and-forget exec
	// GET  /exec/jobs/{id}   — job poll
	execHandler.Register(mux)

	// POST /files             — write a file atomically
	// GET  /files?path=       — read a file
	// GET  /files?dir=        — list a directory
	filesHandler.Register(mux)

	// GET /vars — scope list
	// GET/PUT/DELETE /vars/{project}/{env}[/{key}] — var access
	varsHandler.Register(mux)

	// GET /health — unauthenticated liveness probe
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","hostname":%q}`, tsnetHostname)
	})

	// Future phases: /vars/, /integrations/{docker,systemd,metrics}/

	// ── Step 11: Serve. ─────────────────────────────────────────────────────
	addr := ":80"
	if p := os.Getenv("TAILKITD_PORT"); p != "" {
		addr = ":" + p
	}

	logger.Info("tailkitd listening",
		zap.String("addr", addr),
		zap.String("hostname", tsnetHostname),
	)

	if err := srv.ListenAndServe(addr, handler); err != nil && err != http.ErrServerClosed {
		logger.Error("server exited with error", zap.Error(err))
		return 1
	}

	logger.Info("tailkitd stopped cleanly")
	return 0
}

// resolveHostname returns the tsnet hostname tailkitd should register as.
// See hostname.go for the resolution logic and sanitizeHostname.
func resolveHostname(logger *zap.Logger) (string, error) {
	if h := os.Getenv("TAILKITD_HOSTNAME"); h != "" {
		logger.Info("using explicit TAILKITD_HOSTNAME", zap.String("hostname", h))
		return h, nil
	}

	lc := &local.Client{}
	status, err := lc.Status(context.Background())
	if err == nil && status.Self != nil && status.Self.HostName != "" {
		h := "tailkitd-" + SanitizeHostname(status.Self.HostName)
		logger.Info("resolved hostname from system tailscaled",
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
	logger.Info("resolved hostname from OS hostname",
		zap.String("os_hostname", osHost),
		zap.String("tsnet_hostname", h),
	)
	return h, nil
}
