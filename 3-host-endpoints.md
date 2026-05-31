# Step 3: `GET /host` Endpoint

## Status
Implemented in current `tailkitd`.

## Endpoint
- `GET /host`

## Response Shape
The response merges operator-defined host data and live Tailscale status:
- host config fields: `name`, `role`, `environment`, `provider`, `instance_type`, `tags`, `metadata`
- Tailscale fields: `ts_hostname`, `ts_dns_name`, `ts_ips`, `os`, `arch`, `online`
- control-plane state: `is_admin`

## Data Sources
- Host identity: `HostManager.Get()` from `/etc/tailkitd/hosts.toml`
- Network reality: `LocalClient.Status(...)`
- Admin flag: bootstrap/admin state in memory

## Validation/Errors
- Method other than `GET` returns `405`.
- Tailscale status failure returns `500`.

## Notes
- `arch` currently maps from `status.Version` in implementation.
