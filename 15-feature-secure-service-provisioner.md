# Feature 15: Secure Service Provisioner and Lifecycle Binding

## Status
Partially implemented with a minimal secure provision path.

## Implemented
- Endpoint: `POST /services/provision`.
- Input includes artifact hash, signature, sender public key, runtime, staged path, and service name.
- Artifact verification enforces hash and signature checks.
- Provision output writes:
- `/var/lib/tailkitd/services/<service>/artifact`
- `/var/lib/tailkitd/services/<service>/meta.json`
- `/etc/tailkitd/services.d/<service>.toml`
- Service registry reload after successful provision.

## Runtime Support (Current)
- `port-only`
- `restricted-systemd` (mapped to managed `binary` service execution metadata)

## Security/Control Integration
- Requires caller identity in request context.
- Enforces RBAC for `service.write` when access grants exist.
- Enforces `X-State-Epoch` stale-write protection.

## Remaining Gaps
- No full lifecycle revocation orchestration yet.
- No formal policy engine for runtime hardening profiles.
- No built-in artifact distribution transport; endpoint expects staged local artifact path.
