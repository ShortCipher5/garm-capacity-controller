# Consistent SQLite backup and fail-closed restore

This is a **local offline recovery primitive**, not a live production recovery
workflow or a backup service. It does not connect to Garm or Incus, stop old
writers, schedule backups, manage encryption keys, or resume a gateway. No live
infrastructure was accessed to qualify it. The `capacity-ledger` CLI wraps these
local primitives; it does not automate the live recovery procedure.

## Local operator CLI

Build on Linux with CGO and the pinned SQLite dependencies:

```sh
go build -o ./capacity-ledger ./cmd/capacity-ledger
./capacity-ledger --help

# Existing private local ledger; cooperating writers may remain active.
./capacity-ledger backup --source /private/ledger/source.db \
    --destination /private/backups/new-snapshot.db

# FIRST stop/fence old writers, provider execution, collectors and queued replay.
# This flag acknowledges work performed separately. It does not perform or prove it.
./capacity-ledger restore --source /private/backups/new-snapshot.db \
    --destination /private/recovery/new-ledger.db --old-writers-fenced
```

The tool never creates a missing source, migrates the source in place, overwrites
an existing destination, removes existing sidecars, starts a service, changes a
gateway configuration, or enables admission. Both commands open an existing source
read-only and use the same WAL-aware SQLite export and artifact validation as the
library. Historical schema migrations apply to the copy only. Destinations and
source paths must satisfy the private-file and local-filesystem rules below.
There are no `--force`, `--ready`, reconciliation, or resume options.

Restore refuses to run without explicit `--old-writers-fenced` acknowledgement.
That acknowledgement is **not a fencing mechanism or evidence of safe recovery**.
Keep every old external execution path and queued request replay fenced. Preserve
newer dispatch/cancellation history, quarantine request identities whose complete
history cannot be recovered, and account for ambiguous in-flight work before
using the restored path. The complete operator procedure below remains mandatory.
The CLI never collects inventory and cannot verify external credentials, physical
allocation boundaries, or post-backup operation history.

Exit codes: `0` confirms publication of a standalone private **admission-blocked**
artifact; `2` means invalid usage or missing fencing acknowledgement; `1` means
failure/interruption; `3` means an uncertain result that may already have installed
the destination. Never interpret a nonzero exit as proof the destination is absent.
For an uncertain result, retain the source and backup, inspect the destination
offline, and use a fresh destination for another attempt. Errors deliberately omit
raw driver messages, source contents, and operator-supplied paths/arguments.
SIGINT/SIGTERM cancel export; the same library cancellation/durability limits apply.
Successful restore prints the mandatory fresh barrier/inventory and stopped
scheduling reminder, not a claim that recovery or external fencing is complete.

## Supported APIs

Linux, CGO, and the pinned SQLite driver are required. From code in this module:

```go
// Existing path opened read-only; no source creation or in-place migration.
err := admission.BackupSQLiteFile(ctx, "/private/ledger/source.db",
    "/private/backups/new-file-snapshot.db")

// source is an already-open *admission.SQLiteStore.
err = source.BackupSQLite(ctx, "/private/backups/new-snapshot.db")

// The original and snapshot are not overwritten. No ready Store is returned.
err = admission.RestoreSQLite(ctx, "/private/backups/new-snapshot.db",
    "/private/recovery/new-ledger.db")
```

All destination paths must be **new**. Their immediate parent directories must
already exist, be real directories rather than symlinks, and have no group or
other permission bits (normally mode `0700`). An input to `BackupSQLiteFile` or `RestoreSQLite` must likewise be a
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
inventory, dispatch intents/outcomes, included cancellation tombstones, and prior audit history, then durably sets
`ready=0` and clears the reconciliation nonce and barrier timestamp. An audit
entry records `backup_created` or `backup_restored` on the copy. Owned version-one
and version-two inputs receive dispatch/cancellation migrations through current version three on the copy only. Included tombstones are preserved; later history absent from an older backup must be recovered or old identities quarantined before replay. See [cancellation history limits](undispatched-cancellation.md).

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
not backed up through its method. The file entry point cannot inspect another
process's poisoned handle or determine its external operation history; preserve
that evidence separately. An error wrapping `ErrUncertain` means installation happened but
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
   evidence. There is still no physical-retirement release API. Narrow never-dispatched cancellation requires fresh ready baseline and authoritative history; a restored blocked snapshot alone cannot authorize it.
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
go test -race -count=1 ./cmd/capacity-ledger
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

CLI tests additionally exercise the actual built executable, required fencing
acknowledgement, argument validation before database access, missing-source
refusal, redacted errors and uncertainty handling, private standalone output,
destination sidecar preservation, committed WAL contents, historical source
preservation, and restored dispatch refusal until new complete reconciliation.
Schema-three binary tests preserve unknown dispatch operation/resource correlation,
105 permanent cancellation tombstones (including keys outside the 100-entry read
view), migration exclusions, and exact idempotent cancellation replay while blocked.
Both historical v1 and v2 ledgers are exported/restored through the compiled CLI;
the source bytes stay unchanged, only the copy migrates, and legacy exclusions
still prevent cancellation after a new complete baseline. A source collector fence
survives backup but is rejected by both artifacts; incomplete and pre-barrier
inventories cannot reopen either artifact. These tests do not simulate or prove
external fencing, missing post-backup history recovery, or real host completeness.

Not qualified: real Garm/Incus recovery, old-writer fencing, adapter inventory
completeness, hard power-loss/process-kill fault injection, hardware durability,
large-database performance, remote archival/encryption/retention, scheduling,
operator authentication, or a live gateway service. These model tests are not
production acceptance.
