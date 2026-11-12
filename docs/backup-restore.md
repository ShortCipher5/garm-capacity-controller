# Consistent SQLite backup and fail-closed restore

This is a **local offline recovery primitive**, not a live production recovery
workflow or a backup service. It does not connect to Garm or Incus, stop old
writers, schedule backups, manage encryption keys, or resume a gateway. No live
infrastructure was accessed to qualify it. There is no backup/restore CLI.

## Supported APIs

Linux, CGO, and the pinned SQLite driver are required. From code in this module:

```go
// source is an already-open *admission.SQLiteStore.
err := source.BackupSQLite(ctx, "/private/backups/new-snapshot.db")

// The original and snapshot are not overwritten. No ready Store is returned.
err = admission.RestoreSQLite(ctx, "/private/backups/new-snapshot.db",
    "/private/recovery/new-ledger.db")
```

Both destination paths must be **new**. Their immediate parent directories must
already exist, be real directories rather than symlinks, and have no group or
other permission bits (normally mode `0700`). A restore input must likewise be a
private regular file (normally `0600`) in a private real parent directory, not a
symlink. A destination with an existing database, symlink, WAL, SHM, or rollback
journal is rejected. Use a dedicated local filesystem, not NFS. The trusted
local owner must not concurrently rename files, replace parent directories,
create destination sidecars, or independently open the new path during export.
These checks are not a security boundary against the file owner or root.

The source can remain open during backup. SQLite's online backup API captures a
transactionally consistent database, including committed WAL contents. Concurrent
cooperating writers may continue, but a backup is only a historical point-in-time
view, not a record of everything committed before the method returns. **Copying
only an open `.db` file is not an alternative.** Source readiness, reconciliation
fences, reservations, and audit history are not changed by taking a backup.

Export uses a private temporary file in the destination directory. Before
publication it checks application/schema ownership, SQLite integrity, foreign
keys, stored policy, and allocation accounting. It preserves reservations,
inventory, dispatch intents/outcomes, and prior audit history, then durably sets
`ready=0` and clears the reconciliation nonce and barrier timestamp. An audit
entry records `backup_created` or `backup_restored` on the copy. Owned version-one
inputs receive the existing version-two dispatch migration on the copy only.

The finished artifact is closed in DELETE journal mode with all committed data
in its single private database file. File sync, an atomic same-filesystem
no-overwrite hard-link installation, and directory sync precede success. A
concurrent exporter cannot replace the winner. A cancellation observed or failure
before installation removes staging files and publishes no destination. An
unexpected process kill can leave a private `.ledger-backup-*` staging file; it
is **not** a supported restore input or a successful backup. Keep all writers
stopped while inspecting and cleaning crash remnants.

Exports honor cancellation and have an additional 30-second bound; SQLite lock
calls can delay cancellation briefly. A busy or continuously changing large
source may therefore fail rather than wait forever. A poisoned source handle is
not backed up. An error wrapping `ErrUncertain` means installation happened but
a later sync/cleanup could not be verified. Do not treat it as success or blindly
retry over the same path. Keep the old database, inspect the named output, and
use a fresh destination for another attempt. Filesystem/hardware power-loss
behavior still requires deployment-specific qualification.

## Recovery boundary and operator procedure

A backup, including one taken from a ready source, is **never admission-ready**.
Restore invalidates fences again, even if a caller opened and reconciled the
backup after it was created. Passing SQLite integrity checks, restarting a
process, or finding an old healthy snapshot is not permission to provision.

Before using a restored path:

1. Establish a maintenance window and stop/fence **every old gateway writer**,
   provider execution path, and collector. Preserve the original database and
   unresolved operation evidence. The restore API cannot revoke credentials,
   cancel remote work, or fence a process using a different database. Merely
   observing that the old process disappeared is not absence proof.
2. Restore into a new private path. Retain the original database and backup.
   Keep required configuration and external key versions separately; the ledger
   does not store provider credentials or encryption keys. Restrict backup
   storage and any separate archival encryption to the operator's security
   requirements.
3. Open the new path using `admission.OpenSQLite(path, admission.Config{})`.
   Reopening does not clear the durable barrier. New `Admit` and new
   `ClaimDispatch` calls fail closed; existing reservation replay is lookup
   only, never a fresh external-execution permit. Existing dispatch intents
   continue to prohibit another claim.
4. Call `BeginReconciliation(ctx)` on the new Store **before collecting any
   observations**. Never reuse a source/backup fence or a pre-barrier snapshot.
5. Collect authoritative, fresh, complete, bounded Garm runner, Incus instance,
   and in-flight operation inventories for every configured host. Include
   resources and creating/ambiguous operations that appeared **after the backup**
   and therefore have no historical reservation. Unbounded or unresolved
   placement must leave admission blocked, not be reported as empty inventory.
6. Call `CompleteReconciliation(ctx, newFence, snapshot)`. Incomplete or
   pre-barrier views leave the barrier closed. Previously charged resources
   missing from the new view become unknown and remain charged. Observed orphans
   are charged; absence from a historical backup or a single list is not release
   evidence. There is still no allocation-release API.
7. Resume scheduling only after the complete new reconciliation succeeds and
   external credential isolation, bypass resistance, and host-budget boundaries
   have been independently established and qualified. This repository does not
   yet implement that production orchestration.

The model trusts the adapter's completeness and identity assertions. It cannot
prove an omitted post-backup operation does not exist or recognize resources an
adapter falsely reports as absent. If the old database contains dispatch history
newer than the backup, preserve it for conservative reconciliation; do not
interpret a missing historical intent as permission to retry a create.

## Verification and remaining gates

```sh
go test -race -count=1 ./internal/admission -run SQLiteBackup
go test -race -count=1 ./...
go vet ./...
gofmt -l .
```

Disposable local tests cover committed WAL state, concurrent writers and atomic
reservation/audit snapshots, source readiness/fence preservation, closed artifact
readiness, active fence invalidation on both export and restore, dispatch-intent
preservation, refusal before a new reconciliation, post-backup creating orphans,
missing-resource retention, incomplete inventories, truncated/empty inputs,
private paths, symlinks, destination sidecars, no-overwrite concurrent publication,
canceled export cleanup, and poisoned source handles.

Not qualified: real Garm/Incus recovery, old-writer fencing, adapter inventory
completeness, hard power-loss/process-kill fault injection, hardware durability,
large-database performance, remote archival/encryption/retention, scheduling,
operator authentication, or a live gateway service. These model tests are not
production acceptance.
