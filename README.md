# tailkitd

A node-level daemon for Tailscale-native infrastructure tooling. tailkitd runs on every node in your tailnet and exposes a consistent HTTP API for files, environment variables, Docker, systemd, and host metrics — all gated by Tailscale identity and local config.

Tailscale handles the network. tailkitd handles the node.

---

## Capabilities

| Concern | tsnet / Tailscale | tailkitd |
|---|---|---|
| Node identity + auth | ✓ via `lc.WhoIs` | — |
| Peer discovery | ✓ via `lc.Status` | — |
| Installed tool registry | — | ✓ `GET /tools` |
| File receive / read | — | ✓ `POST /files`, `GET /files` |
| Env var store | — | ✓ `GET /vars/{project}/{env}` |
| Docker / Compose / Swarm control | — | ✓ `/integrations/docker/...` |
| Systemd unit + journal control | — | ✓ `/integrations/systemd/...` |
| Host resource metrics | — | ✓ `/integrations/metrics/...` |

---

## Installation

```bash
curl -fsSL https://github.com/wf-pro-dev/tailkitd/releases/latest/download/install.sh | sudo sh -s -- --auth-key tskey-auth-xxxx
```

See [docs/install.md](docs/install.md) for flags, the `nosystemd` variant, and uninstall.

---

## Commands

| Command | Description |
|---|---|
| `tailkitd run` | Start the daemon (default) |
| `tailkitd install [flags]` | Install tailkitd on this node |
| `tailkitd uninstall` | Remove tailkitd from this node |
| `tailkitd verify` | Validate installation and config files |
| `tailkitd status` | Show service status |

---

## Logging

tailkitd uses [`go.uber.org/zap`](https://github.com/uber-go/zap). Logs go to stderr.

```bash
# Human-readable output at DEBUG level
TAILKITD_ENV=development tailkitd run

# JSON to stderr at INFO level (default)
tailkitd run
```

Every log line carries a `component` field (`files`, `vars`, `docker`, `systemd`, `metrics`) for easy filtering.

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
