# Physical retirement: concrete proof boundary, not an enabled release route

**No physical retirement, DELETE, runner stop, deregistration, release or finalizer
is implemented by this slice.** The new primitive only observes authenticated
project collections. It never grants a permission, changes accounting, pauses a
listener or supplies an external fence. Unknown/busy/ambiguous histories stay
charged. [Automatic CREATE history](automatic-create-evidence.md) is not retirement
history or job-safety authority.

## A source-verified stop-is-drain counterexample

The inspected official [actions/runner v2.323.0, commit d8b82f44](https://github.com/actions/runner/tree/d8b82f44a9ec0258904d5869ae683c31d5aac299)
is a concrete counterexample to treating a generic service stop as job-preserving
drain. It is NOT a claim about the installed runner or a qualification of newer
versions. Any real integration must pin the actual deployed binary/source.

- [Runner.cs](https://github.com/actions/runner/blob/d8b82f44a9ec0258904d5869ae683c31d5aac299/src/Runner.Listener/Runner.cs#L339-L365)
  maps unloading/Ctrl-C to runner shutdown. The message loop is linked to the
  shutdown token; its finally block calls `jobDispatcher.ShutdownAsync()` before
  deleting the listener session ([lines675–703](https://github.com/actions/runner/blob/d8b82f44a9ec0258904d5869ae683c31d5aac299/src/Runner.Listener/Runner.cs#L675-L703)).
- [JobDispatcher.ShutdownAsync](https://github.com/actions/runner/blob/d8b82f44a9ec0258904d5869ae683c31d5aac299/src/Runner.Listener/JobDispatcher.cs#L201-L214)
  explicitly calls `EnsureDispatchFinished(..., cancelRunningJob: true)`.
  Cancellation sends worker shutdown/cancel messages and may kill the worker
  after failure or timeout ([lines614–687](https://github.com/actions/runner/blob/d8b82f44a9ec0258904d5869ae683c31d5aac299/src/Runner.Listener/JobDispatcher.cs#L614-L687)).
- `RunOnceJobCompleted` is signaled in a **finally** block, not solely a successful
  work-completion branch ([lines336–345](https://github.com/actions/runner/blob/d8b82f44a9ec0258904d5869ae683c31d5aac299/src/Runner.Listener/JobDispatcher.cs#L336-L345)).
  A signal, success-looking listener exit or disappearance alone cannot prove the
  intended job-and-post-job completion contract.

Therefore `systemctl stop`, SIGTERM, offline status, deleting a session or an idle
read followed by stop is NOT the implemented job-safe retirement mechanism.
Natural one-job/JIT exhaustion may support a narrower future protocol, but unused
idle runners, classic/broker message races, scale-set behavior, renewal, updates,
restarts, worker descendants and cleanup still require actual qualification.

## Exact missing external proofs

These are requirements, not JSON booleans accepted by the controller.

### No new assignment AND no interrupted accepted work

A future runner/host integration must establish a version-qualified intake seal
serialized with **every** classic/broker job acquisition and dispatch path. It must
resolve in-flight polls/acquire responses and account for all accepted or queued
work, not merely stop polling and assume nothing was accepted. Existing work,
lease renewal, legitimate cancellation messages and post-job cleanup must continue.

A local intake seal is not automatically a GitHub scheduling/session fence. The
service-side handling of outstanding assignment leases and session removal while
work continues must have a qualified contract. The current public busy/DELETE
contract does not supply that token. Do not invent one from an authenticated
`fenced:true` response. A narrow consumed-ephemeral-runner path needs explicit
server single-job guarantees plus trusted natural completion and no restart,
not just the presence of JIT configuration or a completed-job webhook.

The independent supervisor must bind the intent nonce to runner registration,
host boot/process birth identities, exact Incus UUID/generation/creation, original
request/placement/backend audit sequences and protocol versions. It must observe
accepted work AND worker/post-job descendants finished, and hold a no-restart/
reconnect generation through deletion. A job may legitimately end failed or be
cancelled by its owner; retirement itself must not induce its cancellation.
Restart, lease loss or an old-boot acknowledgment invalidates the proof.

No such trusted supervisor/intake/session protocol is implemented or enrolled.
A credential accessible to arbitrary guest job code cannot attest its own safety.

### Old provisioning writers and already accepted remote requests

Runtime ledger lineage prevents accidental wrong-volume initialization. A paused
host and immutable placement prevent NEW claims or reinterpretation. Neither
revokes an old dispatched permit, a different ledger/process, external credentials,
an already authenticated HTTP request, a queued handler or a remote operation.

A real external fence needs a backend-enforced generation that excludes all old
writers and a drained accepted-request frontier: every pre-fence accepted request
must have a known terminal outcome, and none may register/create later. A local
SQLite epoch, TLS certificate revocation, killing a gateway or a firewall rule
alone does not attest that server-side accepted work finished. Existing keepalive
connections, preauthenticated handlers and queues must be covered, not only new
connections. Direct administrative provisioning paths and other scopes need the
same qualification. There is no current backend frontier/fencing API here.

The actual mTLS fixture deliberately accepts an old HTTP write, returns two empty
project collections, then completes that queued write. The observation remains a
record of reads, NOT an external fence or atomic physical absence.

## New read-only primitive: project observations, not physical absence proof

`incusapi.Client.ObserveProjectAbsence(ctx, priorVerifiedIdentity)` performs two
matching recursive instance/operation collections with fresh standalone root
binding checks throughout and a ten-second overall context deadline. It respects
earlier cancellation. No named-instance 404, write, DELETE, ledger barrier or
operator-supplied proof is used. Input is an opaque incarnation previously observed
through actual authenticated transport; zero/operator JSON is not a constructor.

It rejects visible same-name replacement, UUID/generation/name/key aliases,
missing/ambiguous incarnation fields, unbounded allocations, duplicate identity,
unknown or active operations, malformed/wrong-project resources, changing
collections and backend/topology drift. Canonical fingerprints ignore row order
but include the bounded observed identities. Records retain counts/digests and
the original subject, never unrelated guest names, bootstrap, operation metadata
or raw errors. Terminal failure/cancellation is only a non-inflight observation,
NEVER successful create/delete evidence.

`Valid()` means authenticated structured matching READS only. The record explicitly
says `snapshot_atomic:false` and `project_only:true`. Its five outstanding gates
cannot be cleared by a caller, JSON, an empty count, stable hashes or a timeout:

1. external writer frontier/fence;
2. assignment plus accepted/post-job completion fence;
3. authorization visibility and whole-host scope coverage;
4. original DELETE history and a NEW post-delete durable barrier;
5. the unimplemented accounting finalizer.

Two stable reads still cannot stop a writer after the last read. They also cannot
prove unrestricted authorization. Official [Incus6.0.4 operations GET](https://github.com/lxc/incus/blob/a99413afd0c742da3bba10eb4f75d639f9dbbd32/cmd/incusd/operations.go#L543-L569)
filters projects AND permission visibility before producing HTTP200. Trusted TLS
is authentication, not proof that all relevant rows are visible. A restricted
collection may be empty while a target exists. Other projects/daemons on the same
hardware and objects moved out of project remain outside this observation.
Fixtures explicitly demonstrate both successful-read counterexamples.

## Future deletion and release ordering

Only after those external mechanisms exist and are qualified may a future flow:

1. Persist a retirement intent with exact immutable subject, request, original
   dispatch/placement/binding/history references and a one-use challenge.
2. Hold qualified assignment/completion/no-restart and old-writer/frontier fences.
3. Verify the same incarnation immediately before an authorized external delete.
   Incus delete resources are NAME-only and have no UUID/ETag precondition, so an
   exclusive no-rename/restore/recreate window is indispensable.
4. Durably capture the exact original DELETE operation while its short-lived
   record exists. Missing historical CREATE/DELETE proof is not repaired by an
   operator checkbox, later name lookup or a 404.
5. Install a NEW durable post-delete barrier. Collect complete qualified Garm,
   GitHub/session, whole-host Incus and accepted/in-flight-operation absence while
   fences remain held. This project observation can contribute only its limited
   read evidence, never replace the external proofs.
6. A separately reviewed transaction checks unchanged identity/history/fences,
   writes permanent no-recreate retirement history and audit, then retires exactly
   the proven charge without discarding original dispatch/evidence foreign keys.
   Reappearance must be charged/quarantined and close readiness.

This ordering remains a design. There is no finalizer, no hard-coded permission
receipt and no live action hidden behind these fixtures.
