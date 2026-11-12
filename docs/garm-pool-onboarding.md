# Disabled pool onboarding foundation and activation boundary

This is an experimental **typed library foundation**, not a working dynamic pool
console. There is no new HTTP route, CLI flag, browser create/enable action, or
runtime wiring. Existing startup maps and management allowlists remain unchanged.
It must not be exposed until durable route enrollment and activation coordination
are implemented and qualified. No live Garm/Incus operations were used in tests.

## Pinned API facts

Public Garm v0.2.1 commit `154638445c3949c1958b01812f69d9a1e4d82684`
supports `POST /api/v1/repositories/{repoID}/pools`. `CreateRepoPool` validates the
creation parameters, resolves a bootstrap template, then inserts the pool; it
**does not call provider `ValidatePoolInfo`**. Provider validation is not the
blanket creation blocker. Actual Runner/SQLite tests cover this boundary.

A disabled pool with minimum idle zero still needs `max_runners >= 1`; zero is
rejected by upstream validation. Stock/unopted Garm rejects an explicit
`template_id: 0`. The separate source8 native bootstrap protocol permits explicit
zero only for its correctly opted-in provider and stores SQL NULL, avoiding a
legacy bootstrap template. This foundation exclusively requests that native
zero mode. It does not create a positive-template pool and silently change its
generation later. A client opt-in expresses intent, not proof that the installed
Garm is patched, compatible or qualified.

## Implemented library sequence

`garmapi.ManagementConfig.AllowDisabledPoolCreation` is independently false by
default; both that opt-in and `AllowWrites` are necessary. No gateway runtime
configuration enables it. A dedicated upstream credential, verified HTTPS trust,
exact repository allowlist and provider scope are still mandatory. The low-level
transport alone is not a journal or safe production administration service.

`garmmanagement.Manager.CreateDisabledPool` is the coordinator foundation:

1. Own and canonicalize a bounded trusted spec. It contains only provider, image,
   flavor, architecture, tags and bounded maximum, with no arbitrary specs,
   credentials, URLs, scripts or instance payload. A future handler must select a
   fixed server-side template reference, not accept arbitrary execution policy.
2. Check a fresh repository fingerprint, then atomically commit an immutable
   scoped creation intent and audit **before POST**. This is preflight, not an
   upstream CAS or external-writer fence.
3. Send exactly one POST with `enabled:false`, `min_idle_runners:0`, bounded
   positive maximum, Linux, `template_id:0`, empty extras and shell disabled.
4. Capture an acknowledged canonical pool UUID durably, while the intent remains
   pending, **before any readback GET**. A malformed acknowledgement may preserve
   a returned UUID as an observation, but never as verified ownership/readiness.
5. Read that exact repository/pool UUID, through an opaque client-bound receipt.
   Verify scope/provider, fixed spec, native template-zero shape, disabled/min0,
   empty children and extras, and shell false. No pool allowlist is expanded.
6. Commit a sanitized outcome. A confirmed result contains request/pool/repository
   IDs, journal lineage, management-endpoint identity hash, spec hash, provider,
   generation and last readback fingerprint. It always says `route_required:true`,
   `route_installed:false`, `activation_allowed:false`.

Creation uses the existing journal's repository-subresource category with explicit
`operation:create_disabled_pool` and protocol `native-disabled-repository-pool-v1`.
There is no schema migration. It shares the same cross-process single unresolved
slot as pool/repository updates. Confirmed request-ID replay returns historical
recorded evidence with **no remote traffic**, not a fresh observation. Changed
request-ID reuse is rejected.

## Failure and recovery behavior

- Lost/failed/redirected POST response: unknown, globally blocked, no retry.
- Valid UUID but unsafe response: preserve the UUID, unknown, no readback permit.
- UUID capture/audit failure: remain pending, no readback, no further mutation.
- Readback timeout/mismatch or outcome-journal failure: retain the captured UUID
  where durable capture succeeded, remain pending/unknown, no route or enable.
- Cancellation is not a remote fence. An accepted delayed POST can still commit
  after its caller has ended; a second mutation remains blocked across restart.
- A repository GET or a similar-looking listed pool never reconstructs a lost
  POST acknowledgement, proves completion, or clears the unresolved barrier.
- Journal identity/missing-mount guards and non-activatable inspection backups
  remain in force. Same-lineage raw rollback and external writer fencing remain
  explicit limitations. There is no reset/delete/automatic recovery endpoint.

## Required next core contract, not implemented here

The proposed versioned `RegisterInactivePoolRoute` handshake must look up the
exact confirmed record in the independently bound journal itself. A caller's
`StagedPool` JSON is **not authority**. It must match a fresh exact remote
observation and trusted execution-template/host-group/source-actor-group revisions,
then atomically insert immutable route identity, bounded ledger pool policy,
closure of readiness and audit under a policy revision/barrier. Only already
bound hosts and fixed trusted source/transport references may participate.
No handler may mutate the facade's maps or accept arbitrary endpoints/keys/scripts.

Route registration starts inactive and is idempotent only for an exact matching
intent/UUID. Current active state/revision must be checked atomically at final
admission/claim; old execution plans and source receipts must never be
reinterpreted. Restart/backup must retain route identity and fail closed on
missing/mismatched trusted references, not discard rows or bootstrap new state.

Only a separately qualified explicit activation coordinator may enable remotely,
journal the known acknowledgement, obtain fresh exact readback, and then activate
locally. Unknown remote enable always leaves the local route inactive. Deactivation
must make the local route inactive first, then request remote disable; it cannot
certify job-safe drain or release resources. External alternative writers and the
entire affected routing graph still need independent qualification.

Until that core ACK and activation protocol land, there is no practical dynamic
pool creation/activation UI. Pool deletion, runner drain, minimum-idle decreases,
physical host enrollment and restore activation remain unavailable.

## Offline qualification

Authenticated disposable TLS client tests cover fixed request fields, independent
default-off scope, exact receipt/readback, aliases and malformed responses, and no
scope expansion or retries. Actual journal/TLS tests cover intent-before-POST,
UUID-before-GET, confirmed replay, altered reuse, ambiguous ACKs/readback,
restart/global blocking, failed receipt audit, delayed accepted POST after caller
cancellation, and write-blocked inspection backups. Pinned stock Runner/SQLite
qualification proves max-zero/native-zero rejection and the absence of a provider
validation call; it is not proof of installed source8 support or live routing.
