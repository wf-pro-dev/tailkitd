# Configuration

All configuration lives under `/etc/tailkitd/`. tailkitd validates every file at startup.

**A missing config file disables the corresponding integration — it returns `503` on every request rather than crashing.** A present but invalid file is a fatal startup error.

Restart tailkitd after editing any config file:

```bash
sudo systemctl restart tailkitd
```

---

## Directory layout

```
/etc/tailkitd/
  env                          # auth key + runtime env vars (written by install)
  logging.toml                 # app + API logging config
  tools/                       # tool registration files (written by tailkit.Install())
  integrations/
    files.toml
    vars.toml
    docker.toml                # only written when Docker is detected at install time
    systemd.toml               # only written when systemd is PID 1 at install time
    metrics.toml
/var/lib/tailkitd/
  recv/                        # default file inbox (one subdirectory per tool)
/var/log/tailkitd/
  api.json.log                 # rotated API/request logs
```

---

## logging.toml

Controls app/state logging and API/request logging.

App logs go to `stderr` and are meant for local `journalctl` or console
inspection. API/request logs go to a dedicated JSON file for collection.

```toml
# /etc/tailkitd/logging.toml

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

**Default behavior:**
- app logs write to `stderr`
- API logs write to `/var/log/tailkitd/api.json.log`
- app format defaults to `text`
- API format defaults to `json`
- both app and API log levels default to `info`

**App fields:**
- `level`
  - Valid values: `debug`, `info`, `warn`, `error`
- `format`
  - Valid values: `text`, `json`

**API fields:**
- `enabled`
  - Enables or disables API/request logging
- `level`
  - Valid values: `debug`, `info`, `warn`, `error`
- `format`
  - Must be `json`
- `path`
  - Absolute path to the API log file

**API rotation fields:**
- `max_size_mb`
  - Rotate when the file reaches this size in megabytes
- `max_backups`
  - Number of rotated files to keep
- `max_age_days`
  - Maximum age of rotated files in days
- `compress`
  - Compress rotated files when `true`

**Environment overrides:**

Use `/etc/tailkitd/env` for operator overrides under systemd.

| Variable | Meaning |
|---|---|
| `TAILKITD_APP_LOG_LEVEL` | Override `[app].level` |
| `TAILKITD_APP_LOG_FORMAT` | Override `[app].format` |
| `TAILKITD_API_LOG_ENABLED` | Override `[api].enabled` |
| `TAILKITD_API_LOG_LEVEL` | Override `[api].level` |
| `TAILKITD_API_LOG_PATH` | Override `[api].path` |
| `TAILKITD_API_LOG_MAX_SIZE_MB` | Override `[api.rotation].max_size_mb` |
| `TAILKITD_API_LOG_MAX_BACKUPS` | Override `[api.rotation].max_backups` |
| `TAILKITD_API_LOG_MAX_AGE_DAYS` | Override `[api.rotation].max_age_days` |
| `TAILKITD_API_LOG_COMPRESS` | Override `[api.rotation].compress` |
| `TAILKITD_LOG_LEVEL` | Deprecated alias for `TAILKITD_APP_LOG_LEVEL` |

**Precedence:**

1. built-in defaults
2. `/etc/tailkitd/logging.toml`
3. environment overrides from `/etc/tailkitd/env`

**Request log level policy:**
- `INFO` for successful state-changing requests
- `INFO` for successful read-only requests
- `DEBUG` for high-volume, low-value read paths such as health and stream endpoints
- `WARN` for `4xx`
- `ERROR` for `5xx`

API request logs include a generated `request_id`. Request-scoped app logs
reuse the same `request_id` when available.

---

## files.toml

Controls which directories tailkitd can read from and write to.

Each `[[path]]` entry grants access to one directory. At least one entry is required.

**Fields:**

| Field | Type | Default | Description |
|---|---|---|---|
| `dir` | string | — | Absolute path, must end with `/` |
| `allow` | list | — | Permitted operations. Valid values: `read`, `write` |
| `share` | bool | `false` | When `true`, this path is included in the `GET /files/config` response, making it discoverable by tools like `devbox`. Paths without `share = true` are never disclosed. |

```toml
# /etc/tailkitd/integrations/files.toml

# Dedicated drop zone for files pushed over the tailnet.
[[path]]
dir   = "/var/lib/tailkitd/recv/"
allow = ["read", "write"]
share = true   # visible via GET /files/config

