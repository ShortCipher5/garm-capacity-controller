# Automatic original-CREATE evidence collection

The concrete gateway now attempts real authenticated CREATE evidence capture
inside the original physical-create callback, **not retirement or recovery**.
There is no release, DELETE, dispatch retry, manual override or new HTTP/CLI route.
The [journal](create-observation-journal.md) and [future manual recovery gates](manual-accounting-recovery.md)
retain their full scope, immutable backend/dispatch provenance and fencing limits.

## Exact point in the existing create path

The original bound dispatch still proceeds:

1. Commit the one-use dispatch intent with immutable backend provenance.
2. Make the physical create POST once.
3. Durably record its validated submitted operation UUID/resource BEFORE Wait.
4. Wait for terminal success and verify the exact running tagged allocation.
5. NEW: attempt identity observation, exact original terminal-operation capture
   and immutable CREATE journal append.
6. Persist the ordinary confirmed outcome and perform the existing final read.

The collector is not invoked on lost create responses, failed/unknown outcomes,
failed running readback, submitted replays or confirmed replays. It never hunts
for a replacement operation UUID or similarly named instance. Confirmed replay
returns the existing verified resource without another POST, Wait or capture.

Only a trusted credential-owning backend supplies the optional observation
capability. The actual `incusapi.Client` implements it; a provider/bootstrap
caller cannot enable it, supply a verified receipt or change the trusted subject.
Unsupported trusted test/legacy implementations produce unconfirmed collection,
not a fabricated journal record or a new provisioning attempt.

## Bounded attempt, not retention guarantee

One independent five-second context deadline spans identity GETs, operation GET
and the append attempt. It survives a caller transport disconnect after running
verification so a completed create does not automatically lose its short-lived
receipt. There are no detached collector workers, polls, retries or later replay
attempts. Underlying SQLite locking and synchronous durability retain their
existing cancellation/uncertain-commit semantics; a context deadline is not a
hard real-time fsync limit and a write worker is never abandoned for a quick reply.

Incus 6.0.4 purges terminal operations after about five seconds from completion,
not from the collector starting. Wait/readback/network delay can consume that
window. Capture is best effort, never guaranteed. A 404, expiry, unsupported
version, malformed response, incarnation drift, deadline or ordinary rolled-back
append refusal must not turn into evidence. Historical unknown records are not
silently upgraded by this path.

## Failure semantics

Missing/refused collection emits exactly one fixed diagnostic for that original
callback:

```text
create operation evidence collection unconfirmed; allocation remains charged
```

No raw error, endpoint, bootstrap, operation metadata or provider identifier is
included. "Unconfirmed" intentionally does not assert that the journal transaction
failed to commit: a reply can be lost AFTER a durable write.

A plain capture failure does not invalidate the existing verified guest or its
charge. Ordinary confirmed provisioning may still finish, with no CREATE journal
entry. That is NOT proof of retirement eligibility. Absence of a journal record
is only absence of recorded evidence, never physical absence or a retry permit.

An uncertain journal commit or failed runtime-lineage guard is different: the
callback refuses confirmation, the normal ledger writer remains fail-closed,
and original submitted operation/resource correlation plus the charge are retained.
The code does not regenerate a missing runtime identity, activate an archive or
repair a lost ledger. A later safe read may find the journal record committed;
it still cannot authorize release. No caller should assume that a failed RPC
means the physical guest did not get created.

Stored CREATE evidence is still name-only operation history with same-incarnation
readback around capture, not exclusive history, external writer/job fencing or
post-delete absence. No finalizer or automatic job-safe drain is implemented.

## Disposable verification

Tests exercise the real Facade → concrete mutual-TLS Client → runtime SQLite path,
including one physical fixture POST and a durable submitted UUID visible before
both Wait and terminal capture. They verify automatic capture before confirmation,
no replay recapture, fixed/redacted failure diagnostics, operation expiry and
malformation, read/generation drift, network deadline, caller disconnect,
unknown/lost-response behavior, ordinary audit rollback, actual deferred-FK commit
failure and runtime-lineage loss. Every outcome retains its allocation charge.

These are disposable loopback fixtures, not a live host or installed Garm
qualification. No real runner, registration, guest, job or infrastructure is touched.
