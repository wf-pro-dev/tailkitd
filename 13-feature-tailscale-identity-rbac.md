# Feature 13: Tailscale Identity RBAC and Scoped Delegation

## Status
Partially implemented.

## Implemented
- Access grant registry in `/etc/tailkitd/access.d/*.toml`.
- Grant shape: `identity`, `target`, `role`.
- Roles: `admin`, `superadmin`.
- API to read/write grants:
- `GET /admin/access/grants`
- `POST /admin/access/grants`
- Enforcement integrated in admin and provision mutation paths when any grants exist.

## Current Capability Mapping
- `service.write`: admin or superadmin
- `host.write`: superadmin only
- `access.write`: superadmin only
- `admin.transfer`: superadmin only

## Remaining Gaps
- No richer custom role/capability matrix yet.
- No explicit migration tooling from admin-key-only mode.
- No deny rules or policy precedence beyond exact target then wildcard.
