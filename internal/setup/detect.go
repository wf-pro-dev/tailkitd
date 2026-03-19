package setup

import (
	"os"
	"strings"
)

// integrations holds the result of probing the local machine at install time.
// Each field is set to true only when the corresponding runtime is confirmed present.
type integrations struct {
	// Docker is true when /var/run/docker.sock exists and is a socket.
	// When false, docker.toml is not written and the Docker integration
	// remains disabled (503) on first startup.
	Docker bool

	// Systemd is true when PID 1 is systemd.
	// When false, systemd.toml is not written and the systemd integration
	// remains disabled (503) on first startup.
	Systemd bool
}

// detectIntegrations probes the machine for Docker and systemd.
// Both checks are purely local and require no network access.
func detectIntegrations() integrations {
	return integrations{
		Docker:  dockerPresent(),
		Systemd: systemdPresent(),
	}
}

// dockerPresent returns true if the Docker socket exists.
// Does not attempt to connect — socket presence is sufficient
// to confirm Docker is installed and running.
func dockerPresent() bool {
	info, err := os.Stat("/var/run/docker.sock")
	if err != nil {
		return false
	}
	// Confirm it is actually a socket, not a regular file.
	return info.Mode()&os.ModeSocket != 0
}

// systemdPresent returns true if PID 1 is systemd.
// Reads /proc/1/exe — works on any Linux without external tools.
func systemdPresent() bool {
	target, err := os.Readlink("/proc/1/exe")
	if err != nil {
		return false
	}
	return strings.Contains(target, "systemd")
}
