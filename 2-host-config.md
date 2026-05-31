# Step 2: Fleet Host Config (`hosts.toml`)

## Status
Implemented in current `tailkitd`.

## What Exists Today
- Host config file path is `/etc/tailkitd/hosts.toml`.
- Schema is a fleet list via `[[hosts]]` entries, not a single-host root object.
- Unknown keys are rejected.
- `hosts[].name` is required and must be unique.
- Defaults are applied per entry:
- `role = "unclassified"`
- `environment = "default"`
- `provider = "unknown"`
- `tags = []`
- `metadata = {}`
- If the file is missing at first start, tailkitd generates a default file with one local host entry.

## Runtime Behavior
- `HostManager` watches and hot-reloads `hosts.toml`.
- On invalid reload, in-memory state remains last known good and errors are logged.
- The local host entry is resolved by exact match with local Tailscale peer hostname.

## Current API Coupling
- `GET /host` returns the local host entry plus live Tailscale info.
- Admin host config mutation updates only the local peer entry in `hosts.toml`.

## Notes
- This is now a fleet-centric control file and should be treated as source-of-truth for node identity classification.
