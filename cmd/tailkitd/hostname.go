// hostname.go contains hostname resolution helpers that are used by both
// the tailscale build and tests. No build tag — always compiled.
package main

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
	"tailscale.com/client/local"
)

// sanitizeHostname strips characters not valid in a Tailscale MagicDNS
// hostname (lowercase alphanumeric and hyphens only). Leading/trailing
// hyphens are trimmed.
func SanitizeHostname(h string) string {
	h = strings.ToLower(h)
	var b strings.Builder
	for _, r := range h {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func resolveTailnetHostname(logger *zap.Logger) (string, error) {
	lc := &local.Client{}
	status, err := lc.Status(context.Background())
	if err != nil {
		return "", err
	}
	if status.Self == nil || status.Self.HostName == "" {
		return "", fmt.Errorf("tailscale self hostname unavailable")
	}
	logger.Debug("resolved hostname from system tailscaled",
		zap.String("host_hostname", status.Self.HostName),
	)
	return status.Self.HostName, nil
}

func daemonHostnameForTailnetHost(hostname string) string {
	return "tailkitd-" + SanitizeHostname(hostname)
}
