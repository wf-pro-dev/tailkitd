# API Reference

All responses are JSON unless noted. All requests are made over the Tailscale network — the node must be reachable via tsnet.

---

## Conventions

### Errors

All errors return JSON with an `error` field and an optional `hint` field.

```json
{"error": "write permission denied for this path", "hint": "add write to the allow list in files.toml"}
```

| Status | Meaning |
|---|---|
| `400` | Invalid request — bad path, invalid arg, reserved key |
| `401` | Caller identity could not be resolved |
| `403` | Operation not permitted on this node |
| `404` | File, scope, key, or resource not found |
| `503` | Integration not available (missing config file, daemon not running) |

### Async jobs

Operations that may take time return a job immediately and execute asynchronously:

```json
{"job_id": "01J2K...", "status": "accepted"}
```

Poll for the result:

```
GET /exec/jobs/{id}?id={job_id}
```

```json
{"job_id": "...", "status": "completed", "stdout": "...", "stderr": "...", "error": ""}
```

Job statuses: `accepted`, `completed`, `failed`. Jobs are kept in memory for 5 minutes.

---

## Health

```
GET /health
```

Returns node identity information.

```json
{
  "tailkit_name": "tailkitd-myhost",
  "hostname":     "myhost",
  "tailkit_ip":   "100.x.x.x",
  "host_ip":      "100.x.x.x"
}
```

---

## Tools

```
GET /tools
```

Lists all tools registered on this node. Tool files are read from `/etc/tailkitd/tools/` on every request — installs and upgrades are reflected immediately without a tailkitd restart.

---

## Files

Files config must be present (`files.toml`). Path access is gated by `allow` lists. Path traversal is rejected on every operation.

### Config discovery

```
GET /files/config
```

Returns the node's files integration config, filtered to only include path rules where `share = true`. Paths without `share = true` are never disclosed.

```json
{
  "paths": [
    {"dir": "/etc/nginx/", "allow": ["read", "write"]}
  ]
}
```

### Write a file

```
POST /files
```

Two modes depending on headers:

**Explicit destination** — writes to a path permitted by `files.toml`:

| Header | Description |
|---|---|
| `X-Dest-Path` | Absolute destination path on the node |
| `Content-Type` | `application/octet-stream` |

```
POST /files
X-Dest-Path: /etc/nginx/conf.d/api.conf
Content-Type: application/octet-stream
<body>
```

Response:

```json
{"written_to": "/etc/nginx/conf.d/api.conf", "bytes_written": 1234}
```

**Default inbox** — writes to `/var/lib/tailkitd/recv/{tool}/{filename}`. No `files.toml` rule needed:

| Header | Description |
|---|---|
| `X-Tool` | Tool name (determines inbox subdirectory) |
| `X-Filename` | Filename within the tool's inbox |

```
POST /files
X-Tool: devbox
X-Filename: deploy.sh
<body>
```

Response:

```json
{"written_to": "/var/lib/tailkitd/recv/devbox/deploy.sh", "bytes_written": 512}
```

### Read a file

```
GET /files?path=/absolute/path/to/file
```

Default response:

```json
{"content": "file contents here"}
```

Pass `Accept: application/octet-stream` to receive raw bytes instead.

### Stat a file

```
GET /files?path=/absolute/path/to/file&stat=true
```

Returns file metadata including a SHA-256 hash. Use this for integrity checks and drift detection without fetching the full file content.

```json
{
  "name":    "nginx.conf",
  "size":    1024,
  "is_dir":  false,
  "mod_time": "2026-01-15T10:30:00Z",
  "mode":    "-rw-r--r--",
  "sha256":  "e3b0c44298fc1c149afbf4c8996fb924..."
}
```

### List a directory

```
GET /files?dir=/absolute/path/to/dir/
```

```json
[
  {"name": "api.conf", "size": 1234, "is_dir": false, "mod_time": "...", "mode": "-rw-r--r--"}
]
```

---

## Inbox

The inbox operates on `/var/lib/tailkitd/recv/{tool}/`. The files integration must be enabled but no path rule in `files.toml` is required.

```
GET    /inbox/{tool}              List files in the tool's inbox
GET    /inbox/{tool}/file?path=   Read a file (relative path)
DELETE /inbox/{tool}/file?path=   Delete a file (relative path)
```

`GET /inbox/{tool}` returns an empty array when the inbox directory does not exist yet. `path` is relative to the tool's inbox directory — absolute paths and traversal sequences are rejected with `400`.

---

## Vars

Vars config must be present (`vars.toml`). Project/env access is gated by `allow` lists in each `[[scope]]` entry.

**Key rules:**
- Must match `^[a-zA-Z][a-zA-Z0-9_./-]*$`
- Keys prefixed with `_` are reserved for internal metadata and cannot be read or written by callers

