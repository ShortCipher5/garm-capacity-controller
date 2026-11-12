# Durable gateway dispatch boundary

This is the gateway's **one-shot execution core**, not a deployed gateway, complete Incus transport, or production enforcement claim. It connects the reservation model to a trusted backend callback without treating preflight replay as permission to create again.

## Why reservation replay is insufficient

Garm may repeat requests, a client can lose a response, and two processes can arrive at the same reservation concurrently. Returning an existing reservation must not cause another provider invocation. Checking a previously successful preflight also ignores later reconciliation barriers, stale inventory, host draining and newly discovered overcommit.

`ClaimDispatch` rechecks the exact persisted request, current global readiness, every host's observation freshness, the target host's policy/health, conservative totals and known inventory. It inserts a unique dispatch intent **and audit event in one BEGIN IMMEDIATE transaction**, then returns a new permit only after commit. Existing intents return `ErrDispatchClaimed`, never another permit. A resource already represented by linked inventory cannot receive a fresh create permit.

A claim is denied when newly imported inventory makes global, host or pool allocations exceed budget, even if its reservation predates that discovery. A failed/unknown/idle/running reservation is not a new create opportunity. Claims made before a later barrier are already in-flight work and remain charged; beginning a drain is not retroactive cancellation of an already committed dispatch.

## One-shot execution

`ExecuteDispatch` owns the control flow:

1. Commit the unique intent and audit.
2. Invoke the trusted backend callback once, **outside the database transaction**.
3. Persist only bounded operation/resource identifiers and a submitted/confirmed/unknown result, never bootstrap data, credentials, raw response bodies or provider error text.

Concurrent handles/processes and restarted callers cannot obtain a second callback invocation for the same reservation. Errors, panics, cancellation, invalid receipts and failure to save the result remain charged. The core does not sleep and retry an uncertain external action, release the allocation, or turn it into the pre-JIT benign-defer outcome.

This is **at-most-once callback invocation**, not exactly-once execution at Incus. A crash between intent commit and sending the request is indistinguishable from a lost remote result without authoritative backend evidence. Such an intent stays blocked. Copying a permit is not permission to call a provider again; use the owned execution wrapper, not an application-level retry loop.

Receipt identity is immutable after attachment, and a confirmed record cannot be downgraded. The operation ID is the primary create operation UUID, not a URL with query parameters or credentials. A multi-operation backend still needs its own authoritative operation-aware reconciliation. No uncertain-create redispatch or confirmed-retirement workflow is implemented here.

## Storage migration

Owned schema version 1 first receives the version-2 dispatch journal; current version 3 adds [never-dispatched cancellation](undispatched-cancellation.md), excluding all preexisting v1/v2 keys because schema version alone cannot establish their dispatch provenance. Migrations are transactional and audited. Existing policy, readiness, reservations, inventory, nonces and audit history are retained. The application-ID/private-file checks still apply; unrelated or future-version databases are rejected. The original JSON ledger is not imported. Migration occurs only when an application explicitly opens its local database; nothing here opens the user's live stack or database.

## Remaining real integration work

- A concrete credential-verified Incus transport with immutable explicit CPU/RAM limits, trusted project/host routing and bounded authenticated requests.
- A provider facade and credential-isolated gateway service. Garm and wrapper identities must not retain Incus write credentials or direct sockets.
- Full Garm/Incus/operation inventory collection, deterministic identity binding, restart reconciliation and evidence-based retirement.
- Operation-aware delete/start policies. Never broadcast a delete for unknown placement or let failure cleanup blindly terminate a potentially busy runner.
- Safe drain for already-idle runners, complete admin authentication/authorization, secret handling and operational audit/backup qualification.

Tests cover concurrent handles/processes invoking a backend once, crash-intent persistence, callback errors/panics/invalid results, atomic audit rollback, freshness/barrier/profile checks, linked inventory/overcommit refusal, immutable result identity and owned version-one migration. They use disposable backends only. Actual Incus requests and installed-stack behavior are not certified by these tests.

Consistent backup/restore remains a separate open acceptance gate: see [operational acceptance](operational-acceptance.md). No dispatch-core test should be read as backup/restore qualification.
