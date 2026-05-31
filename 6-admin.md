# Step 6: Admin Bootstrap and Election

## Status
Implemented in current `tailkitd`.

## Bootstrap Files
Generated if missing:
- `/etc/tailkitd/admin.key` (mode `0600`, random 32-char hex)
- `/etc/tailkitd/admin.fence` (mode `0600`, initial value `0`)

## Election Inputs
- Local daemon hostname (`tailkitd-...`)
- Local Tailscale peer hostname
- Fleet host names from `hosts.toml`
- Reachability/probe results for peer `/host` endpoints

## Election Rules
- If no eligible peer tailkitd nodes are discovered: local node becomes admin.
- If any peer reports `is_admin=true`: local node is non-admin.
- Otherwise: deterministic hostname ordering picks one admin.

## Scope of Peer Discovery
Only peers that appear in current fleet host names are considered eligible for admin probing.

## Notes
- This is best-effort distributed bootstrap, not consensus.
- Admin state is maintained in memory and exposed via `GET /host`.
