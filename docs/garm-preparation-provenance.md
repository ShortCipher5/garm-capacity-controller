# Never-dispatched Garm preparation provenance (zero-agent/non-JIT normal pools only)

This is a narrow liveness fix, not a deletion/release mechanism or a generic
exception for `creating` resources. It applies ONLY to the pinned v0.2.1 zero-agent normal
pool `pending_create` row shape: runner `pending`, empty provider ID, zero agent
ID and no job. Malformed or wrong-case safety fields fail closed. The documented
omitempty meaning of an absent zero agent ID is pinned to that API version.

Default JIT-enabled NORMAL pools also store a nonzero AgentID after JIT, and
are NOT fixed by this zero-agent slice. Their original JIT response must be
source-bound to the original row/pool/generation before qualifying that path.
A later positive API/callback AgentID is not that evidence.

Scaleset rows are deliberately excluded. Their pre-JIT pending-row race remains
blocking until separately qualified against pinned scaleset database/JIT behavior.
Do not remove that guard based on similar status strings.

## What the collector and transaction prove

After beginning the durable barrier, the collector must find the exact current
immutable execution plan, original still-creating reservation and backend-binding
sequence, with no dispatch or cancellation history. It must collect complete fresh
Garm, actual Incus instance and operation inventories for every configured host.
A physical row replaces preparation classification; active, unknown, incomplete,
stale or failed operation/host views keep admission blocked.

Only after all collections succeed may that one candidate Garm database row be
omitted from physical inventory. Its reservation stays FULLY CHARGED. The barrier
completion transaction independently rechecks the exact fence, plan, request,
binding sequence, no intent/tombstone, complete host views and no previous or
current resource with the same identity. It audits the proof with readiness.
It grants no cached permit, cannot survive a different fence and cannot authorize
a second create. ClaimDispatch itself is not relaxed.

An existing persisted creating/unknown/physical inventory row is NOT laundered by
later absence or a matching status. Legacy/unplanned rows remain charged and
blocking. This fixes new provable ZERO-AGENT preparations only; it does not clean up past
ambiguous attempts or certify job-safe drain.

## Evidence and open qualification

Tests cover strict authenticated parser fixtures, real Facade+SQLite placement,
claim/barrier ordering, physical presence, operation/freshness failures,
cancellation, old history, audit rollback, backup and stale-fence replay. Charges
remain one throughout preparation to physical presence. A compiled upstream Garm
normal-pool pending-DB-row/JIT/reconciliation integration fixture is still a
separate qualification step; parser/Facade tests must not be called that proof.
Scaleset qualification and installed routing/native isolation/external writer
fencing remain open. No live infrastructure is accessed and no release route is
added.

## Schema8 active-runtime follow-on

The narrow zero-agent proof above is retained for offline compatibility tests;
it is not an active-runtime bypass. Active schema8 creates require an original
source-issued receipt for non-JIT as well as JIT normal pools. See
[source-bound receipt design](jit-preparation-receipts-design.md),
[configuration contract](source-preparation-config.md) and
[actual source-to-gateway fixtures](../integration/garm-v0.2.1/source-gateway-qualification.md).
The full fixture exercises public HTTP enrollment, actual SQLite, AddRunner,
positive original JIT identity, pending polling, compiled provider commands,
gateway reconciliation and one-shot dispatch against local mTLS fixtures.
It is not native guest execution, GitHub authentication or physical-host proof.
Scalesets remain explicitly blocked, as do prior ambiguous/unknown attempts.
