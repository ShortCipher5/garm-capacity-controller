# Admission architecture

## Intended enforcement flow

```text
Garm pool -> provider admission adapter -> reserve transaction -> Incus create
                                           |                      |
                                      durable ledger <--- reconcile observations
                                           |
                                     Go API / React admin
```

The adapter must be an unavoidable synchronous boundary for every relevant provider create. Garm still owns GitHub integration and runner lifecycle. This project owns global policy, host scheduling, admission accounting and visibility. The initial simulator does not implement the adapter.

## Invariants

1. Admission serializes all pools against global, host and pool budgets in one critical section.
2. Positive CPU/RAM/runner resources and known pool/host mappings are required. Zero budgets deny, not unlimited.
3. Every non-retired reservation is charged, including failed, unknown and deleting. This slice implements no retirement at all.
4. Retrying an identical request identity returns the original reservation; changing its identity-bound parameters is an error. Retried admission does not perform a provider action.
5. A reservation is durable before permission to create is returned. Persistence ambiguity poisons the current Store object. A future provider adapter must perform authoritative restart reconciliation before acting on recovered reservations.
6. Host disable/drain/unknown health/stale observation deny new reservations. A stored replay is not permission to create again on a disabled host.
7. Lowering a budget never deletes resources. Overcommitted capacity blocks new admissions and is an operator-visible condition.
8. No inference from age, a create error, a single missing inventory response or controller restart releases capacity.

## Reservations versus inventory

A production SQLite transaction should reserve global/host/pool capacity and append an audit event together. Bind reservations to a stable provider request, target host, pool and resource profile. Use a unique request key and fencing/lease generation for provider operations, not a TTL that frees capacity. Reconcile reserved-but-not-issued, creating, idle, busy, deleting, failed and unknown states against both Garm and Incus. Resolve identity before merging, so a reservation and its instance are not counted twice. Pre-existing or orphaned instances consume capacity too; unbounded unknowns block new work.

The original model is monotonic. The SQLite core also supports narrowly proven [never-dispatched cancellation](undispatched-cancellation.md), atomically removing only an exact still-creating reservation with a permanent tombstone. Dispatched or observed allocations cannot be released. It cannot establish a complete live inventory, and demo health is not measured host health. Retention and accounting release require separate designs.

## Storage limits

Linux `flock` on a sibling lock file coordinates cooperating Store objects and processes. Writers re-read the ledger while locked; readers use the same lock. Writes sync a private temporary file, rename in the same directory, then sync that directory. Do not use NFS or share the ledger across controller machines. Do not delete or replace the lock file while a controller may be running. Private directories and a dedicated identity are prerequisites, not solved by advisory locking.

If a write, rename or sync fails, no successful new admission is returned. A rename followed by failed directory sync may already be visible; the Store is blocked, not blindly retried. Reopening reads the durable or visible state but is not proof an external create happened or did not happen. Production recovery requires reconciliation first.
