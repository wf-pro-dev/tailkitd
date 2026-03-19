package setup

import (
	"bytes"
	"os/exec"
	"strings"
)

// runOutput executes a command and returns its trimmed stdout.
// stderr is discarded — callers use the error return for failure signals.
func runOutput(name string, args ...string) (string, error) {
	var out bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}
