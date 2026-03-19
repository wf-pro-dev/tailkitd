package setup

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"go.uber.org/zap"

	"github.com/wf-pro-dev/tailkitd/internal/config"
)

// checkLevel indicates the severity of a verification result.
type checkLevel int

const (
	levelOK   checkLevel = iota
	levelWarn            // informational — does not block startup
	levelError           // blocks startup or indicates misconfiguration
)

// checkResult is a single line in the verify report.
type checkResult struct {
	level   checkLevel
	label   string
	message string
}

// Report is the full result of Verify().
type Report struct {
	checks []checkResult
}

// HasErrors returns true if any check failed with levelError.
func (r *Report) HasErrors() bool {
	for _, c := range r.checks {
		if c.level == levelError {
			return true
		}
	}
	return false
}

// HasWarnings returns true if any check produced a warning.
func (r *Report) HasWarnings() bool {
	for _, c := range r.checks {
		if c.level == levelWarn {
			return true
		}
	}
	return false
}

// Errors returns the count of error-level checks.
func (r *Report) Errors() int {
	n := 0
	for _, c := range r.checks {
		if c.level == levelError {
			n++
		}
	}
	return n
}

// Warnings returns the count of warning-level checks.
func (r *Report) Warnings() int {
	n := 0
	for _, c := range r.checks {
		if c.level == levelWarn {
			n++
		}
	}
	return n
}

// Print writes a human-readable report to w.
func (r *Report) Print(w io.Writer) {
	fmt.Fprintln(w, "\ntailkitd verify")
	fmt.Fprintln(w)
	for _, c := range r.checks {
		icon := "  ✓ "
		switch c.level {
		case levelWarn:
			icon = "  ⚠ "
		case levelError:
			icon = "  ✗ "
		}
		fmt.Fprintf(w, "%s %-18s %s\n", icon, c.label, c.message)
	}
}

func (r *Report) ok(label, msg string) {
	r.checks = append(r.checks, checkResult{levelOK, label, msg})
}

func (r *Report) warn(label, msg string) {
	r.checks = append(r.checks, checkResult{levelWarn, label, msg})
}

func (r *Report) fail(label, msg string) {
	r.checks = append(r.checks, checkResult{levelError, label, msg})
}

// Verify runs all validation checks and returns a Report.
// It uses the existing Load*Config functions from internal/config so
// every check uses exactly the same logic tailkitd uses at startup.
// No writes are performed — safe to run at any time.
func Verify() *Report {
	r := &Report{}
	// Discard logger output during verify — we surface errors via the report.
	logger := zap.NewNop()
	ctx := context.Background()

	checkBinary(r)
	checkEnvFile(r)
	checkDirectories(r)
	checkUnitFile(r)
	checkConfigMetrics(r, ctx, logger)
	checkConfigFiles(r, ctx, logger)
	checkConfigVars(r, ctx, logger)
	checkConfigDocker(r, ctx, logger)
	checkConfigSystemd(r, ctx, logger)

	return r
}

func checkBinary(r *Report) {
	info, err := os.Stat(binaryDst)
	if err != nil {
		r.fail("binary", fmt.Sprintf("%s not found", binaryDst))
		return
	}
	if info.Mode()&0111 == 0 {
		r.fail("binary", fmt.Sprintf("%s is not executable", binaryDst))
		return
	}

	out, err := runOutput(binaryDst, "--version")
	if err != nil {
		r.ok("binary", fmt.Sprintf("%s present", binaryDst))
		return
	}
	r.ok("binary", fmt.Sprintf("%s (%s)", binaryDst, string(out)))
}

func checkEnvFile(r *Report) {
	info, err := os.Stat(envFile)
	if err != nil {
		r.fail("env", fmt.Sprintf("%s not found", envFile))
		return
	}

	mode := info.Mode().Perm()
	if mode&0o004 != 0 {
		r.warn("env", fmt.Sprintf("%s is world-readable (expected 0640)", envFile))
	} else {
		r.ok("env", envFile)
	}

	// Check TS_AUTHKEY is set without reading its value.
	env, err := os.ReadFile(envFile)
	if err != nil {
		r.fail("env", fmt.Sprintf("cannot read %s: %v", envFile, err))
		return
	}
	hasKey := false
	for _, line := range splitLines(string(env)) {
		if len(line) > 11 && line[:11] == "TS_AUTHKEY=" && line[11:] != "" {
			hasKey = true
		}
	}
	if !hasKey {
		r.fail("env", "TS_AUTHKEY is missing or empty in "+envFile)
	}
}

