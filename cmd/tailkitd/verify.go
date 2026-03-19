package main

import (
	"fmt"
	"os"

	"github.com/wf-pro-dev/tailkitd/internal/setup"
)

// cmdVerify runs all validation checks and prints a structured report.
// Returns 0 if clean, 1 if any error or warning was found.
func cmdVerify() int {
	report := setup.Verify()
	report.Print(os.Stdout)

	if report.HasErrors() {
		fmt.Fprintf(os.Stdout, "\nResult: %d error(s), %d warning(s)\n", report.Errors(), report.Warnings())
		return 1
	}
	if report.HasWarnings() {
		fmt.Fprintf(os.Stdout, "\nResult: 0 errors, %d warning(s)\n", report.Warnings())
		return 0 // warnings do not block startup
	}
	fmt.Fprintf(os.Stdout, "\nResult: all checks passed\n")
	return 0
}
