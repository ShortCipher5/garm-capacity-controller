# Manual accounting recovery: evidence capture, not automated drain

**No release/delete/recovery route is enabled.** This slice implements real
credential-verified, read-only Incus identity and terminal-operation observations.
It does not change a ledger, dispatch state, charge, registration, guest or policy.
Automatic job-safe drain remains separate and unqualified.

## Actual read-only API

From trusted controller policy, construct an `incusapi.IdentityExpectation` with
exact name, controller, pool, reservation key, CPU and RAM. Call:

```go
identity, err := client.ObserveInstanceIdentity(ctx, expected)
observation, err := client.CaptureTerminalOperation(ctx, identity, knownOperationUUID,
    incusapi.RecoveryCreate) // or RecoveryDelete for externally performed cleanup
```

Both results are opaque values minted only by authenticated transport reads.
Zero values and pasted operator JSON cannot manufacture a valid result. Their
explicit `Observation()` / `Record()` methods return bounded whitelisted data for
a future immutable evidence journal; serialized data is NOT a verified constructor.
No bootstrap, raw operation metadata, certificate, arbitrary URL or error body is
retained. There is no general endpoint/proxy or write method.

Identity observation requires a fresh trusted standalone daemon identity, actual
TLS peer key binding, exact reported server version `6.0.4`, tagged bounds, canonical
UUID, UUID generation and nonzero creation time. It has a ten-second overall bound.
Terminal capture has a five-second overall bound, respects earlier cancellation,
and performs one operation GET, never redispatch or timeout recovery. It requires:

- exact previously known operation UUID and matching daemon/project scope;
- task class and pinned create/delete description;
- explicit terminal status 200 / Success and explicit empty error;
- exactly one canonical instance resource in the configured project;
- valid operation creation/update times;
- for create, a new identity read matching the entire prior incarnation subject.

Source is pinned to [Incus v6.0.4 commit a99413af](https://github.com/lxc/incus/tree/a99413afd0c742da3bba10eb4f75d639f9dbbd32).
[Terminal operations are removed from memory and database after about five seconds](https://github.com/lxc/incus/blob/a99413afd0c742da3bba10eb4f75d639f9dbbd32/internal/server/operations/operations.go#L227-L277).
A 202 response is only operation creation, not completion. A missing/expired
operation, a 404, failure/cancellation, malformed result or lost response is
unknown, never terminal-success or physical-absence evidence. Capture must happen
while the record exists; later polling cannot reconstruct missing history.

Server timestamps are parsed reported values, not a qualified clock. Incus 6.0.4
sets terminal status without advancing `updatedAt` in [Start](https://github.com/lxc/incus/blob/a99413afd0c742da3bba10eb4f75d639f9dbbd32/internal/server/operations/operations.go#L277-L333).
That field is NOT completion time and can precede an instance's database creation
time. This read-only API does not reject cross-object/future timestamps as if a
server/client clock-skew contract already existed. Any future journal/finalizer
must record local durable intent/capture/barrier ordering, qualify clocks where
needed, and reject contradictory lifecycle evidence under that contract.
`Valid()` never means chronology, exclusive history, absence or release eligibility.

The public Operation API exposes class and description but no numeric server
operation type or instance UUID. Pinned descriptions are `Creating instance` and
`Deleting instance`. Non-default project resource URLs must explicitly identify
the project; the default project uses its pinned omitted-query representation.
Other server versions and URL representations need separate qualification.

## DELETE observation is deliberately weaker

All Incus operation resources refer to names, not instance UUIDs. Create readback
proves consistency around capture, not exclusive history before the first read;
it too requires the original known journal operation and exclusive create path.
An Incus delete operation resource likewise refers to a NAME. The [delete handler](https://github.com/lxc/incus/blob/a99413afd0c742da3bba10eb4f75d639f9dbbd32/cmd/incusd/instance_delete.go)
loads project/name and has no immutable UUID/ETag deletion precondition. Therefore
preobserved UUID plus a successful same-name delete is NOT incarnation-bound
retirement proof without an exclusive no-rename/restore/recreate window.

Delete records explicitly have `ResourceReadbackMatched=false` and
`RequiresExclusiveFence=true`. The capture API does NOT use a later instance
GET404 as absence proof and does not assert that a job finished safely. Create
records re-read the same incarnation but still require the separate fencing and
absence protocol before any future accounting release.

## Required future manual protocol

This sequence is a design, not an enabled operator command:

1. Qualify credential isolation, one daemon per modeled hardware budget, immutable
   backend/scope bindings and pinned transport versions. Reject duplicate aliases,
   unbounded scopes, name/UUID/generation drift and unknown old writers.
2. Persist a retirement intent bound to exact request, dispatch, backend/project,
   incarnation and evidence versions. Keep the charge and permanently prohibit
   reuse of that identity. Stop NEW claims, then actually fence all old writers,
   credentials and queued requests. A SQLite barrier cannot revoke a permit
   already delivered to a provider or work on another database.
3. Require durable authenticated terminal CREATE evidence matching the journal's
   original known operation UUID, resource and request. An unknown intent lacking
   that UUID cannot be silently upgraded by observing a similarly named guest.
   Current confirmed records lacking incarnation/backend proof are not magically
   upgraded. Missing historical evidence remains charged pending a separately
   reviewed recovery design, never an operator-checkbox override.
4. A qualified operator performs job-safe deregistration, completion/cleanup and
   physical deletion OUTSIDE this controller. Arm trusted read-only capture before
   the action and durably record terminal DELETE evidence within its short
   retention window. Do not call idle, offline, Garm pending_delete, force/bypass
   flags or a completed-job webhook an atomic job-assignment fence.
5. AFTER terminal deletion, install a NEW durable reconciliation barrier and
   collect fresh complete Garm, standalone Incus and in-flight-operation absence
   while the exclusive writer/identity fence remains active. A cluster HTTP200 can
   be partial, so clustered recovery is unsupported. One missing entry or 404 is
   insufficient. Scope, key or incarnation changes and active/unknown operations
   block the workflow.
6. Only then may a separately reviewed finalization transaction verify the exact
   unchanged intent/request, proof versions, fresh one-use fence and absence,
   write permanent no-recreate retirement history plus audit, and remove exactly
   the proven charge. Preserve original dispatch and evidence history; do not
   blindly delete rows protected by dispatch foreign keys. Reappearance must be
   charged/quarantined and close readiness, never resurrect a retired identity.

A typed operation observation alone satisfies NONE of the missing external writer,
job-safety, physical absence, durable history or finalization requirements. The
next storage slice must journal whitelisted observations atomically without
changing charges or dispatch states, preserving v3 cancellation tombstones and
v4 host bindings. No evidence-retirement accounting exemption is implemented here.

## Verification boundary

Disposable mutual-TLS fixtures exercise actual root/instance/operation GETs,
unknown/malformed/failed/cancelled/expired operations, canonical project and
operation identity, tagged bounds, UUID/generation/creation drift, opaque JSON,
redaction, exact-version gating and explicit delete-proof weakness. They verify
zero writes, not live stack qualification. No real infrastructure was accessed.