# Read access to the tailkitd config directory.
[[path]]
dir   = "/etc/tailkitd/"
allow = ["read"]
# share defaults to false — hidden from GET /files/config
```

**Validation rules:**
- `dir` must be an absolute path ending with `/`
- `allow` must not be empty
- Duplicate `dir` entries are rejected

### Inbox

Files sent without an explicit destination land in `/var/lib/tailkitd/recv/{tool}/`. No `files.toml` path rule is needed for the inbox — it is daemon-owned. See [api.md](api.md#inbox) for the inbox endpoints.

---

## vars.toml

Controls which project/environment scopes are accessible on this node.

Each `[[scope]]` entry grants access to one project/env combination. At least one entry is required.

**Fields:**

| Field | Type | Description |
|---|---|---|
| `project` | string | Project identifier. Must match `^[a-z0-9_-]+$` |
| `env` | string | Environment identifier. Must match `^[a-z0-9_-]+$` |
| `allow` | list | Permitted operations. Valid values: `read`, `write` |

```toml
# /etc/tailkitd/integrations/vars.toml

[[scope]]
project = "myapp"
env     = "prod"
allow   = ["read", "write"]
```

**Validation rules:**
- `project` and `env` must match `^[a-z0-9_-]+$`
- `allow` must not be empty
- Duplicate `project`/`env` pairs are rejected

---

## docker.toml

Controls Docker integration. Only written at install time when `/var/run/docker.sock` is present.

Each section maps to a group of Docker endpoints. Setting `enabled = false` on a section returns `403` for all endpoints in that group, regardless of `allow`.

**Valid `allow` values per section:**

| Section | Valid values |
|---|---|
| `[containers]` | `list`, `inspect`, `logs`, `stats`, `start`, `stop`, `restart`, `remove` |
| `[images]` | `list`, `pull` |
| `[compose]` | `list`, `up`, `down`, `pull`, `restart`, `build` |
| `[swarm]` | `read`, `write` |

```toml
# /etc/tailkitd/integrations/docker.toml

[containers]
enabled = true
allow   = ["list", "inspect", "logs", "stats", "start", "stop", "restart"]

[images]
enabled = true
allow   = ["list"]

[compose]
enabled = true
allow   = ["list", "up", "down", "restart"]

[swarm]
enabled = false
allow   = ["read"]
```

---

## systemd.toml

Controls systemd integration. Only written at install time when systemd is PID 1.

**Units section — valid `allow` values:**

`list`, `inspect`, `unit_file`, `logs`, `start`, `stop`, `restart`, `reload`, `enable`, `disable`

**Journal section — fields:**

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | bool | — | Gates per-unit journal endpoints |
| `priority` | string | `info` | Minimum log severity. Valid: `emerg`, `alert`, `crit`, `err`, `warning`, `notice`, `info`, `debug` |
| `lines` | int | `100` | Default number of lines returned per request. Must be `> 0` |
| `system_journal` | bool | `false` | Permits `GET /integrations/systemd/journal` (system-wide) |

```toml
# /etc/tailkitd/integrations/systemd.toml

[units]
enabled = true
allow   = ["list", "inspect", "unit_file", "logs", "start", "stop", "restart", "reload"]

[journal]
enabled        = true
priority       = "info"
lines          = 100
system_journal = false
```

---

## metrics.toml

Controls which host metrics are exposed. All sections are independent.

**Shared field:** every section has `enabled = true/false`.

**Section-specific fields:**

| Section | Extra fields |
|---|---|
| `[disk]` | `paths` — list of absolute mount points to report. Empty = all filesystems |
| `[network]` | `interfaces` — list of interface names to report. Empty = all interfaces |
| `[processes]` | `limit` — max processes returned, sorted by CPU desc. Range: 1–100. Default: 20 |
| `[ports]` | no extra fields; enables TCP LISTEN socket discovery and streaming |

```toml
# /etc/tailkitd/integrations/metrics.toml

[host]
enabled = true

[cpu]
enabled = true

[memory]
enabled = true

[disk]
enabled = true
paths   = []

[network]
enabled    = true
interfaces = []

[processes]
enabled = true
limit   = 20

[ports]
enabled = true
```

**Ports behavior:**
- If `[ports]` is absent or `enabled = false`, `/integrations/metrics/ports*` returns `503`
- When enabled, tailkitd reads `/proc/net/tcp`, `/proc/net/tcp6`, and `/proc/<pid>/fd` to resolve listening sockets and best-effort owning process metadata
- `pid` may be `-1` and `process` may be empty when procfs ownership could not be resolved

---

## Permission model

Two layers. Non-overlapping.

**Layer 1 — Tailscale identity** controls who can reach tailkitd at all. Every request is resolved via `lc.WhoIs` — unauthenticated callers get `401`.

**Layer 2 — Integration config files** control what a specific node exposes per operation. Defined per node in `/etc/tailkitd/integrations/`. A node running a production database might allow `containers_read` but not `containers_write`.

**Request validation order on every endpoint:**

1. Is this integration available? (config file present and `enabled = true`)
2. Does the caller have a valid Tailscale identity? (`lc.WhoIs` result)
3. Does this node permit this specific operation? (`allow` list in config)
4. Execute
