package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/wf-pro-dev/tailkitd/internal/setup"
)

func newInstallerCmd() *cobra.Command {
	var authKey string
	var hostname string

	cmd := &cobra.Command{
		Use:    "install-system",
		Short:  "Install tailkitd on this node",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			key := authKey
			if key == "" {
				key = os.Getenv("TS_AUTHKEY")
			}
			if key == "" {
				return fmt.Errorf("--auth-key or TS_AUTHKEY is required")
			}
			if os.Geteuid() != 0 {
				return fmt.Errorf("install-system must be run as root (use sudo)")
			}

			return setup.Install(setup.InstallOptions{
				AuthKey:  key,
				Hostname: hostname,
			})
		},
	}

	cmd.Flags().StringVar(&authKey, "auth-key", "", "Tailscale auth key (required, or set TS_AUTHKEY env var)")
	cmd.Flags().StringVar(&hostname, "hostname", "", "Tailnet hostname for this node (default: system hostname)")
	return cmd
}

func newUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove tailkitd from this node",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Geteuid() != 0 {
				return fmt.Errorf("uninstall must be run as root (use sudo)")
			}
			return setup.Uninstall()
		},
	}
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show service status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			statusCmd := exec.Command("systemctl", "status", "tailkitd")
			statusCmd.Stdout = os.Stdout
			statusCmd.Stderr = os.Stderr
			if err := statusCmd.Run(); err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					return exitCodeError(exitErr.ExitCode())
				}
				return fmt.Errorf("systemctl status tailkitd: %w", err)
			}
			return nil
		},
	}
}
