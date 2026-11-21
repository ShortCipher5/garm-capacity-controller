# Stock GARM v0.2.1 behavior when an external provider cannot create a runner

Status: read-only source investigation. No live system was contacted.

Subject: upstream `cloudbase/garm` at commit `154638445c3949c1958b01812f69d9a1e4d82684`
("Update all dependencies", 2026-05-08). Every "VERIFIED" claim below was read from
that exact commit via `git show 15463844:<path>` on a checkout that carries private
changes on top; the working tree was not trusted. Line numbers refer to the pinned
blobs. "INFERENCE" marks conclusions derived by combining verified code paths, not
directly read statements.

## (a) How stock GARM invokes external providers — VERIFIED

- Version selection: `runner/providers/external/external.go:29-35` switches on
  `cfg.External.InterfaceVersion`: `"v0.1.0"` or empty selects `runner/providers/v0.1.0`,
  `"v0.1.1"` selects `runner/providers/v0.1.1`; anything else is rejected. Constants in
  `vendor/github.com/cloudbase/garm-provider-common/execution/common/versions.go:8-13`.
- Command protocol: the binary is exec'd with env `GARM_COMMAND` set to one of
  `CreateInstance, DeleteInstance, GetInstance, ListInstances, StartInstance,
  StopInstance, RemoveAllInstances, GetVersion`, plus the v0.1.1-only
  `GetSupportedInterfaceVersions, ValidatePoolInfo, GetConfigJSONSchema,
  GetExtraSpecsJSONSchema` (`.../execution/common/commands.go:22-40`). Other env vars:
  `GARM_INTERFACE_VERSION`, `GARM_CONTROLLER_ID`, `GARM_POOL_ID`,
  `GARM_PROVIDER_CONFIG_FILE`, `GARM_POOL_EXTRASPECS` (base64), `GARM_INSTANCE_ID` for
  instance-scoped ops. `CreateInstance` receives the marshalled
  `params.BootstrapInstance` JSON on stdin (`runner/providers/v0.1.1/external.go:73-98`).
- Go interface: `common.Provider` in `runner/common/provider.go:25-39`;
  `CreateInstance(ctx, commonParams.BootstrapInstance, CreateInstanceParams)
  (commonParams.ProviderInstance, error)` where `commonParams` is
  `github.com/cloudbase/garm-provider-common/params` (vendored module
  `garm-provider-common v0.1.9`, pinned `go.mod`). A successful create must print a
  valid `ProviderInstance` JSON on stdout (`external.go:108-116`).

## (b) CreateInstance failure handling — VERIFIED

`vendor/github.com/cloudbase/garm-provider-common/util/exec/exec.go:25-39` runs the
binary and, on any error, returns:

> `provider binary failed with stdout: %s; stderr: %s`

wrapped around the raw `c.Run()` error. `runner/providers/v0.1.1/external.go:100-107`
then does:

```go
out, err := garmExec.Exec(ctx, e.execPath, asJs, asEnv)
if err != nil {
    metrics.InstanceOperationFailedCount.WithLabelValues("CreateInstance", e.cfg.Name).Inc()
    return commonParams.ProviderInstance{}, garmErrors.NewProviderError("provider binary %s returned error: %s", e.execPath, err)
}
```

- The exit code is not inspected. Exit code 1 and exit code 75 take the identical path:
  a single opaque `ProviderError`, the stdout JSON is discarded, stderr is only embedded
  as text in the message. The v0.1.0 provider is the same (`runner/providers/v0.1.0/external.go`,
  same function shape).
- The only exit-code branch in either file is in `DeleteInstance`
  (`v0.1.1/external.go:158-159`), which tolerates `ExitCodeNotFound`.
- Defined exit codes are only `ExitCodeNotFound = 30` and `ExitCodeDuplicate = 31`
  (`.../execution/common/commands.go:49-56`), with a provider-side helper
  `ResolveErrorToExitCode` mapping `ErrNotFound`/`ErrDuplicateEntity`
  (`commands.go:89-98`). Upstream docs say only: "In case of error, `garm` expects at
  the very least to see a non-zero exit code." (`doc/external_provider.md:241`).

## (c) Pool behavior after a failed create — VERIFIED unless noted

- The instance record lingers. `addPendingInstances` transitions it to `creating`,
  and on provider error sets status `error` with the error text stored as
  `provider_fault` (`runner/pool/pool.go:1709-1770`, status set at `:1762`). A
  best-effort `DeleteInstance` of the partial instance is attempted via a deferred
  cleanup in `addInstanceToProvider` (`pool.go:1109-1136`).