```
GET    /vars                            List all configured scopes
GET    /vars/{project}/{env}            List all keys as JSON map
GET    /vars/{project}/{env}?format=env Render as KEY=VALUE text (shell-quoted)
GET    /vars/{project}/{env}/{key}      Get a single key
PUT    /vars/{project}/{env}/{key}      Set a key (body: plain text value)
DELETE /vars/{project}/{env}/{key}      Delete a key
DELETE /vars/{project}/{env}           Delete entire scope
```

---

## Docker

Docker config must be present (`docker.toml`). Each section has its own `enabled` flag and `allow` list.

### Availability

```
GET /integrations/docker/available
```

Returns `200 {"available": true}` if docker.toml is present and the Docker daemon is reachable, `503` otherwise.

### Containers

```
GET  /integrations/docker/containers                     List all containers
GET  /integrations/docker/containers/{id}                Inspect a container
GET  /integrations/docker/containers/{id}/logs?tail=100  Fetch logs
GET  /integrations/docker/containers/{id}/stats          Resource stats (not implemented)
POST /integrations/docker/containers/{id}/start          Start → async job
POST /integrations/docker/containers/{id}/stop           Stop → async job
POST /integrations/docker/containers/{id}/restart        Restart → async job
```

Response types are Docker SDK types: `container.Summary`, `container.InspectResponse`.

### Images

```
GET  /integrations/docker/images               List images
GET  /integrations/docker/images/{id}          Inspect an image
POST /integrations/docker/images/{ref}/pull    Pull an image → async job
DELETE /integrations/docker/images/{id}/remove Remove an image
```

### Compose

```
GET  /integrations/docker/compose/projects             List all compose projects
GET  /integrations/docker/compose/{project}            Get a single project
POST /integrations/docker/compose/{project}/up         Bring up → async job
POST /integrations/docker/compose/{project}/down       Bring down → async job
POST /integrations/docker/compose/{project}/pull       Pull images → async job
POST /integrations/docker/compose/{project}/restart    Restart → async job
POST /integrations/docker/compose/{project}/build      Build → async job
```

### Swarm

```
GET /integrations/docker/swarm/nodes     List swarm nodes
GET /integrations/docker/swarm/services  List swarm services
```

---

## Systemd

Systemd config must be present (`systemd.toml`). Requires D-Bus access.

### Availability

```
GET /integrations/systemd/available
```

Returns `{"available": true/false}` — false when systemd.toml is missing or D-Bus is unreachable.

### Units

```
GET  /integrations/systemd/units                         List all units
GET  /integrations/systemd/units/{unit}                  Inspect unit properties
GET  /integrations/systemd/units/{unit}/file             Read unit file content
POST /integrations/systemd/units/{unit}/start            Start → async job
POST /integrations/systemd/units/{unit}/stop             Stop → async job
POST /integrations/systemd/units/{unit}/restart          Restart → async job
POST /integrations/systemd/units/{unit}/reload           Reload → async job
POST /integrations/systemd/units/{unit}/enable           Enable → async job
POST /integrations/systemd/units/{unit}/disable          Disable → async job
GET  /integrations/systemd/units/{unit}/journal?lines=   Fetch unit journal
```

### Journal

```
GET /integrations/systemd/journal?lines=   System-wide journal (requires system_journal = true)
```

Journal responses are arrays of `JournalEntry`:

```json
[
  {
    "timestamp_us": 1742300000000000,
    "message":      "Started tailkitd node agent.",
    "unit":         "tailkitd.service",
    "priority":     "info",
    "fields":       {"_PID": "1234", "SYSLOG_IDENTIFIER": "tailkitd"}
  }
]
```

`?lines=` caps the number of entries returned. Defaults to the value set in `systemd.toml`.

---

## Metrics

Metrics config must be present (`metrics.toml`). Each section is independently enabled.

### Availability

```
GET /integrations/metrics/available
```

Returns `{"available": true/false}`.

### Endpoints

```
GET /integrations/metrics/host       Host info (OS, platform, uptime)
GET /integrations/metrics/cpu        CPU usage per core + total
GET /integrations/metrics/memory     Virtual and swap memory
GET /integrations/metrics/disk       Disk usage per configured path
GET /integrations/metrics/network    IO counters per configured interface
GET /integrations/metrics/processes  Top processes by CPU (capped by limit)
GET /integrations/metrics/all        All enabled metrics in one response
```

`/integrations/metrics/all` returns only sections that are enabled in `metrics.toml`. Disabled sections are omitted from the response (`omitempty`).

Response types are [gopsutil v4](https://github.com/shirou/gopsutil) types: `host.InfoStat`, `cpu.InfoStat`, `mem.VirtualMemoryStat`, `disk.UsageStat`, `net.IOCountersStat`.
