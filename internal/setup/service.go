package setup

import (
	"fmt"
	"os"
	"time"
)

// unitContent is the tailkitd.service unit file.
// Key decisions (documented in prior design):
//   - Type=notify: systemd waits for READY=1 before marking active
//   - WatchdogSec=30s: restarts if the process hangs without sending WATCHDOG=1
//   - ProtectSystem=strict + ReadWritePaths: filesystem jail
//   - EnvironmentFile: keeps TS_AUTHKEY out of the unit file itself
//   - StartLimitBurst=5: gives up after 5 rapid crashes in 120s
const unitContent = `[Unit]
Description=tailkitd node agent
Documentation=https://github.com/wf-pro-dev/tailkitd
After=network-online.target
Wants=network-online.target
ConditionPathExists=/etc/tailkitd

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
StartLimitBurst=5
StartLimitIntervalSec=120s

# Filesystem sandboxing
# The daemon may only write to its own config and state directories.
ProtectSystem=strict
ReadWritePaths=/etc/tailkitd /var/lib/tailkitd
PrivateTmp=true
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
`

// writeUnitFile writes the systemd service unit to disk.
// Unlike config files, this is always overwritten — the unit file
// is managed entirely by tailkitd and should never be manually edited.
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

// startService starts the service and waits up to 30 seconds for it
// to signal READY via sd_notify (the unit is Type=notify).
func startService() error {
	if err := run("systemctl", "start", "tailkitd"); err != nil {
		return fmt.Errorf("start tailkitd: %w", err)
	}

	// Poll for active state — systemctl start returns once the process
	// is spawned, not once it has signalled READY. We give it 30s.
	fmt.Print("  …  waiting for tailkitd to become active")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out, err := runOutput("systemctl", "is-active", "tailkitd")
		if err == nil && string(out) == "active\n" {
			fmt.Println(" ✓")
			return nil
		}
		fmt.Print(".")
		time.Sleep(1 * time.Second)
	}
	fmt.Println()
	return fmt.Errorf("tailkitd did not become active within 30s — check: journalctl -u tailkitd")
}

// disableService stops and disables the service.
func disableService() error {
	// Ignore errors — service may not be running.
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
