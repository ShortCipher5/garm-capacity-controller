# Admission architecture

## Implemented experimental enforcement flow

```text
patched Garm -> provider -> gateway pre-JIT reservation
                               |
                     one-shot dispatch intent -> Incus create
                               |                       |
                          SQLite ledger <--- reconcile observations
                               |
                      read-only API / React admin
```

The patched Garm pre-JIT hook, compiled provider and authenticated gateway implement this flow against disposable API fixtures. Every relevant physical create must use the same authoritative gateway; installed-stack bypass resistance remains a deployment gate. Garm still owns GitHub integration and runner lifecycle. This project supplies admission accounting, fixed trusted host placement and visibility. It does not yet choose hosts dynamically. The separate JSON simulator does not implement the network flow.

## Invariants

1. Admission serializes all pools against global, host and pool budgets in one critical section.
2. Positive CPU/RAM/runner resources and known pool/host mappings are required. Zero budgets deny, not unlimited.
3. Every non-retired reservation is charged, including failed, unknown and deleting. Only narrowly proven never-dispatched cancellation can remove a reservation, with a permanent identity tombstone. Physical retirement is not implemented.
4. Retrying an identical request identity returns the original reservation; changing its identity-bound parameters is an error. Retried admission does not perform a provider action.
5. A reservation and a separate one-shot dispatch intent are durable before a physical create. Persistence ambiguity poisons the writer. The gateway requires authoritative fresh reconciliation after restart and never issues another permit for an existing dispatch intent.
6. Host disable/drain/unknown health/stale observation deny new reservations. A stored replay is not permission to create again on a disabled host.
7. Lowering a budget never deletes resources. Overcommitted capacity blocks new admissions and is an operator-visible condition.
8. No inference from age, a create error, a single missing inventory response or controller restart releases capacity.

## Reservations versus inventory

The SQLite implementation reserves global/host/pool capacity and appends an audit event in one `BEGIN IMMEDIATE` transaction. Reservations bind a stable provider request to its target host, pool and resource profile; a separate durable one-shot intent owns physical dispatch. A barrier-first reconciliation merges Garm runners, Incus instances and operations, including creating, idle, busy, deleting, failed and unknown states. Exact identity links prevent double counting a reservation and its instance. Pre-existing or orphaned resources remain charged; unmapped or unbounded inventory blocks admission. There is no TTL-based release, automatic recovery of unknown creates or safe physical retirement.

The original model is monotonic. The SQLite core also supports narrowly proven [never-dispatched cancellation](undispatched-cancellation.md), atomically removing only an exact still-creating reservation with a permanent tombstone. Dispatched or observed allocations cannot be released. It cannot establish a complete live inventory, and demo health is not measured host health. Retention and accounting release require separate designs.

## Storage limits

The gateway uses the local SQLite WAL/FULL ledger, not the original JSON store. It requires one authoritative local database, a trusted private filesystem and cooperating writers. It is not distributed consensus; database readiness cannot fence another process with separate host credentials. Use the [consistent backup and fail-closed restore procedure](backup-restore.md), never an ordinary copy of an open main database file.

For the separate JSON simulator, Linux `flock` on a sibling lock file coordinates cooperating Store objects and processes. Writers re-read the ledger while locked; readers use the same lock. Writes sync a private temporary file, rename in the same directory, then sync that directory. Do not use NFS or share the ledger across controller machines. Do not delete or replace the lock file while a controller may be running. Private directories and a dedicated identity are prerequisites, not solved by advisory locking.

If a write, rename or sync fails, no successful new admission is returned. A rename followed by failed directory sync may already be visible; the Store is blocked, not blindly retried. Reopening reads the durable or visible state but is not proof an external create happened or did not happen. Recovery of external allocations requires authoritative reconciliation first; the JSON simulator is not a live recovery mechanism.