- The retry loop is scheduled every 5 seconds, and the worker skips rows with
  `CreateAttempt >= 5` (`runner/common/pool.go:26`; `runner/pool/pool.go:70,1417-1422,1893`).
  Those are not a recovery schedule. After provider cleanup succeeds, the worker
  calls ordinary `UpdateInstance` to set `pending_create` and increment the counter
  (`pool.go:1444-1479`). The SQL store validates before applying or saving the
  update (`database/sql/instances.go:414-434`); the pinned transition table does
  not allow `error → pending_create` (`params/params.go:193-198`).
  **Inference:** a row still in `error` can undergo repeated cleanup attempts
  without another create or an incremented counter. Cleanup failure also stops
  that retry before the update. Neither five creates in about 30 seconds nor
  recovery as soon as capacity returns is guaranteed.
- The failed-create retry worker does not use the geometric backoff that applies
  to the separate pending-deletion worker (`pool.go:1625-1669`). Its repeated
  work on this path can be cleanup rather than repeated creation.
- A retained row is not necessarily permanent. The separate timeout reaper
  examines ordinary pool rows whose update age exceeds the pool timeout. If the
  managed runner is absent from GitHub's runner listing or is offline, and the
  lock, pool lookup and deletion checks succeed, it can mark the row pending
  deletion (`pool.go:652-710,1916-1957`). That is not itself provider removal or job assignment.
- While retained, the row counts toward the pool maximum: the idle-fill check
  uses the total number of pool rows regardless of status. It also counts toward
  the idle floor if its separate `RunnerStatus` is neither active nor terminated
  (`pool.go:1346-1386`).
