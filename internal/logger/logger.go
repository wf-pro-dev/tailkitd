package logger

import (
	"fmt"
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/wf-pro-dev/tailkitd/internal/config"
)

type BuiltLoggers struct {
	App *zap.Logger
	API *zap.Logger
}

func Build(cfg config.LoggingConfig) (BuiltLoggers, error) {
	appLevel, err := parseLevel(cfg.App.Level)
	if err != nil {
		return BuiltLoggers{}, err
	}
	apiLevel, err := parseLevel(cfg.API.Level)
	if err != nil {
		return BuiltLoggers{}, err
	}

	appCore, err := buildAppCore(cfg.App, appLevel)
	if err != nil {
		return BuiltLoggers{}, err
	}

	appLogger := zap.New(appCore, zap.AddCaller())
	apiLogger := zap.New(zapcore.NewNopCore())
	if cfg.API.Enabled {
		apiCore, err := buildAPICore(cfg.API, apiLevel)
		if err != nil {
			return BuiltLoggers{}, err
		}
		apiLogger = zap.New(apiCore).With(zap.String("service", "tailkitd"))
	}

	return BuiltLoggers{
		App: appLogger,
		API: apiLogger,
	}, nil
}

func buildAppCore(cfg config.AppLogConfig, level zapcore.Level) (zapcore.Core, error) {
	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.TimeKey = "ts"
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	if cfg.Format == "text" {
		encoderCfg = zap.NewDevelopmentEncoderConfig()
		encoderCfg.TimeKey = "ts"
		encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder
		return zapcore.NewCore(
			zapcore.NewConsoleEncoder(encoderCfg),
			zapcore.Lock(os.Stderr),
			level,
		), nil
	}
	return zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderCfg),
		zapcore.Lock(os.Stderr),
		level,
	), nil
}

func buildAPICore(cfg config.APILogConfig, level zapcore.Level) (zapcore.Core, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0755); err != nil {
		return nil, fmt.Errorf("logger: create api log directory for %s: %w", cfg.Path, err)
	}
	writer := &lumberjack.Logger{
		Filename:   cfg.Path,
		MaxSize:    cfg.Rotation.MaxSizeMB,
		MaxBackups: cfg.Rotation.MaxBackups,
		MaxAge:     cfg.Rotation.MaxAgeDays,
		Compress:   cfg.Rotation.Compress,
	}
	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.TimeKey = "ts"
	encoderCfg.LevelKey = "level"
	encoderCfg.MessageKey = "msg"
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	return zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderCfg),
		zapcore.AddSync(writer),
		level,
	), nil
}

func parseLevel(raw string) (zapcore.Level, error) {
	var level zapcore.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		return zapcore.InfoLevel, fmt.Errorf("logger: invalid level %q: %w", raw, err)
	}
	return level, nil
}
