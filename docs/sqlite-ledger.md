# SQLite admission ledger and conservative reconciliation

This slice implements an **offline accounting core**, not live Garm/Incus enforcement. It neither replaces Garm nor supplies a provider admission adapter. Caller-supplied complete observations are a trusted internal contract, not proof that real infrastructure was completely observed.

## What is implemented

`admission.OpenSQLite(path, initialPolicy)` creates a private local database, using the pinned `github.com/mattn/go-sqlite3` driver. Building this package's SQLite tests requires Linux, CGO and a C compiler. Dependencies are pinned in `go.mod` and `go.sum`. The original JSON store remains available and is not silently imported or migrated.

SQLite uses WAL, FULL synchronous durability, foreign keys, STRICT tables and a bounded busy timeout. Every operation runs in a `BEGIN IMMEDIATE` transaction: all global/host/pool budget reads, reservation writes and the corresponding audit event commit together. Cooperating handles and processes sharing the same local database serialize before reading capacity, so independent repository pools cannot write-skew the model's global budget. A busy, canceled, corrupt or unavailable database returns no new admission. An ambiguous commit outcome blocks the affected Store handle rather than retrying an external action.

Only initial database creation accepts initial policy. Reopening does not reset budgets, readiness, reservations or observation age. A different initial config cannot silently replace existing policy. An application ID identifies owned databases. Schema version 2 adds the one-shot dispatch journal; current version 3 adds immutable never-dispatched cancellation tombstones and conservatively excludes all preexisting v1/v2 keys from cancellation; unknown versions and unrelated nonempty databases are rejected. Ownership is inspected read-only before enabling WAL, so refusing a foreign database does not first change its journal mode. There is no JSON migration, policy editor, retention job or physical-retirement API. A narrow trusted-library [never-dispatched cancellation](undispatched-cancellation.md) is available. See [the dispatch boundary](gateway-dispatch.md) for its precise at-most-once and recovery limits.

## Reconciliation barrier

A new database starts with admission blocked, even if policy contains healthy hosts.

1. `BeginReconciliation(ctx)` commits a global admission barrier and a new epoch plus random nonce **before collection starts**.
2. A future trusted adapter collects complete Garm, Incus and in-flight operation views for every configured host. Each host observation must explicitly assert complete sources and bounded inventory, be fresh, and be collected at or after the barrier's timestamp. Exactly one observation per configured host is required.
3. `CompleteReconciliation(ctx, fence, snapshot)` atomically validates and imports those bounded allocations, updates host observation freshness/health, records an audit event and opens admission. It consumes its nonce. Old, overlapping, wrong-nonce or already-consumed completions cannot reopen or alter state.
4. A crash after Begin, invalid/partial response, unknown identity, arithmetic overflow or duplicate binding leaves the durable barrier closed. A new Begin supersedes an abandoned collector. No timeout opens the barrier.

Every new admission rechecks readiness and **all hosts'** observation freshness inside its budget transaction. Snapshot reads also consume expired readiness, so an overview never reports an expired view as ready merely because no new admission was attempted. If any required view expires or moves into the future, admission closes readiness until a new barrier and complete snapshot succeed. Observation updates preserve enabled/draining/disabled policy; they cannot re-enable a host. An unhealthy target remains ineligible.

These fences coordinate the local ledger only. They do not fence Garm, Incus, a provider executable or a second independent database. Production startup/recovery must reconcile before admitting against externally changing infrastructure. Snapshot completeness, source identities and bypass resistance must still be proven by pinned disposable-stack integration tests.

## Accounting rules

- Every reservation remains charged, including creating, idle, running, failed, unknown and deleting. Retry lookup returns the original reservation, not permission to repeat a create, even while new admissions are blocked.
- Inventory without a reservation is charged separately. Pre-existing/orphaned resources are not free capacity.
- An inventory identity can bind one reservation only, on the same host/pool. That allocation is charged once at the **component-wise maximum** of reserved and observed CPU/RAM, not twice and not at the smaller profile.
- Resource IDs, pools and reservation bindings are immutable after first import. Duplicate links and attempts to late-bind an already charged orphan are rejected instead of silently freeing capacity. Resolving those identities requires a future explicit reviewed workflow.
- Lower observations never decrease an inventory resource's charged profile. Missing previous resources become unknown and remain charged. A single missing list entry, create error, elapsed time or restart is never deletion proof.
- Over-budget observations are retained and block further admissions. Integer overflow fails the entire import closed, with no partial state.

This intentionally leaks capacity conservatively until a separate confirmed-retirement design exists. There is **no physical-retirement release API**. The sole exception is exact still-creating work with fresh authoritative history, no dispatch and no linked resource, atomically cancelled with a permanent tombstone through [CancelUndispatched](undispatched-cancellation.md). It is suitable for tests and developing adapter contracts, not operating a real fleet indefinitely.

## Durability and trust limits

Use a private directory and a dedicated local identity. The database file must be a private regular file. WAL, SHM and database files belong to the same installation; do not use NFS, share the database between controller machines, or independently edit it. SQL constraints are defense in depth, not protection from a user who owns the files.

Copying only an open `.db` file is **not a safe backup** in WAL mode. The `BackupSQLite` and `RestoreSQLite` library APIs use the SQLite online backup facility, produce private standalone admission-blocked artifacts, and refuse overwriting a destination. See [backup and recovery procedure](backup-restore.md). The local `capacity-ledger` CLI wraps these APIs; there is no qualified live recovery service. Any restoration must be followed by reconciliation against authoritative external state before live admission; restoring a historical ledger cannot prove external resources disappeared.

Audit events contain bounded operation kinds and request identities, not raw provider payloads, credentials or error dumps. This is transactional history, not tamper-proof security auditing, and retention/export/authorization are not implemented.

## Verification

Run on Linux with Go 1.25+ on the integrated facade branch, CGO enabled and a C compiler:

```sh
go test -race -count=1 ./internal/admission -run SQLite
go test -race -count=1 ./...
go vet ./...
```

Tests exercise concurrent handles and separate processes; reservation/audit rollback; ambiguous-commit handle poisoning; reopen after an unfinished barrier; stale/duplicate/overlapping fences; incomplete/pre-barrier observations; global freshness expiry; conservative deduplication, orphans and missing resources; immutable bindings; host policy preservation; overflow; busy/canceled transactions; and unsupported schema/private-file checks. These are model/database tests, not an installed Garm/provider compatibility certification or a hardware fault/backup qualification.
