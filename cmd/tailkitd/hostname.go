// hostname.go contains hostname resolution helpers that are used by both
// the tailscale build and tests. No build tag — always compiled.
package main

import "strings"

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
