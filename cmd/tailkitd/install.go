package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"

	"github.com/wf-pro-dev/tailkitd/internal/setup"
)

func cmdInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	authKey := fs.String("auth-key", "", "Tailscale auth key (required, or set TS_AUTHKEY env var)")
	hostname := fs.String("hostname", "", "Tailnet hostname for this node (default: system hostname)")
	fs.Parse(args)

	// Auth key: flag takes precedence over env var.
	key := *authKey
	if key == "" {
		key = os.Getenv("TS_AUTHKEY")
	}
	if key == "" {
		fmt.Fprintln(os.Stderr, "error: --auth-key or TS_AUTHKEY is required")
		os.Exit(1)
	}

	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "error: install must be run as root (use sudo)")
		os.Exit(1)
	}

	opts := setup.InstallOptions{
		AuthKey:  key,
		Hostname: *hostname,
		// Detection of Docker and systemd is automatic inside Install.
	}

	if err := setup.Install(opts); err != nil {
		fmt.Fprintf(os.Stderr, "install failed: %v\n", err)
		os.Exit(1)
	}
}

func cmdUninstall() {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "error: uninstall must be run as root (use sudo)")
		os.Exit(1)
	}
	if err := setup.Uninstall(); err != nil {
		fmt.Fprintf(os.Stderr, "uninstall failed: %v\n", err)
		os.Exit(1)
	}
}

func cmdStatus() {
	cmd := exec.Command("systemctl", "status", "tailkitd")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run() // exit code mirrors systemctl — don't mask it
	os.Exit(cmd.ProcessState.ExitCode())
}
