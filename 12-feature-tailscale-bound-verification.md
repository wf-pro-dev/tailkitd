# Feature 12: Tailscale-Bound Artifact Verification

## Status
Implemented as artifact key bootstrap + identity endpoint + verification in provision flow.

## What Exists Today
- Per-node Ed25519 keypair generation at startup if missing:
- private: `/etc/tailkitd/artifact.key` (0600)
- public: `/etc/tailkitd/artifact.pub` (0644)
- Public key exposure endpoint:
- `GET /identity/pubkey`

## Verification Path
`POST /services/provision` validates:
- staged artifact SHA-256 matches declared hash
- provided signature validates against sender public key

## Current Endpoint Response
`GET /identity/pubkey` returns:
- `node_hostname`
- `tailscale_identity` (caller login when available)
- `artifact_public_key`

## Notes
- This is node-identity binding over authenticated tailnet transport.
- Rotation policy is not yet formalized; keys persist until replaced out-of-band.
