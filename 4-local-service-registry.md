# Step 4: Local Service Registry (`services.d`)

## Status
Implemented in current `tailkitd`.

## Directory
- `/etc/tailkitd/services.d`

## Schema
Each `*.toml` file defines one outsider service:
- `name`
- `runtime` (`systemd`, `binary`, `port-only`)
- `priority`
- `tags`
- `expected_ports`
- runtime-specific fields: `systemd_unit`, `binary_path`, `pid_file`

## Validation
- Unknown keys are rejected.
- Runtime one-of rules are enforced strictly.
- Invalid files are skipped during reload with warning logs.

## Runtime Behavior
- Registry loads current snapshot at startup.
- `fsnotify` watcher hot-reloads on create/write/rename/remove.
- Snapshot replacement is atomic in memory.

## Consumers
- `GET /services`
- Admin mutation endpoints for service CRUD
- `/services/provision` writes generated service files here
