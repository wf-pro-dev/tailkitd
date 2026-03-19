package exec

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"text/template"
	"time"

	"go.uber.org/zap"

	tailkit "github.com/wf-pro-dev/tailkit"
)

// Runner executes registered commands safely. It validates caller-supplied args
// against declared patterns, substitutes them into ExecParts per-slot using
// text/template, and runs the resolved command with exec.CommandContext.
//
// Invariant: the runner never joins ExecParts into a single string and splits
// it — substitution happens per-element so spaces inside arg values cannot
// be used to inject additional arguments.
type Runner struct {
	logger *zap.Logger
}

// NewRunner constructs a Runner with the given logger.
func NewRunner(logger *zap.Logger) *Runner {
	return &Runner{
		logger: logger.With(zap.String("component", "exec.runner")),
	}
}

// Run validates args, substitutes them into the command entry, and executes it.
// It always returns a JobResult — errors during execution are captured in the
// result rather than returned as Go errors, because the caller needs a
// serialisable result to store in the job store regardless of outcome.
//
// The context carries the job timeout (set by the caller from Command.Timeout).
// Cancelling ctx kills the subprocess.
func (r *Runner) Run(ctx context.Context, entry ExecEntry, args map[string]string) tailkit.JobResult {
	start := time.Now()

	// Check context before doing any work.
	if err := ctx.Err(); err != nil {
		return tailkit.JobResult{
			Status:   tailkit.JobStatusCancelled,
			ExitCode: -1,
			Error:    "cancelled before start",
		}
	}

	// 1. Validate all declared args.
	if err := validateArgs(entry.Command, args); err != nil {
		r.logger.Warn("exec arg validation failed",
			zap.String("tool", entry.Tool.Name),
			zap.String("command", entry.Command.Name),
			zap.Error(err),
		)
		return tailkit.JobResult{
			Status:   tailkit.JobStatusFailed,
			ExitCode: -1,
			Error:    err.Error(),
		}
	}

	// 2. Substitute args into each ExecParts element individually.
	// This is the critical invariant: we template per-slot, never join+split.
	resolvedParts, err := substituteArgs(entry.Command.ExecParts, args)
	if err != nil {
		r.logger.Error("exec template substitution failed",
			zap.String("tool", entry.Tool.Name),
			zap.String("command", entry.Command.Name),
			zap.Error(err),
		)
		return tailkit.JobResult{
			Status:   tailkit.JobStatusFailed,
			ExitCode: -1,
			Error:    fmt.Sprintf("template substitution failed: %v", err),
		}
	}

	// 3. Build and run the command.
	binary := resolvedParts[0]
	cmdArgs := resolvedParts[1:]

	r.logger.Info("exec starting",
		zap.String("tool", entry.Tool.Name),
		zap.String("command", entry.Command.Name),
		zap.String("binary", binary),
		zap.Strings("args", cmdArgs),
	)

	cmd := exec.CommandContext(ctx, binary, cmdArgs...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	durationMs := time.Since(start).Milliseconds()
	exitCode := 0

	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			// Context cancelled, binary not found, etc.
			exitCode = -1
		}
	}

	// Determine status: context cancelled vs normal non-zero exit vs success.
	status := tailkit.JobStatusCompleted
	errMsg := ""
	if ctx.Err() != nil {
		status = tailkit.JobStatusCancelled
		errMsg = "killed: context deadline exceeded"
	} else if runErr != nil {
		status = tailkit.JobStatusFailed
		errMsg = runErr.Error()
	}

	result := tailkit.JobResult{
		Status:     status,
		ExitCode:   exitCode,
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		DurationMs: durationMs,
		Error:      errMsg,
	}

	if runErr != nil {
		r.logger.Error("exec failed",
			zap.String("tool", entry.Tool.Name),
			zap.String("command", entry.Command.Name),
			zap.Int("exit_code", exitCode),
			zap.Int64("duration_ms", durationMs),
			zap.String("stderr", stderr.String()),
		)
	} else {
		r.logger.Info("exec completed",
			zap.String("tool", entry.Tool.Name),
			zap.String("command", entry.Command.Name),
			zap.Int("exit_code", exitCode),
			zap.Int64("duration_ms", durationMs),
		)
	}

	return result
}

// ─── Arg validation ───────────────────────────────────────────────────────────

// validateArgs checks that all required args are present and that all supplied
// values match their declared patterns.
func validateArgs(cmd tailkit.Command, supplied map[string]string) error {
	// Build a lookup of declared args.
	declared := make(map[string]tailkit.Arg, len(cmd.Args))
	for _, arg := range cmd.Args {
		declared[arg.Name] = arg
	}

	// Check required args are present.
	for _, arg := range cmd.Args {
		val, ok := supplied[arg.Name]
		if arg.Required && (!ok || val == "") {
			return fmt.Errorf("required arg %q is missing", arg.Name)
		}
	}

	// Validate pattern for every supplied value.
	for name, val := range supplied {
		arg, ok := declared[name]
		if !ok {
			return fmt.Errorf("unknown arg %q (not declared in command)", name)
		}
		if arg.Pattern == "" {
			continue
		}
		re, err := regexp.Compile(arg.Pattern)
		if err != nil {
			// Pattern was validated at Install time; this would be a bug.
			return fmt.Errorf("arg %q has invalid pattern %q: %v", name, arg.Pattern, err)
		}
		if !re.MatchString(val) {
			return fmt.Errorf("arg %q value %q does not match pattern %q", name, val, arg.Pattern)
		}
	}

	return nil
}

// ─── Template substitution ────────────────────────────────────────────────────

// substituteArgs renders each element of execParts as a text/template using
// args as the data map. Each element is rendered independently — this prevents
// space-in-value injection that would occur if we joined the parts first.
func substituteArgs(execParts []string, args map[string]string) ([]string, error) {
	resolved := make([]string, len(execParts))
	for i, part := range execParts {
		// Fast path: no template syntax.
		if !strings.Contains(part, "{{") {
			resolved[i] = part
			continue
		}

		tmpl, err := template.New("").Option("missingkey=error").Parse(part)
		if err != nil {
			return nil, fmt.Errorf("part[%d] %q: parse template: %w", i, part, err)
		}

		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, args); err != nil {
			return nil, fmt.Errorf("part[%d] %q: execute template: %w", i, part, err)
		}
		resolved[i] = buf.String()
	}
	return resolved, nil
}
