package docker

import (
	"context"
	"fmt"

	dockerclient "github.com/docker/docker/client"
	"go.uber.org/zap"
)

// Client wraps a *dockerclient.Client and holds the subsystem logger.
// It is initialized once at startup and shared across all Docker handlers.
type Client struct {
	docker *dockerclient.Client
	logger *zap.Logger
}

// NewClient creates and returns a Docker Client. It initializes the underlying
// Docker SDK client using environment-based configuration
// (DOCKER_HOST, DOCKER_TLS_VERIFY, etc.) with automatic API version negotiation,
// then pings the daemon to verify the socket is reachable.
//
// If the socket is unreachable the error is logged as a warning but is not
// fatal — handlers will return 503 for every request until the daemon becomes
// available. A nil *Client is never returned; the ping result only affects the
// startup log.
func NewClient(ctx context.Context, logger *zap.Logger) (*Client, error) {
	log := logger.With(zap.String("component", "docker"))

	dc, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("docker: failed to create client: %w", err)
	}

	// Ping to verify socket availability. Log a warning on failure but do not
	// prevent startup — per invariant 5, handlers return 503, not a crash.
	if _, err := dc.Ping(ctx); err != nil {
		log.Warn("docker socket unavailable",
			zap.String("socket", "/var/run/docker.sock"),
			zap.Error(err),
		)
	} else {
		log.Info("docker client connected",
			zap.String("socket", "/var/run/docker.sock"),
		)
	}

	return &Client{
		docker: dc,
		logger: log,
	}, nil
}

// Docker returns the underlying *dockerclient.Client for use by handlers and
// the Compose service initializer. Callers must not close or replace this
// instance.
func (c *Client) Docker() *dockerclient.Client {
	return c.docker
}

// Logger returns the subsystem logger (component=docker). Handlers and the
// Compose/Swarm sub-packages use this to attach additional fields without
// re-constructing a logger.
func (c *Client) Logger() *zap.Logger {
	return c.logger
}

// Close releases resources held by the underlying Docker client. Call this
// during graceful shutdown after all in-flight requests have completed.
func (c *Client) Close() error {
	return c.docker.Close()
}
