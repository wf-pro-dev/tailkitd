# Feature 14: Node Mutation Epoch and Stale-Write Protection

## Status
Implemented as node-local optimistic concurrency control.

## What Exists Today
- Persisted epoch file: `/etc/tailkitd/state.epoch`.
- Mutation routes require `X-State-Epoch`.
- Validation rejects stale (`caller < current`) and future (`caller > current`) epochs.
- Successful mutations increment and persist epoch atomically.
- Conflict responses include current epoch in `X-State-Epoch`.

## Covered Mutation Surfaces
- `/admin/*` mutation endpoints
- `POST /services/provision`

## Non-Goals (Current)
- This is not distributed consensus.
- No global linearizability across nodes.
- No cross-node replicated write log.

## Operational Effect
Clients get deterministic stale-write detection and can perform read-refresh-retry loops.
