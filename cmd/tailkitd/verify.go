package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/wf-pro-dev/tailkitd/internal/setup"
)

func newVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Validate installation and config",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report := setup.Verify()
			report.Print(cmd.OutOrStdout())

			if report.HasErrors() {
				fmt.Fprintf(cmd.OutOrStdout(), "\nResult: %d error(s), %d warning(s)\n", report.Errors(), report.Warnings())
				return exitCodeError(1)
			}
			if report.HasWarnings() {
				fmt.Fprintf(cmd.OutOrStdout(), "\nResult: 0 errors, %d warning(s)\n", report.Warnings())
				return nil
			}

			fmt.Fprintf(cmd.OutOrStdout(), "\nResult: all checks passed\n")
			return nil
		},
	}
}
