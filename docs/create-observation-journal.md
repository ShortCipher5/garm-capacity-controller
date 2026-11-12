# Original CREATE observation journal

This is non-destructive history storage, **not manual accounting finalization**.
No external delete, job drain, dispatch retry, state promotion, inventory removal,
capacity release, automatic rebind, HTTP handler or CLI recovery route is added.
Unknown/submitted/confirmed allocations remain charged in their original states.
The [manual recovery requirements](manual-accounting-recovery.md) still apply.

## Trusted library boundary

A credential-owning collector can call:

```go
identity, err := client.ObserveInstanceIdentity(ctx, trustedExpectation)
observation, err := client.CaptureTerminalOperation(ctx, identity,
    originalDispatch.OperationID, incusapi.RecoveryCreate)
entry, err := store.StoreCreateObservation(ctx, exactRequest, observation)
```

Every error must be checked before proceeding. The original dispatch operation
must already be durably recorded from the original create response. A controller
must capture while the terminal record is available: Incus 6.0.4 removes it after
about five seconds. The [automatic post-submitted collector](automatic-create-evidence.md)
now attempts this once inside the original verified successful create callback.
It cannot reconstruct expired operations. Failed, missing, lost or unrecorded
evidence stays unknown.

Only the opaque `incusapi.TerminalOperationObservation` minted by real verified
transport reads is accepted. Serialized records, pasted operator receipts, a
configured `verified` boolean and zero values are not constructors. DELETE
observations are refused until a separately reviewed durable retirement-intent
and external-fencing model can identify the intended operation.

## Atomic qualification, not new authority

One `BEGIN IMMEDIATE` transaction checks:

- exact still-present persisted request and its unchanged declared resources;
- a submitted/confirmed/unknown dispatch with its original known operation UUID
  and exact named resource, never an intent without an operation identity;
- positive immutable dispatch `HostBindingSequence` matching the v4 host binding's
  audit sequence. A raw simulation dispatch or unbound historical row has no such
  provenance and cannot be upgraded by a later binding or wall-clock comparison;
- the entire authenticated backend key, origin, exact project scope, reported
  daemon name and qualified version against that unchanged durable binding;
- exact observed pool, key, bounds and the gateway's current deterministic request
  correlation `SHA256(controllerID + NUL + poolID + NUL + name)`;
- no unqualified legacy host or cancellation tombstone;
- valid original CREATE observation with same-incarnation readback, not DELETE;
- local binding/dispatch/observation/capture/store append order, rejecting a local
  clock rollback rather than inventing server/client clock synchronization.

The backend audit sequence is transactional provenance. Local timestamps are
supplementary consistency checks, not evidence that another writer was fenced.
Remote creation/update times remain reported server values, not completion times
or qualified lifecycle chronology. Both CREATE and DELETE operation resources are
name-only; same-subject readback around capture does not prove exclusive history
before the first observation. This journal establishes neither job safety nor
physical absence and is never permission to finalize retirement.

A same-TLS-key name/version/scope change is not a harmless refresh. It must fail
transport pin checks or the atomic durable binding comparison. No automatic
requalification occurs.

## Immutable, bounded records

Schema 5 adds one append-only `create_evidence` table. Each request has at most one
first observation; `(scope, operation UUID)` is also unique. Foreign keys preserve
its original dispatch and binding-audit association. Update/delete triggers forbid
rewriting that history through the application database interface.

The record is a bounded canonical JSON whitelist, at most 16 KiB, plus indexed
identity fields and a SHA256 corruption-detection digest. It contains no bootstrap,
raw operation metadata, certificate, private key, arbitrary endpoint or raw error.
The digest is not a signature and does not protect against an administrator who
controls the ledger or Incus. The private local filesystem and credential boundary
remain prerequisites.

Retries with the same request, backend provenance, remote subject and operation
return the FIRST stored record with `Replay=true`, preserving its timestamps.
Later local observation/capture times and current instance status do not overwrite
history. Any remote identity/operation contradiction fails closed. There is one
audit insertion atomically with the new record, never a second audit on replay.
Corrupt or noncanonical stored records are rejected rather than silently repaired.

`LookupCreateEvidence(ctx, key)` uses a physically read-only indexed transaction.
It returns redacted historical data, not an opaque verified constructor or a live
absence/authorization view. No full-history administrative scan or importer is
provided. An uncertain write commit uses the store's existing poison behavior;
after reopening, inspect/retry the exact identity, never create another runner.

## Migration and recovery

Open and backup/export support owned schemas 1 through 5. Opening older ledgers
migrates conservatively: v3 cancellation tombstones and provenance exclusions,
v4 host bindings, binding audit sequences and legacy-host exclusions are retained.
No migration manufactures a CREATE observation, changes a dispatch result or
removes a charge. The v4 migration's admission-blocking and legacy qualification
rules remain unchanged. Schema 5 itself does not alter readiness.

The supported SQLite online backup and operator backup/restore CLI preserve the
journal along with original dispatch/binding/cancellation history. Artifacts are
still admission-blocked, without a usable old reconciliation fence. A historical
backup cannot include evidence or creates committed after it was taken. External
old-writer/credential/queued-request fencing, recovery of later authoritative
history and a NEW complete post-barrier inventory remain mandatory. The CLI's
acknowledgment flag is not proof that any of those external actions happened.

## Disposable verification

Tests use actual mutual TLS and an explicit one-shot create against disposable
local API fixtures, then real identity/operation GETs and SQLite append/readback.
They check no additional backend writes, unchanged charges/states/readiness,
opaque/JSON-forged/delete rejection, unknown/no-op/unbound history, full binding
and generation drift, first-record replay, audit rollback, concurrent handles,
corrupt/noncanonical history, historical migration and operator CLI backup/restore.
No real host, runner registration or live job is contacted or modified.
