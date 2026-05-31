# Feature 11: Service Invite Tokens

## Status
Implemented with daemon-side single-use enforcement.

## What Exists Today
- Signed invite token envelope with payload and signature.
- Payload includes `jti`, issuer, grantee, target node, service name, artifact hash, expiry, and single-use flag.
- Verification is performed server-side by `POST /services/claim`.
- Claim persistence is stored in `/var/lib/tailkitd/invites/claims.json`.

## Security Guarantees
- Signature must verify.
- Token must be unexpired.
- Caller identity must match token grantee.
- Token ID replay is blocked by persisted claim store.

## API
- `POST /services/claim`
- body: `{"token":"<base64>"}`
- success: `{"status":"claimed", ...}`

## Notes
- Single-use enforcement is daemon-owned, not client-only.
- Claims survive daemon restarts.
