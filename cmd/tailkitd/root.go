package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:           "tailkitd",
		Short:         "tailkitd is a Tailscale-native node agent",
		Long:          "tailkitd runs on a Linux node and serves the tailkit API over tsnet with local integration controls for files, vars, Docker, systemd, and metrics.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		Version:       fmt.Sprintf("%s (%s, %s)", version, commit, date),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitCodeError(cmdRun())
		},
	}

	rootCmd.SetVersionTemplate("tailkitd {{.Version}}\n")
	rootCmd.AddCommand(
		newCompletionCmd(rootCmd),
		newVerifyCmd(),
		newStatusCmd(),
		newUninstallCmd(),
		newInstallerCmd(),
	)

	return rootCmd
}

type exitCodeError int

func (e exitCodeError) Error() string {
	return fmt.Sprintf("exit code %d", int(e))
}

func newCompletionCmd(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:       "completion [bash|zsh|fish|powershell]",
		Short:     "Generate shell completion scripts",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return root.GenBashCompletionV2(os.Stdout, true)
			case "zsh":
				return root.GenZshCompletion(os.Stdout)
			case "fish":
				return root.GenFishCompletion(os.Stdout, true)
			case "powershell":
				return root.GenPowerShellCompletionWithDesc(os.Stdout)
			default:
				return fmt.Errorf("unsupported shell %q", args[0])
			}
		},
	}
}
