# Step 7: Admin Endpoints and Mutation Guardrails

## Status
Implemented in current `tailkitd`.

## Auth and Concurrency
Admin routes require:
- `X-Admin-Key` matching `/etc/tailkitd/admin.key`
- `X-State-Epoch` on mutation methods (`POST`, `PUT`, `PATCH`, `DELETE`)

Stale/future epoch requests fail with `409` and current epoch in response header `X-State-Epoch`.

## Access Control Layer
When grants exist in `/etc/tailkitd/access.d/*.toml`, caller identity is checked for capability-level authorization.

## Endpoints
- `POST /admin/hosts/me/config`
- `POST /admin/hosts/{me|hostname}/services/{service}`
- `DELETE /admin/hosts/{me|hostname}/services/{service}`
- `GET /admin/access/grants`
- `POST /admin/access/grants`
- `POST /admin/files/write`
- `POST /admin/transfer`
- `POST /admin/internal/accept-promotion` (peer-internal)

## Current Semantics
- Host config mutation only permits local peer entry updates.
- Service mutations are written atomically and followed by registry reload.
- Admin transfer uses fencing token increment plus rollback on promotion failure.
- Successful mutations increment epoch and return updated `X-State-Epoch`.
