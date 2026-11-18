# Shared broker grants (schemas 17–18)

This adds durable capacity grants to the existing admission database, not another
ledger and not a continuous allocator or deployment switch. Grants alone never
permit native creation. The stock provider still requires authenticated ingress,
immutable execution policy, a fresh host binding, and one-shot dispatch.

## Accounting and redemption

All database allocation readers count every unused grant, including past its
expiry time. The broker reads policy, inventory, reservations, release tombstones,
grants and the audit revision in one read transaction. Issuance compares that
revision and policy revision inside the same immediate transaction that checks
canonical global, host and pool budgets and records the batch. Batch IDs and
canonical payloads make retry idempotent; changed requests conflict. The API is
server-only and must not be exposed as arbitrary provider or browser JSON.

New stock admission must find an exact unused, unexpired grant for the verified
controller/pool/provider/client leaf, immutable execution profile, requested
resources and eligible bound host. It atomically replaces this charge with the
actual immutable runner reservation, execution plan and origin. There is no
ungranted fallback and no grant ID supplied by the provider. Existing stock plan
replay consumes nothing. Historical schema-16 plans are permanently identified
at migration, rather than adopting new grants.

A consumed grant never becomes available again, even after reservation
cancellation or authoritative retirement. Cancellation preserves the permanent
execution plan and tombstone; the grant follows that identity. An explicit
unused-only expiry or revocation changes its terminal state transactionally.
Clock movement alone never frees a charge. Before an explicit terminal expiry,
a wall-clock rollback can make a still-charged unused grant eligible again; after
terminal expiry it cannot revive. A delayed create after retirement
needs another valid grant or is denied. Grants are bound to immutable host
identity and expire no more than 24 hours after issuance.

Schema migration closes readiness. The current-accounting reservation and dispatch INSERT
triggers reject pre-schema-18 writers that remained open during migration.
Permanent batch manifests detect missing grant history rather than silently
freeing capacity. Backup/restore uses the existing whole SQLite backup path.

## Remaining integration

The trusted broker still needs its continuously refreshed stock/GitHub demand
reader, ledger/profile-to-planner adapter and grant producer. This API enforces
canonical capacity budgets; it does not itself recompute scheduling fairness or
interpret the broker policy as a resource plan. Pool limits are desired targets,
not physical permits. No automatic GARM pool writer or runtime enrollment proof
is introduced. The exclusive native-provider boundary remains a separate gate;
ledger readiness is not proof of that boundary.

## Bounded hot history and durable archival

The 100,000-record bound applies to unarchived/hot grants and batches, not lifetime
issuance. Only an entirely terminal batch can be compacted: every grant must be
explicitly revoked/expired, or consumed with an exact canonical cancellation or
authoritative release tombstone and no unresolved dispatch or linked inventory.
Unused, active, unknown and merely old work never qualifies. Mixed batches remain
hot until every grant qualifies; compaction does not bypass that safety bound.

Compaction stores one immutable gzip-compressed canonical batch inside the SAME
SQLite database and clears duplicated grant/batch JSON atomically. Every grant ID,
batch ID, original hash, final state, consumed request identity and audit entry stays
permanent. Exact retries reconstruct the original payload and disposition; they
cannot recreate grants. Reverse audit linkage and tombstone counts remain checked
on the allocation path. Full startup and backup/restore checks, and exact cold
replay, verify bounded decompression and canonical hashes. Compressed input is
bounded to 2 MiB per batch and inflated input to 1 MiB. No external archive file
or separately restorable data store is introduced.

Run the local operator command against an existing runtime ledger with its separate
protected identity file:

```
capacity-ledger compact-grants --source LEDGER --identity-file IDENTITY --limit 20
```

It processes a bounded page of eligible batches, never enables readiness, activates
a service, deletes audit/identities or releases capacity. The same operation is
available to the owning runtime through `CompactBrokerGrantHistory`. Take normal
consistent SQLite backups; no special archive-copy procedure is needed. Archive
bytes, small tombstones and audit history continue to grow with lifetime usage.
This removes the lifetime entry ceiling, not all storage growth. Do not delete
archive records or tombstones to reclaim space.

Routine metadata and reverse-history checks still traverse retained rows. Their
latency can grow with lifetime history; compaction bounds hot payload memory, not
constant-time metadata work. No constant-time or fixed-size database claim is made.
