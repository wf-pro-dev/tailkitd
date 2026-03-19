package docker

import (
	"bytes"
	"context"
	"fmt"
	osexec "os/exec"
)

// runCommand executes a system command and returns combined stdout+stderr output.
// Used for compose operations that go through the docker CLI rather than the SDK.
func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := osexec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s %v: %w\noutput: %s", name, args, err, out.String())
	}
	return out.String(), nil
}
