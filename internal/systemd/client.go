// Package systemd implements the tailkitd systemd integration, exposing
// unit control and journal access over the tailkitd HTTP API.
//
// The D-Bus connection is initialised once at startup and shared across all
// handlers. Missing systemd.toml → 503 on every endpoint.
package systemd

import (
	"context"
	"fmt"

	"github.com/coreos/go-systemd/v22/dbus"
	"go.uber.org/zap"

	"github.com/wf-pro-dev/tailkitd/internal/config"
)

// Client wraps the go-systemd D-Bus connection with the node's permission config.
type Client struct {
	conn   *dbus.Conn
	cfg    config.SystemdConfig
	logger *zap.Logger
}

// NewClient initialises a D-Bus connection to systemd using
// dbus.NewSystemConnectionContext. A connection failure is logged as a warning
// but does not prevent tailkitd from starting — Available() will return false
// and handlers will respond 503 until the connection is restored by a restart.
//
// If cfg.Enabled is false, NewClient returns a non-nil Client whose every
// handler responds 503 (invariant 5: missing config = 503).
func NewClient(ctx context.Context, cfg config.SystemdConfig, logger *zap.Logger) (*Client, error) {
	logger = logger.With(zap.String("component", "systemd"))

	if !cfg.Enabled {
		logger.Warn("systemd integration disabled — no systemd.toml")
		return &Client{cfg: cfg, logger: logger}, nil
	}

	conn, err := dbus.NewSystemConnectionContext(ctx)
	if err != nil {
		// Connection failure is non-fatal. In container environments D-Bus may
		// not be available at all. Log once here; Available() reports false.
		logger.Warn("failed to connect to systemd D-Bus",
			zap.Error(err),
			zap.String("hint", "is tailkitd running with D-Bus access?"),
		)
		return &Client{cfg: cfg, logger: logger}, nil
	}

	logger.Info("systemd D-Bus connection established")
	return &Client{conn: conn, cfg: cfg, logger: logger}, nil
}

// Available reports whether the D-Bus connection is live.
func (c *Client) Available(_ context.Context) bool {
	return c.cfg.Enabled && c.conn != nil
}

// Close releases the D-Bus connection.
func (c *Client) Close() {
	if c.conn != nil {
		c.conn.Close()
	}
}

// dbusConn returns the live D-Bus connection or an error if unavailable.
func (c *Client) dbusConn() (*dbus.Conn, error) {
	if c.conn == nil {
		return nil, fmt.Errorf("systemd D-Bus connection unavailable")
	}
	return c.conn, nil
}
