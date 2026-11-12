# Dynamic route eligibility and activation guard contract

Design for the next offline implementation. No active route, activation writer,
remote enable, HTTP action or provisioning permission is supplied by this file.
The reviewed schema9 catalog and resolver remain inactive-only.

## Persistent state and compatibility

Registration identity remains permanently immutable. Future route eligibility
must be an append-only, audited transition history, not an UPDATE which rewrites
its template, original pool identity, host-binding sequences or creation intent.
Each transition belongs to the original route identity sequence and has a strictly
increasing revision. From baseline zero INACTIVE, actual transitions must strictly
alternate ACTIVE/INACTIVE; an already-inactive request is a read-only no-op. A local inactive transition needs no claim of native/job
fencing. A local ACTIVE transition must bind the exact initialized admission-ledger
lineage and full reviewed-operation digest, not just an audit counter or a
controller display name. This is not protection against raw same-lineage rollback.
A local ACTIVE transition requires a separately qualified, bound-journal
known enable acknowledgement and fresh exact source read; one confirmed
enable request ID cannot authorize multiple activation revisions; neither facts JSON nor
GET similarity constructs that proof. No generic boolean activation API exists.

Schema10 should add original route identity and admission-revision columns to
reservations and dispatches (zero only for static historical pools), plus the same
optional immutable fields to execution plans. Omit zero fields in canonical JSON
so source8/static plan bytes and source receipt hashes are not reinterpreted.
Migration closes readiness/the old nonce without granting dynamic eligibility.
A catalog pool with unqualified legacy charge/plan cannot be silently grandfathered.

An indexed permanent registration-audit lookup must distinguish an actual static
pool from one whose catalog row was lost. Missing, malformed or mismatched route
history fails closed, never falls back to current startup defaults.

## Atomic admission and dispatch

In the SAME admission transaction which selects a host and charges capacity:

- verify complete original catalog identity and backend-binding sequences;
- require latest route state ACTIVE, with exact immutable identity and revision;
- require request execution/resources/candidate hosts/source template to match
  the original private route policy and still-current startup reference digest;
- persist identity/revision on reservation and immutable plan with the audit.

New reservation SQL defaults must reject older writers which omit provenance for
a dynamic pool. New dispatch SQL defaults must likewise reject missing or stale
identity/revision, even if an older binary has a previously opened handle.

At final prepared claim, preserve the existing exact plan/binding/source receipt
checks and recheck current ACTIVE identity/revision inside that writer transaction.
No cached resolver status or prior ACK is permission. A claim/disable race has two
legal orders: claim commits first and remains charged/possibly delivered; or
inactive commits first and no new permit is issued. Existing dispatch replay must
remain one-shot. Confirmed read-only verification may still recover original
history without creating a second native request.

Conservative initial behavior: a revision change holds never-dispatched plans
from an older revision. It does not rewrite/reassign/release them. Any later
same-identity resumption needs a separate reviewed protocol. This trades some
liveness for explicit provenance and is not an automatic repair path.

## Runtime resolution and coordination

Use request-local, owned catalog reads and immutable private startup references,
never mutable Facade map edits. Missing/changed references block new claims while
retaining original plans/accounting on their original host. Source actor grants
remain exact fixed references; no implicit role or allowlist expansion.

Remote enable and local ACTIVE commit are different databases, not an atomic CAS.
A coordinator must retain explicit durable phase/intent ownership across them.
Unknown remote enable stays locally inactive. Deactivate locally first, then
request remote disable. Final fresh reconciliation is required before claims;
neither action certifies job-safe drain, stock Garm handler completion or deletion.

A SQLite transition cannot revoke a callback already delivered to a backend.
Previously running binaries, copied credentials/ledgers, hidden projects, native
parent limits and real assignment/accepted-work fences remain external gates.
Immutable profile names are not immutable profile contents or native isolation.

## Required offline tests

- Real SQLite handles/processes: selection+plan+identity+revision+charge/audit
  atomicity, same-request replay, two-pool contention and rollback/uncertainty.
- Both claim/disable orders, old revision replay, unknown native POST and no host
  failover, missing/unqualified route/plan provenance and old-writer SQL defaults.
- Source poll before/after registration/enable and during every transition must
  never turn inactive/stale status into JIT/row/native authorization.
- Canonical archives preserve original transitions/plans/charges and remain
  runtime-disabled; restore/readiness cannot grant ACTIVE or repair old history.
- Actual source AddRunner/SQLite/compiled provider/Gateway/mTLS fixture integration
  in addition to library tests; real issuer/guest/native deployment remains separate.

## Current local implementation checkpoint

Schema10 storage, canonical transition history, route tuple propagation and
transaction/SQL guards are implemented in the isolated candidate. A local
`DeactivatePoolRoute` CAS can append INACTIVE and close readiness; it cannot send
an upstream request or enable anything. `ValidateCurrentOriginalPreparation`
checks source-poll eligibility while the older metadata-only validation retains
historical read semantics. Record ACK replays are also held while locally inactive.

No ACTIVE writer, remote-enable proof adapter, dynamic Facade route resolver
consumer, activation API/UI or usable dynamic provisioning path is present.
ACTIVE rows in unit tests are explicitly synthetic SQL fixtures, not qualified
native evidence. Historical profile names are not profile-content attestations.
Actual admission/claim tests preserve old-revision charges and confirm that a
callback already delivered before local inactive can complete afterward. This is
not revocation or a job/native fence. Full-chain authorization validation is
currently linear in transition count; no truncated recent list authorizes grants.
