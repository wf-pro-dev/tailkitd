# tailkitd

A node-level daemon for Tailscale-native infrastructure tooling. tailkitd runs on every Linux node in your tailnet and exposes a consistent HTTP API for files, environment variables, Docker, systemd, and host metrics — all gated by Tailscale identity and local config.

Tailscale handles the network. tailkitd handles the node.

---

## Capabilities

| Concern | tsnet / Tailscale | tailkitd |
|---|---|---|
| Node identity + auth | ✓ via `lc.WhoIs` | — |
| Peer discovery | ✓ via `lc.Status` | — |
| Host identity API | partial | ✓ `GET /host` |
| Unified service inventory | — | ✓ `GET /services` |
| Artifact identity API | — | ✓ `GET /identity/pubkey` |
| Invite token claim | — | ✓ `POST /services/claim` |
| Admin control plane | — | ✓ `/admin/*` (key + epoch gated) |
| Installed tool registry | — | ✓ `GET /tools` |
| File receive / read | — | ✓ `POST /files`, `GET /files` |
| Env var store | — | ✓ `GET /vars/{project}/{env}` |
| Docker / Compose / Swarm control | — | ✓ `/integrations/docker/...` |
| Systemd unit + journal control | — | ✓ `/integrations/systemd/...` |
| Host resource metrics | — | ✓ `/integrations/metrics/...` |

---

## Installation

Linux only for now.

```bash
curl -fsSL https://github.com/wf-pro-dev/tailkitd/releases/latest/download/install.sh | sudo sh -s -- --auth-key tskey-auth-xxxx
```

See [docs/install.md](docs/install.md) for flags, the `nosystemd` variant, and uninstall.

---

## Commands

| Command | Description |
|---|---|
| `tailkitd` | Start the daemon |
| `tailkitd uninstall` | Remove tailkitd from this node |
| `tailkitd verify` | Validate installation and config files |
| `tailkitd status` | Show service status |
| `tailkitd version` | Show build version |
| `tailkitd completion bash` | Generate shell completions |

---

## Logging

tailkitd uses [`go.uber.org/zap`](https://github.com/uber-go/zap).

- App/state logs go to `stderr` and are intended for `journalctl` / local debugging.
- API/request logs go to `/var/log/tailkitd/api.json.log` as JSON lines.
- Logging is configured in `/etc/tailkitd/logging.toml`.
- Environment variables from `/etc/tailkitd/env` can override `logging.toml`.

```bash
# Default
tailkitd

# Override app log level
TAILKITD_APP_LOG_LEVEL=debug tailkitd

# Override API log file
TAILKITD_API_LOG_PATH=/tmp/api.json.log tailkitd
```

Example `logging.toml`:

```toml
[app]
level = "info"
format = "text"

[api]
enabled = true
level = "info"
format = "json"
path = "/var/log/tailkitd/api.json.log"

[api.rotation]
max_size_mb = 100
max_backups = 10
max_age_days = 14
compress = true
```

App logs include `service` values like `tailkitd/files`, `tailkitd/docker`, `tailkitd/systemd`, `tailkitd/metrics`, and `tailkitd/vars`.

Level rules:

- `DEBUG`: startup details, config-load success, connection details, watcher churn, accepted async jobs, and other diagnostics that help during debugging but are too noisy for routine operations
- `INFO`: daemon lifecycle milestones and successful state-changing operations such as file writes, variable mutations, container actions, image pulls, compose actions, and systemd unit changes
- `WARN`: degraded-but-recoverable conditions, fallbacks, or partial failures where tailkitd continues serving
- `ERROR`: operation failures or subsystem failures that prevented the requested work from completing

Rule of thumb: log state changes and outcomes, not routine reads, polling, stream ticks, or internal bookkeeping.

Request logging policy:

- successful state-changing requests log at `INFO`
- successful read-only requests log at `INFO`
- high-volume, low-value read-only requests such as health checks, availability/config endpoints, stream endpoints, and job polling log at `DEBUG`
- `4xx` requests log at `WARN`
- `5xx` requests log at `ERROR`

Request logs include `request_id`, `method`, `path`, `status`, `duration_ms`, and `caller` when available.

---

## Docs

| Document | Description |
|---|---|
| [docs/install.md](docs/install.md) | Installation, env vars, nosystemd variant, uninstall |
| [docs/config.md](docs/config.md) | All config files with annotated examples |
| [docs/api.md](docs/api.md) | Full HTTP API reference |

---

## Module path

```
github.com/wf-pro-dev/tailkitd
```
