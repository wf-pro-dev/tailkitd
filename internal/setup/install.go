package setup

import (
	"fmt"
	"os"
)

// InstallOptions holds the parameters for a full node installation.
type InstallOptions struct {
	// AuthKey is the Tailscale auth key. Required.
	AuthKey string

	// Hostname overrides the node's Tailnet hostname.
	// If empty, the system hostname is used.
	Hostname string
}

// Install performs a full tailkitd installation on the current node.
//
// Steps (in order):
//  1. Detect Docker and systemd
//  2. Create system user (+ docker group membership if Docker present)
//  3. Create directory layout
//  4. Write skeleton config files (idempotent — skips existing files)
//  5. Write /etc/tailkitd/env (idempotent)
//  6. Copy binary to /usr/local/bin/tailkitd (atomic)
//  7. Write systemd unit file
//  8. Enable service (daemon-reload + systemctl enable)
//  9. Run verify — abort if any error
//  10. Start service and wait for READY
func Install(opts InstallOptions) error {
	step := func(n int, label string) {
		fmt.Printf("\nStep %d — %s\n", n, label)
	}

	step(1, "Detecting integrations")
	i := detectIntegrations()
	fmt.Printf("  •  Docker:  %v\n", i.Docker)
	fmt.Printf("  •  systemd: %v\n", i.Systemd)

	step(2, "Creating system user")
	if err := ensureUser(i.Docker); err != nil {
		return fmt.Errorf("step 2: %w", err)
	}

	step(3, "Creating directory layout")
	if err := ensureDirectories(); err != nil {
		return fmt.Errorf("step 3: %w", err)
	}

	step(4, "Writing integration configs")
	if err := writeConfigFiles(i); err != nil {
		return fmt.Errorf("step 4: %w", err)
	}
	if !i.Docker {
		fmt.Println("  –  docker.toml skipped (Docker not present)")
	}
	if !i.Systemd {
		fmt.Println("  –  systemd.toml skipped (systemd not PID 1)")
	}

	step(5, "Writing env file")
	if err := writeEnvFile(opts.AuthKey, opts.Hostname); err != nil {
		return fmt.Errorf("step 5: %w", err)
	}

	step(6, "Installing binary")
	if err := installBinary(); err != nil {
		return fmt.Errorf("step 6: %w", err)
	}
	fmt.Printf("  ✓  %s\n", binaryDst)

	step(7, "Writing systemd unit file")
	if err := writeUnitFile(); err != nil {
		return fmt.Errorf("step 7: %w", err)
	}

	step(8, "Enabling service")
	if err := enableService(); err != nil {
		return fmt.Errorf("step 8: %w", err)
	}
	fmt.Println("  ✓  tailkitd enabled for boot")

	step(9, "Verifying installation")
	report := Verify()
	report.Print(os.Stdout)
	if report.HasErrors() {
		return fmt.Errorf("verify failed with %d error(s) — service not started", report.Errors())
	}

	step(10, "Starting service")
	if err := startService(); err != nil {
		return fmt.Errorf("step 10: %w", err)
	}

	fmt.Println("\n✓  tailkitd installed successfully")
	fmt.Println("   Logs: journalctl -u tailkitd -f")
	fmt.Println("   Config: /etc/tailkitd/integrations/")
	return nil
}

// Uninstall stops, disables, and removes tailkitd from the current node.
// Config files and state are preserved — this is a service removal, not
// a full wipe. Run `rm -rf /etc/tailkitd /var/lib/tailkitd` manually
// if a full wipe is needed.
func Uninstall() error {
	fmt.Println("Stopping and disabling tailkitd…")
	if err := disableService(); err != nil {
		return err
	}

	fmt.Printf("Removing unit file %s…\n", unitFile)
	if err := removeUnitFile(); err != nil {
		return err
	}

	fmt.Printf("Removing binary %s…\n", binaryDst)
	if err := os.Remove(binaryDst); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove binary: %w", err)
	}

	fmt.Println("\n✓  tailkitd removed")
	fmt.Println("   Config preserved at /etc/tailkitd — remove manually if not needed")
	fmt.Println("   State preserved at /var/lib/tailkitd — remove manually if not needed")
	return nil
}
