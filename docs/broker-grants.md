# Shared broker grants (schema 17)

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
trigger rejects pre-schema-17 writers that remained open during migration.
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

## Bounded history

This implementation retains at most 100,000 permanent grant records and batches.
Issuance refuses a batch before it would exceed that lifetime bound; existing
accounting, dispatch and replay remain usable at the bound. Terminal history is
not pruned. A qualified archival/rotation mechanism is not implemented in this
slice; operators must not delete grant history to regain issuance capacity.
