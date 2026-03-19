package setup

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// unitContent is the tailkitd.service unit file.
const unitContent = `[Unit]
Description=tailkitd node agent
Documentation=https://github.com/wf-pro-dev/tailkitd
After=network-online.target
Wants=network-online.target
ConditionPathExists=/etc/tailkitd
StartLimitBurst=5
StartLimitIntervalSec=120s

[Service]
Type=notify
NotifyAccess=main
WatchdogSec=30s

User=tailkitd
Group=tailkitd

EnvironmentFile=/etc/tailkitd/env
ExecStart=/usr/local/bin/tailkitd run

Restart=on-failure
RestartSec=5s

# Filesystem sandboxing
ProtectSystem=strict
ReadWritePaths=/etc/tailkitd /var/lib/tailkitd
PrivateTmp=true
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
`

// writeUnitFile writes the systemd service unit to disk.
// Always overwritten — the unit file is managed by tailkitd install.
func writeUnitFile() error {
	if err := atomicWrite(unitFile, []byte(unitContent), 0644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}
	fmt.Printf("  ✓  %s\n", unitFile)
	return nil
}

// enableService reloads systemd and enables the unit for boot.
func enableService() error {
	if err := run("systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	if err := run("systemctl", "enable", "tailkitd"); err != nil {
		return fmt.Errorf("enable tailkitd: %w", err)
	}
	return nil
}

// startService starts the service and waits up to 60 seconds for it
// to reach active(running) state.
// With Type=notify, systemd marks the service active only after READY=1
// is received. tsnet may take several seconds to authenticate on first boot,
// so we give it more time than the default.
func startService() error {
	if err := run("systemctl", "start", "tailkitd"); err != nil {
		return fmt.Errorf("start tailkitd: %w", err)
	}

	fmt.Print("  …  waiting for tailkitd to become active")
	deadline := time.Now().Add(60 * time.Second)

	for time.Now().Before(deadline) {
		state, err := runOutput("systemctl", "is-active", "tailkitd")
		if err == nil {
			state = strings.TrimSpace(state)
			switch state {
			case "active":
				fmt.Println(" ✓")
				return nil
			case "failed":
				fmt.Println()
				return fmt.Errorf("tailkitd entered failed state — check: journalctl -u tailkitd")
				// "activating" means systemd is waiting for READY=1 — keep polling.
				// Any other state (deactivating, inactive) is also worth waiting on briefly.
			}
		}
		fmt.Print(".")
		time.Sleep(1 * time.Second)
	}

	// Timed out — but check one final time. The service may have become active
	// in the last polling interval.
	state, err := runOutput("systemctl", "is-active", "tailkitd")
	if err == nil && strings.TrimSpace(state) == "active" {
		fmt.Println(" ✓")
		return nil
	}

	fmt.Println()
	return fmt.Errorf("tailkitd did not become active within 60s — check: journalctl -u tailkitd")
}

// disableService stops and disables the service.
func disableService() error {
	run("systemctl", "stop", "tailkitd")    //nolint:errcheck
	run("systemctl", "disable", "tailkitd") //nolint:errcheck
	run("systemctl", "daemon-reload")       //nolint:errcheck
	return nil
}

// removeUnitFile deletes the service unit file.
func removeUnitFile() error {
	if err := os.Remove(unitFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit file: %w", err)
	}
	return nil
}
