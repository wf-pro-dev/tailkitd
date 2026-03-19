package setup

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
)

const (
	daemonUser  = "tailkitd"
	daemonGroup = "tailkitd"

	binaryDst    = "/usr/local/bin/tailkitd"
	configDir    = "/etc/tailkitd"
	integrDir    = "/etc/tailkitd/integrations"
	toolsDir     = "/etc/tailkitd/tools"
	stateDir     = "/var/lib/tailkitd"
	recvDir      = "/var/lib/tailkitd/recv"
	envFile      = "/etc/tailkitd/env"
	unitFile     = "/etc/systemd/system/tailkitd.service"
)

// ensureUser creates the tailkitd system user and group if they do not exist.
// If Docker is present, the user is also added to the docker group.
func ensureUser(withDocker bool) error {
	// Create group if absent.
	if err := exec.Command("getent", "group", daemonGroup).Run(); err != nil {
		if err := run("groupadd", "--system", daemonGroup); err != nil {
			return fmt.Errorf("create group %s: %w", daemonGroup, err)
		}
	}

	// Create user if absent.
	if err := exec.Command("getent", "passwd", daemonUser).Run(); err != nil {
		if err := run("useradd",
			"--system",
			"--no-create-home",
			"--shell", "/usr/sbin/nologin",
			"--gid", daemonGroup,
			daemonUser,
		); err != nil {
			return fmt.Errorf("create user %s: %w", daemonUser, err)
		}
	}

	// Docker group membership lets tailkitd reach /var/run/docker.sock
	// without running as root.
	if withDocker {
		if err := run("usermod", "-aG", "docker", daemonUser); err != nil {
			return fmt.Errorf("add %s to docker group: %w", daemonUser, err)
		}
	}

	return nil
}

// ensureDirectories creates the full directory tree owned by tailkitd.
func ensureDirectories() error {
	u, err := user.Lookup(daemonUser)
	if err != nil {
		return fmt.Errorf("lookup user %s: %w", daemonUser, err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)

	dirs := []struct {
		path string
		mode os.FileMode
	}{
		{configDir, 0755},
		{integrDir, 0755},
		{toolsDir, 0755},
		{stateDir, 0700}, // tsnet state — tighter permissions
		{recvDir, 0755},
	}

	for _, d := range dirs {
		if err := os.MkdirAll(d.path, d.mode); err != nil {
			return fmt.Errorf("mkdir %s: %w", d.path, err)
		}
		if err := os.Chown(d.path, uid, gid); err != nil {
			return fmt.Errorf("chown %s: %w", d.path, err)
		}
	}
	return nil
}

// writeConfigFiles writes skeleton TOML configs for every integration.
// Docker and systemd configs are only written when the respective
// runtime was detected at install time.
// All writes are idempotent: existing files are never overwritten,
// so re-running install after operator edits is safe.
func writeConfigFiles(i integrations) error {
	files := []struct {
		path    string
		content string
	}{
		{filepath.Join(integrDir, "metrics.toml"), skeletonMetrics},
		{filepath.Join(integrDir, "files.toml"), skeletonFiles},
		{filepath.Join(integrDir, "vars.toml"), skeletonVars},
	}

	if i.Docker {
		files = append(files, struct {
			path    string
			content string
		}{filepath.Join(integrDir, "docker.toml"), skeletonDocker})
	}

	if i.Systemd {
		files = append(files, struct {
			path    string
			content string
		}{filepath.Join(integrDir, "systemd.toml"), skeletonSystemd})
	}

	for _, f := range files {
		if err := writeIfAbsent(f.path, []byte(f.content), 0644); err != nil {
			return err
		}
	}
	return nil
}

// writeEnvFile writes /etc/tailkitd/env with the auth key and hostname.
// Mode 0640, owned root:tailkitd so only root and the daemon can read it.
// Idempotent: existing file is not overwritten.
func writeEnvFile(authKey, hostname string) error {
	if hostname == "" {
		var err error
		hostname, err = os.Hostname()
		if err != nil {
			return fmt.Errorf("resolve hostname: %w", err)
		}
	}

	content := fmt.Sprintf("TS_AUTHKEY=%s\nTAILKITD_HOSTNAME=%s\nTAILKITD_ENV=production\n",
		authKey, hostname)

	if _, err := os.Stat(envFile); err == nil {
		// Already exists — do not overwrite. Auth key rotation is the
		// operator's responsibility after first install.
		fmt.Printf("  ↩  %s already exists, skipping\n", envFile)
		return nil
	}

	if err := atomicWrite(envFile, []byte(content), 0640); err != nil {
		return err
	}

	// Chown to root:tailkitd so the daemon user can read it.
	g, err := user.LookupGroup(daemonGroup)
	if err != nil {
		return fmt.Errorf("lookup group %s: %w", daemonGroup, err)
	}
	gid, _ := strconv.Atoi(g.Gid)
	return os.Chown(envFile, 0, gid)
}

// installBinary copies the running binary to /usr/local/bin/tailkitd
// using an atomic rename so the running daemon (if any) is not disrupted.
func installBinary() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve binary path: %w", err)
	}

	src, err := os.ReadFile(self)
	if err != nil {
		return fmt.Errorf("read binary: %w", err)
	}

	if err := atomicWrite(binaryDst, src, 0755); err != nil {
		return fmt.Errorf("write binary: %w", err)
	}
	return nil
}

// writeIfAbsent writes content to path only when the file does not already exist.
// Uses atomicWrite so partial writes are never left on disk.
func writeIfAbsent(path string, content []byte, mode os.FileMode) error {
	if _, err := os.Stat(path); err == nil {
		fmt.Printf("  ↩  %s already exists, skipping\n", path)
		return nil
	}
	if err := atomicWrite(path, content, mode); err != nil {
		return err
	}
	fmt.Printf("  ✓  %s\n", path)
	return nil
}

// atomicWrite writes data to path via a temp file in the same directory,
// then renames it into place. The rename is atomic on any POSIX filesystem,
// matching the invariant used by tailkitd's own file writes.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tailkitd-install-*")
	if err != nil {
		return fmt.Errorf("atomicWrite %s: create temp: %w", path, err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename

	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("atomicWrite %s: chmod: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("atomicWrite %s: write: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("atomicWrite %s: close: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("atomicWrite %s: rename: %w", path, err)
	}
	return nil
}

// run executes a command, routing its output to stdout/stderr.
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