- Upstream [#793](https://github.com/cloudbase/garm/issues/793) reports the
  transition error in v0.2.1. [#795](https://github.com/cloudbase/garm/pull/795)
  fixes the retry write on upstream main. That doesn't change the pinned v0.2.1
  behavior or add a special capacity-deferral signal.

## (d) Any stock "temporarily out of capacity, retry later" signal? — VERIFIED: none

No. Exhaustive check of the pinned tree: the only distinguished exit codes are 30/31
(see (b)); the `garm-provider-common/errors` package defines `Unauthorized,
NotFound, DuplicateEntity, BadRequest, Timeout, Unprocessable, NoPoolsAvailable,
ProviderError, MissingSecretError` (`vendor/.../errors/errors.go`) — no deferral,
quota, or retry-later kind; grep for tempfail/deferral/no-capacity semantics over
`runner/`, `doc/`, and the vendored provider-common finds nothing. The
`InstanceError` status returned in an exit-0 JSON (`external.go` ValidateResult path,
`pool.go:1137-1143`) is still just an error outcome with the same retry loop.

## (e) Fate of the queued GitHub job when creation keeps failing — VERIFIED

`consumeQueuedJobs` locks the local job record, then tries matching pools.
A synchronous `addRunnerToPool` error can send this pass to the next pool.
But a successful `AddRunner` only establishes a durable `pending_create` row;
the loop marks `runnerCreated` and stops trying pools
(`runner/pool/pool.go:936-997,2152-2199`). A later asynchronous provider
refusal is not fed back into that pool selection pass and does not cancel
the GitHub job.

After the local queued record reaches the ten-minute age check, GARM can
unlock its own record for reconsideration on a later pass
(`pool.go:2092-2134`). That does not guarantee a fresh runner; eligibility and
pool limits still apply. The local record can also be cleaned up as stale or
inactionable (`pool.go:1304-1318,1966-2078`). Neither local record cleanup nor
a successfully inserted runner row proves GitHub assigned that job.

## (f) What stock GARM does with our exit-75 signal — VERIFIED + INFERENCE

Two layers, both verified:

1. Stock GARM never sends `GARM_COMMAND=AdmitInstance`. The command set is fixed
   (see (a)); `AdmitInstance`/`CancelAdmission`/`RecordPreparation`/
   `ValidatePreparation` exist only in our binary (`cmd/capacity-provider/main.go:54`)
   and our maintained patch. Under stock GARM the admission/defer contract is inert.
2. Any non-zero exit from `CreateInstance` — including 75 — is the ordinary failure
   of (b)/(c). Concretely, if `cmd/capacity-provider` is pointed at stock GARM and the
   gateway is short of capacity, `client.Create` errors and the binary exits 1
   (`cmd/capacity-provider/main.go:144-156`); even a hypothetical exit 75 lands in the
   same `NewProviderError` path. What breaks (INFERENCE from verified paths):
   - no deferral: the failed row can stay in `error` despite successful
     cleanup and restored capacity; the separate timeout reaper may later
     move it toward deletion (see (c));
   - the initial failed create can cause best-effort `DeleteInstance` cleanup
     (`pool.go:1109-1125`), and retry ticks can attempt cleanup again without
     issuing another create;
   - audit/log noise: `InstanceOperationFailedCount` incremented per attempt
     (`external.go:102-106`), error text persisted as `provider_fault`
     (`pool.go:1762`);
   - the retained row occupies a slot under `max_runners` and can suppress
     backfill under `min_idle_runners` (see (c));
   - the queued job stalls per (e) — the failed attempts are invisible to job handling.

## (g) Smallest upstreamable change (PROPOSAL — not implemented)

Gap confirmed: stock external providers have no way to say "back off, this is
transient." Minimal shape, reusing existing machinery:

1. `garm-provider-common/execution/common/commands.go` (next to `ExitCodeNotFound`):
   add `ExitCodeDeferral int = 75` and map a new `errors.ErrDeferral` sentinel through
   `ResolveErrorToExitCode`. (Module bump of provider-common.)
2. `runner/providers/v0.1.1/external.go:100-107` (and the v0.1.0 twin): on exec
   failure, copy the existing `DeleteInstance` pattern (`external.go:158-159`) —
   `errors.As(err, *exec.ExitError)` with `ExitCode() == ExitCodeDeferral` → return a
   typed error wrapping `ErrDeferral` instead of plain `ProviderError`.
3. `runner/pool/pool.go` `retryFailedInstancesForOnePool` (`:1410-1487`): consult the
   already-existing `r.backoff.ShouldProcess(instance.Name)` (pattern at
   `:1625-1630`) before requeuing, and `RecordFailure` when the failure was a
   deferral. The geometric 1.5x/20-min backoff then spaces out deferral retries with
   no new backoff code.

That is roughly three small hunks plus a vendored-constant bump. This documents a
proposal only; nothing has been sent to or accepted by upstream.

## Implications for this controller

- `internal/garmprotocol/protocol.go:16` (`DeferredExitCode = 75`) and `:80-83`
  (deferred = exit 75, no reservation key, `retry_after_seconds` 5–60) define a
  contract that stock GARM v0.2.1 cannot honor: at the pinned commit the code has no
  deferral semantics (see (d)).
- The exit-75 outcome is produced by the `AdmitInstance` command
  (`cmd/capacity-provider/main.go:114-126`), which stock GARM never invokes; the
  stock-facing `CreateInstance` command maps gateway failure to a silent exit 1
  (`main.go:149-152`), i.e. ordinary failure under stock rules (see (f)).
- Our own docs already state the dependency: "Garm v0.2.1's small maintained pre-JIT
  compatibility patch is still required for benign defer semantics"
  (`docs/provider-facade.md:33`). The staged patch
  (`integration/garm-v0.2.1/admission-preflight.patch`) adds exactly the typed
  deferral stock lacks: `AdmissionDeferredExitCode = 75` (patch line 431),
  `NewAdmissionDeferred`/`IsAdmissionDeferred`/`AdmissionRetryAfter` (lines 542-553),
  a pre-JIT `AdmitInstance` call in `AddRunner`, and a source-hook opt-out of
  `retryFailedInstances` for preparation-enabled providers (pool.go hunk at patch
  line 1785). This investigation confirms the patch is load-bearing and matches the
  stock gap it fills; against unpatched GARM the controller's deferral signal is
  inert and a provider refusal can leave a charged row that does not requeue
  through the retry worker.
- Upstream path: if the proposal in (g) were ever accepted, the maintained patch
  could shrink toward config and the admission call site only. Until then, stock-behavior
  claims in this repo must continue to describe (b)-(f) above.

## What the existing full-capacity qualification must demonstrate

For an independently authorized qualification of ordinary pools at the host
ceiling, keep the same GitHub repository, run, attempt and job identity from
capacity refusal through restored headroom. Record provider refusal and cleanup,
GARM row state and attempt counter, and GitHub runner assignment separately. A
passing recovery observation requires that the exact job starts within the
predeclared observation window; otherwise record recovery as unproven. Restored
headroom, a disappeared row or a different job starting is not a pass. Record the
exact GARM build and pool timeout alongside the observations. A disposable
offline retry regression must exercise the real SQL state checks or a test store
that rejects the same transition; a permissive fake can hide this failure.

## Evidence notes

- integration/garm-v0.2.1/ in this repo is a staged patch + test harness, not vendored
  source (see its README); stock behavior was verified from the pinned commit in the
  garm checkout as described above.
- Nothing here accesses live GARM, Incus, or GitHub infrastructure; no credentials.
