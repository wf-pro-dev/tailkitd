[tailkitd-README.md](https://github.com/user-attachments/files/26092550/tailkitd-README.md)
# tailkitd

A node-level infrastructure daemon for Tailscale-native tools. tailkitd runs on every node in your tailnet and exposes a consistent HTTP API that tools built with [`tailkit`](https://github.com/wf-pro-dev/tailkit) can call over the tailnet.

Tailscale handles the network — identity, encryption, peer connectivity, and ACL-based access control. tailkitd handles the node — what it exposes, what it can do, and how remote tools interact with it.

---

## What tailkitd adds that tsnet does not

| Concern | tsnet / Tailscale | tailkitd |
|---|---|---|
| Node identity + auth | ✓ via `lc.WhoIs` | — |
| Peer discovery | ✓ via `lc.Status` | — |
| Installed tool registry | — | ✓ `GET /tools` |
| Remote named-command exec | — | ✓ `POST /exec/{tool}/{cmd}` |
| File receive without confirmation | — | ✓ `POST /receive` |
| File read / download | — | ✓ `GET /files` |
| Operational env var store | — | ✓ `GET /vars/{project}/{env}` |
| Docker / Compose / Swarm control | — | ✓ `/integrations/docker/...` |
| Systemd unit + journal control | — | ✓ `/integrations/systemd/...` |
| Host resource metrics | — | ✓ `/integrations/metrics/...` |

---

## Installation

```bash
curl -fsSL https://your-tailkitd-node/install | sh
```

tailkitd runs as a systemd service named `tailkitd.service`. It requires Tailscale to be installed and the node to be joined to a tailnet before installation.

---

## Logging

tailkitd uses [`go.uber.org/zap`](https://github.com/uber-go/zap) for structured logging. Logs are written to stderr.

Set `TAILKITD_ENV=development` for human-readable output at DEBUG level. Omit it or set any other value for JSON output at INFO level.

```bash
# development — human-readable, DEBUG level
TAILKITD_ENV=development tailkitd

# production — JSON to stderr, INFO level (default)
tailkitd
```

Every log line carries a `component` field identifying the subsystem (`exec`, `docker`, `systemd`, `vars`, `files`, `metrics`) so logs from different integrations can be filtered independently.

```json
{"level":"info","ts":1742300000,"component":"exec","msg":"exec accepted","tool":"devbox","command":"reload-nginx","job_id":"01J2K...","caller":"laptop"}
{"level":"info","ts":1742300001,"component":"exec","msg":"exec completed","job_id":"01J2K...","exit_code":0,"duration_ms":240}
{"level":"warn","ts":1742300002,"component":"docker","msg":"permission denied","endpoint":"/integrations/docker/containers/my-app/restart","caller":"monitor-node","reason":"missing acl cap"}
```

Note: var values are never logged regardless of log level.

---

## Configuration

All configuration lives under `/etc/tailkitd/`. tailkitd validates all files at startup. A missing config file disables the corresponding feature with a `503` response — no feature is active by default.

```
/etc/tailkitd/
  tools/                          # written by tailkit.Install() — do not edit manually
    devbox.json
    docker-dashboard.json
  files.toml                      # file read + receive permissions
  vars.toml                       # env var scope permissions
  integrations/
    docker.toml
    systemd.toml
    metrics.toml
```

### files.toml

Path as section header. Each block declares read/write permissions and post-receive hooks for that directory. `post_recv` entries must reference commands registered by an installed tool.

```toml
[["/etc/nginx/conf.d/"]]
read      = true
write     = true
post_recv = ["reload-nginx"]

[["/etc/systemd/system/"]]
read      = true
write     = true
post_recv = ["daemon-reload"]

[["/opt/"]]
read      = true
write     = true
post_recv = []
```

### vars.toml

```toml
[[scope]]
project = "myapp"
read    = true
write   = true

[[scope]]
project = "monitoring"
read    = true
write   = false
```

### integrations/docker.toml

```toml
[permissions]
containers_read   = true
containers_write  = true
images_read       = true
images_write      = false
compose_read      = true
compose_write     = true
swarm_read        = true
swarm_write       = false   # disabled by default — high blast radius
```

### integrations/systemd.toml

```toml
[units]
read  = true
write = true

[journal]
enabled  = true
lines    = 500        # max lines a caller can request
priority = "info"     # minimum priority exposed
```

### integrations/metrics.toml

```toml
[host]
enabled = true

[cpu]
enabled  = true
per_core = true

[memory]
enabled = true
swap    = true

[disk]
enabled = true
paths   = ["/", "/data", "/opt"]

[network]
enabled    = true
interfaces = ["eth0", "tailscale0"]

[processes]
enabled = false    # off by default — most sensitive
limit   = 20
```

---

## Permission model

Two layers. Non-overlapping.

**Layer 1 — Tailscale ACL caps** control which callers can reach which tailkitd feature at all. Defined once in the tailnet policy file. Examples:

```json
"grants": [
  {
    "src": ["tag:devbox"],
    "dst": ["tag:server"],
    "app": {
      "tailscale.com/cap/tailkitd-docker": [{"read": true, "write": true}],
      "tailscale.com/cap/tailkitd-files":  [{"read": true, "write": true}]
    }
  }
]
```

**Layer 2 — Integration config files** control what a specific node exposes per operation. Defined per node in `/etc/tailkitd/integrations/`. A node running a production database might allow `containers_read` but not `containers_write`.

**Request validation order on every endpoint:**
1. Is this integration available? (config file present)
2. Does the caller have the required Tailscale ACL cap? (`lc.WhoIs` result)
3. Does this node permit this specific operation? (integration config)
4. Execute

---

## API reference

All responses are JSON. Operations that may take time return a `job_id` immediately and execute asynchronously. Poll `GET /exec/jobs/{id}` for results. Jobs are kept in memory for 5 minutes. All requests respect context cancellation — if the tailkit client cancels a request mid-flight, tailkitd cleans up and returns immediately. Async jobs run with their own timeout derived from the command's declared `Timeout` field, independent of the HTTP request lifecycle.

### Tools

```
GET  /tools              List installed tools + registered commands
GET  /tools/{name}       Single tool detail
```

### Exec

```
POST /exec/{tool}/{cmd}  Invoke a registered command
                         Body: {"args": {"key": "value"}}
                         Response: {"job_id": "...", "status": "accepted"}

GET  /exec/jobs/{id}     Poll job status
                         Response: {"status": "completed", "exit_code": 0, "stdout": "...", "stderr": "...", "duration_ms": 240}
```

Commands must be registered by an installed tool via `tailkit.Install()`. tailkitd will not execute anything not in the registry. Arg values are validated against declared patterns before substitution.

### Files

```
POST /receive            Receive a file
                         Multipart: path=<dest_path>, file=<content>
                         Response: {"written_to": "...", "job_id": "...", "status": "accepted"}

GET  /files?path=        Read file content or download raw bytes
                         Accept: application/json → {"path": "...", "content": "...", "size": 123}
                         Accept: application/octet-stream → raw bytes

GET  /files?dir=         List directory entries
```

### Vars

```
GET    /vars                           List all projects and environments
GET    /vars/{project}/{env}           List all vars in scope
                                       ?format=env  → KEY=VALUE text
                                       ?format=json → {"KEY": "value"} map
GET    /vars/{project}/{env}/{key}     Get single var
PUT    /vars/{project}/{env}/{key}     Set a var — body: {"value": "..."}
DELETE /vars/{project}/{env}/{key}     Delete a var
DELETE /vars/{project}/{env}          Delete entire scope
```

Keys must match `^[A-Z][A-Z0-9_]*$`. Keys prefixed with `_` are reserved for metadata.

### Docker integration

```
GET    /integrations/docker/info
GET    /integrations/docker/containers
GET    /integrations/docker/containers/{id}
POST   /integrations/docker/containers/{id}/start
POST   /integrations/docker/containers/{id}/stop
POST   /integrations/docker/containers/{id}/restart
DELETE /integrations/docker/containers/{id}
GET    /integrations/docker/containers/{id}/logs?tail=100
GET    /integrations/docker/containers/{id}/stats
GET    /integrations/docker/images
DELETE /integrations/docker/images/{id}
POST   /integrations/docker/images/pull           body: {"image": "nginx:latest"}
GET    /integrations/docker/networks
GET    /integrations/docker/networks/{id}
GET    /integrations/docker/volumes
DELETE /integrations/docker/volumes/{name}
GET    /integrations/docker/compose/
GET    /integrations/docker/compose/{name}
POST   /integrations/docker/compose/{name}/up     body: {"file": "/path/to/compose.yml"}
POST   /integrations/docker/compose/{name}/down
POST   /integrations/docker/compose/{name}/pull
POST   /integrations/docker/compose/{name}/restart
POST   /integrations/docker/compose/{name}/build
GET    /integrations/docker/swarm/info
GET    /integrations/docker/swarm/nodes
GET    /integrations/docker/swarm/services
GET    /integrations/docker/swarm/services/{id}
GET    /integrations/docker/swarm/tasks
```

Response types are Docker SDK types directly: `container.Summary`, `container.InspectResponse`, `image.Summary`, `swarm.Node`, `swarm.Service`, `swarm.Task`.

### Systemd integration

```
GET  /integrations/systemd/units
GET  /integrations/systemd/units/{name}
POST /integrations/systemd/units/{name}/start
POST /integrations/systemd/units/{name}/stop
POST /integrations/systemd/units/{name}/restart
POST /integrations/systemd/units/{name}/reload
POST /integrations/systemd/units/{name}/enable
POST /integrations/systemd/units/{name}/disable
GET  /integrations/systemd/units/{name}/file
GET  /integrations/systemd/units/{name}/journal?lines=100&priority=info
GET  /integrations/systemd/journal?lines=100
```

Response types are go-systemd SDK types directly: `dbus.UnitStatus`, `sdjournal.JournalEntry`.

### Metrics integration

```
GET /integrations/metrics/
GET /integrations/metrics/host
GET /integrations/metrics/cpu
GET /integrations/metrics/memory
GET /integrations/metrics/disk
GET /integrations/metrics/network
GET /integrations/metrics/processes
```

Response types are gopsutil v4 types directly: `mem.VirtualMemoryStat`, `disk.UsageStat`, `cpu.InfoStat`, `net.IOCountersStat`, `host.InfoStat`.

---

## Error responses

All errors return JSON with an `error` field and an optional `hint` field.

| Status | Meaning |
|---|---|
| `400` | Invalid request — bad path, invalid arg, reserved key |
| `401` | Caller identity could not be resolved |
| `403` | Caller lacks required Tailscale ACL cap, or operation not permitted on this node |
| `404` | Tool, command, file, scope, or key not found |
| `503` | Integration not available on this node (no config file, daemon not running) |

---

## What tailkitd does not do

- **Tailscale auth / identity** — handled by tsnet and `lc.WhoIs`
- **Peer discovery** — handled by `lc.Status().Peer`
- **Notifications** — application-level concern, no tailnet specificity
- **Secrets encryption** — vars store is operational config, not a secrets manager
- **Reverse proxy management** — single-purpose tool concern, use Files + Exec
- **Package manager integration** — too distro-specific, community tool territory
- **Arbitrary shell execution** — exec only runs commands registered at install time

---

## Module path

```
github.com/wf-pro-dev/tailkitd
```
