# Pinned Garm admission compatibility patch

**Staged source patch and executable test harness. Not deployed. Not a complete controller or gateway.** This applies only to [Garm v0.2.1, commit 154638445c3949c1958b01812f69d9a1e4d82684](https://github.com/cloudbase/garm/tree/154638445c3949c1958b01812f69d9a1e4d82684). The exact installed Incus provider binary remains unqualified. Do not apply this patch to a live controller from these instructions.

## Why a pre-JIT hook, not a provider-error trick

Stock Garm interprets Create errors as failure, attempts cleanup, increments bounded failure attempts during recovery and may delete runner state. Both normal pools and scale sets do this. There is no existing benign capacity-defer wire outcome.

Simply sleeping inside Create is also unsuitable: GitHub JIT configuration has already been allocated, a Garm runner is already Creating, shutdown can kill the provider child, and startup/reaper logic can delete interrupted work. Refreshing a Garm callback JWT does not refresh stored GitHub JIT configuration.

This patch introduces a smaller **optional admission preflight before JIT allocation and runner persistence**. Denial creates no runner row, no GitHub JIT registration and no downstream provider create/delete. Existing queued-job demand / configured desired idle capacity drives a later attempt. A successful reservation binds the generated name that is subsequently persisted and sent to Create.

The existing approximately five-second Garm loops drive retries. The bounded wire retry value is currently advisory; this patch does not promise exact hint honoring, jitter, fairness or a new durable per-attempt queue. A denied generated name is discarded, not accepted as a durable runner request. Scale-set demand recovery remains stock Garm behavior and is not newly certified by this patch.

## Wire contract

External provider configuration must explicitly opt in:

```toml
interface_version = "v0.1.1"
admission_enabled = true
```

This is an addition to the external provider section, not a complete installation configuration. Default/legacy providers are unchanged. Enabling it against an incompatible executable fails closed; do not enable it against the unmodified Incus provider binary.

The executable receives `GARM_COMMAND=AdmitInstance`, trusted controller/pool/config environment and a small JSON request containing only:

```json
{"protocol":"garm-capacity/v1","name":"runner-1","pool_id":"pool-1"}
```

No bootstrap JWT, JIT configuration, registration token or Incus credentials belong in this request or response.

Reserved receipt, exit 0:

```json
{"protocol":"garm-capacity/v1","outcome":"reserved","name":"runner-1","pool_id":"pool-1","dispatched":false,"reservation_key":"reservation-1"}
```

Never-dispatched capacity denial, exit 75:

```json
{"protocol":"garm-capacity/v1","outcome":"deferred","name":"runner-1","pool_id":"pool-1","dispatched":false,"retry_after_seconds":10}
```

Exact version, matching name/pool, explicit `dispatched:false`, bounded 5–60 second hint and matching exit code are mandatory. Unknown, duplicate or case-folded keys, trailing JSON, mismatched identities and responses over 4096 bytes are rejected. Cancellation, transport failure, malformed output or any dispatched/uncertain outcome is not benign defer. Preflight execution is bounded to ten seconds (or the caller deadline, if earlier), plus at most a 250 ms descendant-held pipe drain tail. Output is bounded and drained; stderr and raw malformed stdout are never embedded in admission errors.

For opt-in gateway configuration, Create execution also uses a bounded, redacted boundary and suppresses raw provider-response debug logging, redacts validation errors/provider faults and verifies the returned runner name. Ordinary Create errors retain their failure semantics and **are never turned into defer**. This does not retrofit redaction or safe cleanup into every legacy Garm provider command.

## Preparation-failure cleanup

After successful admission, opted-in ordinary pools and scale sets call optional
`CancelAdmission` when later JIT/runner-row preparation fails. The command carries
the same protocol/name/pool request, without bootstrap credentials. Its response
requires exit zero and exactly these fields (replay may be true on retry):

```json
{"protocol":"garm-capacity/v1","outcome":"cancelled","name":"runner-1","pool_id":"pool-1","dispatched":false,"reservation_key":"reservation-1","replay":false}
```

Cleanup uses a separate bounded context even if the original request was cancelled.
It preserves the original preparation error and logs only a constant unconfirmed
outcome, never raw child output. A lost reply can follow a committed cancellation;
only exact identity retry can inspect the idempotent result. Cleanup does not run
after denied admission, a successfully persisted runner row, or a physical Create
failure. Default/legacy providers retain their behavior.

The gateway, not Garm, proves that no dispatch intent or linked resource exists.
An ambiguous row/claim race serializes against the same transaction. Claim-first
keeps the allocation charged; cancellation-first permanently rejects late Create.
No TTL, provider-error-based release, physical delete or GitHub deregistration is
introduced. See [cancellation constraints](../../docs/undispatched-cancellation.md).

## The preflight is NOT enforcement

A real gateway must still persist dispatch intent and atomically claim the same reservation immediately before contacting Incus. Replayed reservations are not create permits. All lifecycle paths must use that gateway, and Garm/wrapper identities must not retain direct Incus write credentials or sockets. External Incus administrators remain outside that guarantee.

A failed or unconfirmed preparation cleanup is not release proof; refused transactions remain charged, and an already committed tombstone remains permanent despite a lost reply. No TTL or Create-error-based release is introduced. Reconciliation and an explicit confirmed-retirement design are still required. Create uncertainty must remain charged, and a later cleanup request must not blindly destroy a potentially busy runner. Draining still requires handling already-idle runner dispatch, not just blocking this hook.

## Alternatives and maintenance tradeoff

- **Unmodified Garm + rejecting gateway:** can deny provisioning before dispatch and protect accounting, but Garm treats saturation as failed creation/cleanup, not harmless queue backpressure. It can exhaust retry attempts. Not equivalent to the outlined behavior.
- **Wait inside Create:** avoids an immediate error but introduces stale JIT, process cancellation/restart and reaper hazards. Not the durable queue.
- **Periodically change pool maxima:** not atomic enforcement and does not account for existing idle runners. Rejected as the safety boundary.
- **Native Incus quotas/static partitioning:** potentially useful defense in depth and a forkless hard-cap option after version-specific verification, but not a shared dynamic multi-host admission queue. Provider rejection still has Garm failure semantics. No native-quota integration is implemented or claimed here.
- **Upstream admission extension:** preferred long-term path. This small opt-in hook is a candidate for upstreaming. Until accepted into an upstream release, this source patch must be reviewed/rebased for each Garm change. It is not a maintenance-free solution, and no upstream submission or acceptance is claimed.

The choice here is an explicit small compatibility patch plus a separate credential-isolated gateway, rather than an invisible controller fork or a false claim that pool-limit polling enforces a global budget.

## Executable tests

Requires Go 1.26.2+ (the pinned upstream source requirement), CGO and a C compiler. The script refuses an existing directory, fetches only the public pinned commit, applies the patch in a new disposable checkout and runs actual patched packages with mock/loopback fixtures:

```sh
integration/garm-v0.2.1/test.sh /tmp/new-garm-compat-check
```

Tests exercise the executable admission boundary, strict/redacted protocol handling, cancellation and oversized output, normal-pool denial followed by fresh JIT plus one row, and scale-set denial followed by loopback JIT generation plus one row. No live Garm/Incus process is contacted or deployed. CI retains the formatted patch as an artifact for source review. These tests are not installed-stack qualification, end-to-end job execution, physical Incus enforcement, restart recovery or production readiness proof.

## Compiled facade/service qualification

The harness additionally runs the actual pinned external-provider code against the compiled `capacity-provider` binary and actual gateway service, with disposable mutual-TLS Garm/Incus API fixtures. Confirmed and ambiguous create paths verify one physical POST despite replay, valid Get/List, genuine pre-JIT defer, retained unknown operation correlation and no timeout-based capacity release. See [qualification scope and assertions](../../docs/protocol-qualification.md). This is required by the pinned-source CI job; the fast unit suite alone skips the source-dependent gate. It is not installed-daemon or production qualification.
