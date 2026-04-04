# Installation

tailkitd requires Tailscale to be installed and the node to be joined to a tailnet before running install.

---

## One-liner

```bash
curl -fsSL https://github.com/wf-pro-dev/tailkitd/releases/latest/download/install.sh | sudo sh -s -- --auth-key tskey-auth-xxxx
```

### Flags

| Flag | Description |
|---|---|
| `--auth-key <key>` | Tailscale auth key (required, or set `TS_AUTHKEY` env var) |
| `--hostname <name>` | Override the tailnet hostname (default: system hostname) |
| `--nosystemd` | Download the static no-cgo variant (no systemd journal support) |
| `--version <ver>` | Pin a specific release e.g. `v0.1.4` (default: latest) |

---

## What install does

1. Detects Docker and systemd on the node
2. Creates the `tailkitd` system user and group
3. Creates the directory layout under `/etc/tailkitd/` and `/var/lib/tailkitd/`
4. Writes skeleton config files for every detected integration (skips existing files)
5. Writes `/etc/tailkitd/env` with auth key and hostname
6. Copies the binary to `/usr/local/bin/tailkitd`
7. Writes the systemd unit file
8. Enables the service (`daemon-reload` + `systemctl enable`)
9. Runs `tailkitd verify` — aborts if any check fails
10. Starts the service and waits for `READY`

Re-running install is safe — existing config files and the env file are never overwritten.

---

## Env file

Install writes `/etc/tailkitd/env` which is loaded by the systemd unit. The file is owned `root:tailkitd` with mode `0640`.

| Variable | Description |
|---|---|
| `TS_AUTHKEY` | Tailscale auth key used by tsnet to join the tailnet |
| `TAILKITD_HOSTNAME` | Hostname tailkitd registers on the tailnet (prefixed `tailkitd-`) |
| `TAILKITD_ENV` | Set to `development` for human-readable logs, omit for JSON (default) |
| `TAILKITD_PORT` | HTTP listen port (default: `80`) |
| `HOST_TAILSCALE_IP` | Tailscale IPv4 of the host node, written at install time |

---

## nosystemd variant

The `nosystemd` build is a fully static binary with no CGO and no systemd journal dependency. Use it on nodes where the systemd integration is not needed, or on distributions without systemd.

```bash
curl -fsSL https://github.com/wf-pro-dev/tailkitd/releases/latest/download/install.sh | sudo sh -s -- \
  --auth-key tskey-auth-xxxx \
  --nosystemd
```

The installed binary is named `tailkitd-nosystemd`. All other behaviour is identical.

---

## Verify

Run at any time to validate the installation without modifying anything:

```bash
sudo tailkitd verify
```

Prints a structured report. Exits `0` if clean (warnings allowed), `1` if any error is found.

---

## Uninstall

```bash
sudo tailkitd uninstall
```

Stops and disables the service, removes the unit file and binary. Config files under `/etc/tailkitd/` and state under `/var/lib/tailkitd/` are preserved. Remove them manually if a full wipe is needed:

```bash
sudo rm -rf /etc/tailkitd /var/lib/tailkitd
```
