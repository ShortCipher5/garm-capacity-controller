# Durable remote-management intent foundation

`internal/garmmanagement` wraps the narrow authenticated Garm client with a
**separate, private local SQLite journal**. It does not change the admission
ledger, runner reservations or capacity. It requires Linux, CGO and the pinned
SQLite driver. Use one trusted journal path for every writer to the same Garm
endpoint. It binds to a SHA-256 identity of the configured API destination, not
the bearer token, so rotating credentials preserves history and silently changing
the destination is refused.

## Explicit initialization and expected lineage

`InitializeJournal(newPath)` is a separate new-only operator action. It creates a random immutable64-hex lineage in schema2. `OpenJournal(path,expectedLineage)` opens an existing owner-private matching schema2 journal only; the normal service never initializes, adopts or migrates missing/empty/schema1/wrong-lineage state. Save `Lineage()` in independent trusted durable configuration before first startup. Do not auto-read an expected identity from the file whose identity you are trying to establish.

The parent must already be a real owner-owned0700 directory; files are0600. The explicit initializer refuses existing paths and orphaned SQLite sidecars. The CLI does not chmod arbitrary user directories. Failure to emit initialization output does not authorize deleting/reinitializing the file. No automatic adoption or legacy migration method exists.

This closes accidental bootstrap on missing/empty/newly substituted state, including when an old delayed PUT remains live. It does not prove anti-rollback: an operator substituting a historical raw copy with the **same lineage** can still lose later intents. Such replacement/activation is unsupported and must not be used to resolve uncertainty. Independent external durable history/fencing evidence is required for any future recovery.

## Write protocol

1. The operator reviews a typed update, target UUID and displayed fingerprint.
   Its immutable request UUID and bounded single-line reason identify the review.
2. Look up that request UUID. Confirmed identical requests return the previously
   recorded sanitized observation **without any Garm read or write**. Reusing a
   UUID for different input is rejected. The result is historical, not proof of
   current remote settings; refresh separately.
3. Read the currently scoped remote settings and reject stale fingerprints.
4. In one SQLite `BEGIN IMMEDIATE` transaction, commit the typed before/desired
   settings, request hash, management role, reason, pending state and audit entry.
   SQLite uses WAL, synchronous FULL, foreign keys and a cross-process unique
   unresolved-intent slot. No remote writer is called unless this commit succeeds.
5. Call the existing Garm client once. It performs its own fresh preflight, narrow
   PUT and authoritative readback. The intent does **not** grant automatic retries.
6. Record confirmed, known-pre-dispatch rejection, or unknown with audit in one
   transaction. Completion has a separate bounded background context so caller
   cancellation cannot deliberately erase a completed outcome. A local completion
   failure is itself uncertain.

`Status` and snapshots expose unresolved pending/unknown operations. **Any one
unresolved operation blocks all new writes through the shared journal**, including
after restart and from another process. Reads remain available. A process that
crashes after intent commit but before PUT is indistinguishable from a process
that crashed after remote commit; both remain blocked. Starting a new process,
waiting, changing the request UUID or reloading the browser never clears it.

## Observations are not a completion fence

`ReconcilePending` collects a new authenticated settings read and records either
`observed_desired` or `observed_other`. These names mean exactly an observation,
not "applied" or "not applied". Neither observation clears the unresolved intent:
a delayed upstream PUT may still commit after a GET, another writer may recreate
the same settings, and Garm v0.2.1 offers no atomic conditional update contract.

This foundation deliberately provides **no reset, rollback, retry or unblock
method**. Safe resolution needs a separately qualified operator protocol proving
all relevant remote requests have completed or been fenced, reconciling settings
and durable history, and authorizing a resolution. That protocol is not
implemented. See the [operator recovery protocol and feasibility limits](garm-management-recovery.md). Do not edit the journal, drop pending rows, switch paths or restore
an older copy to make a blocked button work.

The journal serializes only writers using that exact database. It cannot fence
Garm's native UI/CLI, another service, a copied journal or an in-flight request
from an old deployment. External single-writer coordination is still required;
fingerprint preflight plus readback is still **not CAS**, global admission or
job-safe retirement. The client still blocks minimum-idle decreases and excludes
runner-generation/lifecycle changes. See [client limitations](garm-management.md).

## Storage and integration boundaries

- The file and real parent directory must be owner-owned with no group/other access (0600 file,0700 parent), and the existing journal lineage must match trusted independent configuration.
  Symlinks, foreign SQLite application IDs/schemas and non-durable modes are
  refused. Ancestor directories and the local filesystem remain trusted.
- Only typed, sanitized settings and operator metadata are recorded. Never pass
  credentials or private payloads in the reason. The token, upstream response
  bodies, provider specs and runner/job payloads are never journal inputs.
- History/audit reads are cursor-based and capped at 1,000 rows per call. Pending
  is at most one operation. Manager operations have a 30-second context limit;
  completion recording has a separate five-second limit.
- The privileged HTTP layer must still authenticate the distinct management role,
  opt in explicitly, use the same trusted journal path and expose uncertainty
  honestly. Do not turn `ErrOutcomeUnknown` into a retry suggestion or generic
  "not saved" message. `Status` is independent of remote discovery and can show
  a durable stop even when Garm is unavailable.
- The journal is not covered by the admission ledger's backup/restore API. Never
  copy an open WAL main file alone. The [archive tool](garm-management-backup.md) supplies consistent non-activatable
  backups and inspection-only restore copies. It does not supply active recovery
  or an unblock protocol; restoring only the admission ledger does not restore
  this journal. Historical raw same-lineage rollback is not detected by lineage alone.

## Tests

```sh
go test -race -count=1 ./internal/garmapi ./internal/garmmanagement
```

Tests cover atomic intent/audit-before-write, audit rollback, exact-key result
replay without remote traffic, changed-key rejection, known pre-dispatch rejection,
post-remote local journal failure, caller cancellation, process exit and independent
process races. A disposable verified-TLS server commits a real authenticated client
PUT and then returns an error; after journal reopen, matching authoritative reads
remain blocked and no second PUT is sent. These are local fixtures, not live Garm,
Incus or GitHub runner infrastructure and not production qualification.
