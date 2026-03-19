// Package logger provides the single construction point for tailkitd's
// structured logger. Every other package receives a *zap.Logger as a
// constructor argument — this is the only file that calls zap.New*.
package logger

import (
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Build returns a *zap.Logger configured for the given environment.
//
//   - "development": human-readable console output, DEBUG level and above,
//     caller information included. Use when running tailkitd locally.
//   - anything else (including ""): JSON output to stderr, INFO level and above.
//     This is the production default.
//
// The environment is typically sourced from the TAILKITD_ENV environment
// variable in main.go:
//
//	logger, err := logger.Build(os.Getenv("TAILKITD_ENV"))
func Build(env string) (*zap.Logger, error) {
	if env == "development" {
		l, err := zap.NewDevelopment()
		if err != nil {
			return nil, fmt.Errorf("logger: failed to build development logger: %w", err)
		}
		return l, nil
	}

	cfg := zap.NewProductionConfig()
	// Write to stderr so log output is separate from any stdout the process emits.
	cfg.OutputPaths = []string{"stderr"}
	cfg.ErrorOutputPaths = []string{"stderr"}
	// Use ISO8601 timestamps for easier reading in log aggregators.
	cfg.EncoderConfig.TimeKey = "ts"
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	l, err := cfg.Build()
	if err != nil {
		return nil, fmt.Errorf("logger: failed to build production logger: %w", err)
	}
	return l, nil
}

// MustBuild is like Build but panics on error. Suitable for use in main()
// where a logger failure is unrecoverable.
func MustBuild(env string) *zap.Logger {
	l, err := Build(env)
	if err != nil {
		panic(err)
	}
	return l
}
