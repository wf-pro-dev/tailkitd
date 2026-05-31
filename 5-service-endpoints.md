# Step 5: Unified `GET /services`

## Status
Implemented in current `tailkitd`.

## Endpoint
- `GET /services`

## Purpose
Returns one unified list combining:
- outsider services from `/etc/tailkitd/services.d/*.toml`
- registered tools from `/tools`

## Response Model
Each item contains common fields:
- `name`, `source`, `runtime`, `priority`, `tags`, `expected_ports`

Optional fields by source:
- outsider: `systemd_unit`, `binary_path`, `pid_file`
- tool: `tool_version`, `tool_ts_host`

## Current Source Mapping
- outsider entries use `source: "outsider"`
- tool entries use `source: "tool"`, `runtime: "tsnet"`, `priority: "normal"`

## Error Behavior
- Method other than `GET` returns `405`.
- Tool listing failure returns `500`.