func checkDirectories(r *Report) {
	dirs := []string{configDir, integrDir, toolsDir, stateDir, recvDir}
	for _, d := range dirs {
		if _, err := os.Stat(d); err != nil {
			r.fail("dirs", fmt.Sprintf("%s missing", d))
		}
	}
	if !r.HasErrors() {
		r.ok("dirs", "all required directories present")
	}
}

func checkUnitFile(r *Report) {
	if _, err := os.Stat(unitFile); err != nil {
		r.fail("unit file", fmt.Sprintf("%s not found", unitFile))
		return
	}
	// Verify systemctl can parse it.
	if err := exec.Command("systemctl", "cat", "tailkitd").Run(); err != nil {
		r.warn("unit file", "present but systemctl cannot read it — run: systemctl daemon-reload")
		return
	}
	r.ok("unit file", unitFile)
}

// checkConfigMetrics validates metrics.toml using the real config loader.
// Missing file → error (metrics is always required).
func checkConfigMetrics(r *Report, ctx context.Context, logger *zap.Logger) {
	cfg, err := config.LoadMetricsConfig(ctx, logger)
	if err != nil {
		r.fail("metrics.toml", err.Error())
		return
	}
	if !cfg.Enabled {
		r.fail("metrics.toml", "file missing — metrics.toml is required on all nodes")
		return
	}
	r.ok("metrics.toml", "valid")
}

// checkConfigFiles validates files.toml. Missing → error (always required).
func checkConfigFiles(r *Report, ctx context.Context, logger *zap.Logger) {
	cfg, err := config.LoadFilesConfig(ctx, logger)
	if err != nil {
		r.fail("files.toml", err.Error())
		return
	}
	if !cfg.Enabled {
		r.fail("files.toml", "file missing — files.toml is required on all nodes")
		return
	}
	r.ok("files.toml", fmt.Sprintf("valid — %d path rule(s)", len(cfg.Paths)))
}

// checkConfigVars validates vars.toml. Missing → error (always required).
func checkConfigVars(r *Report, ctx context.Context, logger *zap.Logger) {
	cfg, err := config.LoadVarsConfig(ctx, logger)
	if err != nil {
		r.fail("vars.toml", err.Error())
		return
	}
	if !cfg.Enabled {
		r.fail("vars.toml", "file missing — vars.toml is required on all nodes")
		return
	}
	r.ok("vars.toml", fmt.Sprintf("valid — %d scope(s)", len(cfg.Scopes)))
}

// checkConfigDocker validates docker.toml if Docker is present.
// Missing file when Docker socket exists → warn (operator should configure it).
// Missing file when Docker absent → ok (integration correctly disabled).
func checkConfigDocker(r *Report, ctx context.Context, logger *zap.Logger) {
	cfg, err := config.LoadDockerConfig(ctx, logger)
	if err != nil {
		r.fail("docker.toml", err.Error())
		return
	}
	if !cfg.Enabled {
		if dockerPresent() {
			r.warn("docker.toml", "Docker socket found but docker.toml is missing — integration disabled")
		} else {
			r.ok("docker.toml", "not present — Docker not installed (integration disabled)")
		}
		return
	}
	r.ok("docker.toml", "valid")
}

// checkConfigSystemd validates systemd.toml if systemd is PID 1.
// Missing file when systemd is PID 1 → warn (operator should configure it).
// Missing file when systemd absent → ok (integration correctly disabled).
func checkConfigSystemd(r *Report, ctx context.Context, logger *zap.Logger) {
	cfg, err := config.LoadSystemdConfig(ctx, logger)
	if err != nil {
		r.fail("systemd.toml", err.Error())
		return
	}
	if !cfg.Enabled {
		if systemdPresent() {
			r.warn("systemd.toml", "systemd is PID 1 but systemd.toml is missing — integration disabled")
		} else {
			r.ok("systemd.toml", "not present — systemd not running (integration disabled)")
		}
		return
	}
	r.ok("systemd.toml", "valid")
}

// splitLines splits s into non-empty trimmed lines.
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 {
				out = append(out, line)
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
