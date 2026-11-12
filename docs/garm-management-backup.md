# Management journal archives and inspection restores

`garm-journal-archive` makes non-destructive, consistent SQLite snapshots of the
**separate Garm management journal**, including committed WAL contents. It never
contacts Garm/Incus/GitHub, changes a remote setting, clears an intent, initializes
a journal, edits startup configuration or resumes mutations.

A historical journal can omit a later accepted PUT, confirmed request ID or
unresolved operation. **Even an archive with `pending: 0` is not safe to activate.**
Consequently both backups and inspection restores have a distinct SQLite
application ID. Normal `OpenJournal(path, expectedLineage)` refuses them before
returning a usable manager, even when their preserved lineage matches the trusted
configuration. There is intentionally no activate, force, overwrite or replay
option.

## Local commands

Requires Linux, CGO, the pinned SQLite driver and existing owner-owned private
parent directories. Sources must be private regular files, not symlinks. New
outputs must not exist and must have no SQLite sidecars. Initialization is a
separate new-deployment operation, never a recovery action.

```sh
go run ./cmd/garm-journal-archive backup \
  --source /private/management.db \
  --destination /private/backups/management-new.db \
  --lineage "$EXPECTED_JOURNAL_LINEAGE"

go run ./cmd/garm-journal-archive inspect \
  --source /private/backups/management-new.db \
  --lineage "$EXPECTED_JOURNAL_LINEAGE"

go run ./cmd/garm-journal-archive restore-inspection \
  --source /private/backups/management-new.db \
  --destination /private/recovery/management-inspection-new.db \
  --lineage "$EXPECTED_JOURNAL_LINEAGE"
```

`backup` accepts only an existing active-format schema2 journal with the expected
independently configured lineage. It opens the source read-only and uses SQLite's
online backup API rather than copying the open main file. Cooperating writers may
continue; the artifact reflects one consistent SQLite snapshot, not a global
remote-system checkpoint. No source intent/audit row is modified for the backup. The archive contains the
complete journal, including operator reasons and sanitized settings; it is not
encrypted. Keep it private and do not share it without authorization.

`restore-inspection` accepts only a validated standalone archive with the expected
lineage. It creates a new quarantined copy for inspection/recovery preparation,
**not a writable service database**. It preserves all intent, audit, endpoint
binding and lineage records and appends artifact-local provenance. Repeated
inspection copies remain quarantined. It never converts an archive back to an
active application ID.

`inspect` is read-only and returns bounded metadata: archive/parent IDs, operation,
creation time, journal lineage, endpoint-identity hash and intent/audit/pending
counts. It does not print settings, reasons, endpoint addresses, credentials or
job data. The JSON field `writes_permitted` is always false. Artifact IDs and
timestamps describe local provenance, not verified remote fencing or freshness.

## Integrity, publication and failure handling

The tool checks SQLite integrity, foreign keys, source schema/application ID,
immutable lineage, bounded unresolved state and artifact-provenance consistency.
Only schema2 active journals are supported. Legacy schema1 files are not adopted
or migrated by this tool; preserve them for a separately qualified import.

A new private temporary database is populated with SQLite's backup API, validated,
marked as an archive and finalized to standalone DELETE-journal format. The file
is synced and atomically linked without overwriting the destination, followed by
parent-directory sync. Parent directories and the local filesystem are trusted.
Operations are bounded by a 30-second context. No incomplete temporary copy is
published as a successful artifact.

- Existing source/output/sidecar collisions, wrong lineage, unsafe permissions,
  corrupt data and unsupported formats fail closed. Inputs are retained.
- On cancellation or failure before publication, no success is claimed. Preserve
  the source and inspect the destination before another attempt.
- Exit code3 means publication or result reporting is uncertain: the destination
  may already exist and be valid. Inspect it read-only. Never retry over that path,
  delete forensic history or enable writes because a command did not print success.
- Do not copy only an open WAL main file, remove pending rows, change application
  IDs or replace a service journal with an inspection artifact. File owners can
  manually alter SQLite data; the archive marker is a supported-workflow guard,
  not cryptographic tamper protection.

## Recovery limits

Expected lineage detects missing, empty and unrelated replacement databases. It
**cannot detect an owner manually substituting a historical raw active copy with
the same lineage**. Supported archives deliberately do not offer that activation
path. An external durable high-water witness or an independently qualified
all-writer/request/database fence plus complete later history would be needed to
resolve such rollback uncertainty. Neither is implemented here.

A snapshot may be older than a remote commit, and its original request IDs must
never be replayed as new changes. The [recovery protocol](garm-management-recovery.md)
requires authoritative completion/fencing, post-fence observation and explicit
atomic audited disposition before any future resumption. No backup or inspection
command fulfills those requirements. The live journal's pending/unknown stop sign
remains untouched.

The admission ledger, management journal, startup configuration and installed
infrastructure are separate authorities. `capacity-ledger backup` does not include
this journal, and independently taken artifacts are not a coordinated whole-stack
snapshot. Do not claim a consistent production recovery merely because both
individual files pass integrity checks.

## Disposable tests

```sh
go test -race -count=1 ./internal/garmmanagement ./cmd/garm-journal-archive
```

Tests cover committed WAL contents, a pending remote write during backup,
confirmed/unknown history preservation, non-activation despite matching lineage,
inspection-copy provenance, wrong/missing inputs, no-overwrite/sidecar/permission
checks, cancellation, publication ambiguity and output failure. Fixtures do not
access installed services or qualify live disaster recovery.
