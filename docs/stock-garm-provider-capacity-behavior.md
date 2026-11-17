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
- Retry loop: `retryFailedInstances` runs every `PoolConsilitationInterval = 5s`
  (`runner/common/pool.go:26`; loop registered at `pool.go:1893`) and requeues each
  `error` instance to `pending_create` with `CreateAttempt+1`
  (`pool.go:1417-1479`), capped at `maxCreateAttempts = 5` (`pool.go:70`, skip at
  `:1420`). So a failed create is retried at roughly 5-second cadence and gives up
  after ~5 attempts (about 30 s — INFERENCE, timing depends on exec latency).
- No create-side backoff. The geometric 1.5x backoff (start 5 s, cap 20 min,
  `locking/local_backoff_locker.go:60-76`, `maxBackoffSeconds = 1200` in
  `locking/local_locker.go:27`) is applied only to DELETE paths
  (`deletePendingInstances`, `pool.go:1625-1669`). Create retries have no backoff at
  all — a persistent provider failure is a tight retry storm. VERIFIED.
- After the 5th failure the record stays in `error` forever (nothing in the pinned
  tree reaps `error`-status instances that never reached GitHub; the timeout reaper at
  `pool.go:652-716` only handles runners GitHub already knows). INFERENCE for
  "forever": verified that `retryFailedInstancesForOnePool` skips them and no other
  loop transitions them.
- Desired count: `ensureIdleRunnersForOnePool` (`pool.go:1346-1398`) counts any
  instance whose `RunnerStatus` is not `active`/`terminated` — which includes
  `error`-status records — toward `min_idle_runners`, so a lingering failed record also
  suppresses backfill. VERIFIED by code reading; effect INFERENCE.

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

`consumeQueuedJobs` (`runner/pool/pool.go:2073+`) locks the job
(`store.LockJob`) and calls `addRunnerToPool` → `AddRunner`, which only writes the
durable `pending_create` row (`pool.go:936-997`). Once that row exists,
`runnerCreated = true` (`pool.go:2185`) and the job is considered handled — the
asynchronous provider failure does not requeue or fail anything job-related. The job
row then: is retried by creating *another* runner only after it sits queued for
10 minutes (`pool.go:2110-2120`); is deleted if a scale-down decides it is stale
(>10 min, `pool.go:1304-1318`); is deleted once GitHub reports it no longer queued,
checked at most hourly for jobs older than 1 h (`reconcileStaleJobs`,
`pool.go:1966-2071`); and otherwise lingers until webhook events remove it via
`DeleteInactionableJobs` (5-min grace, `pool.go:2078`). On the GitHub side the job
simply stays queued until GitHub itself times it out — outside this repo, INFERENCE.

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
   - no deferral: ~5 create attempts at ~5 s cadence, then a permanent `error` record;
   - the storm hits the gateway/Incus once per attempt, plus one best-effort
     `DeleteInstance` cleanup per attempt (`pool.go:1109-1125`);
   - audit/log noise: `InstanceOperationFailedCount` incremented per attempt
     (`external.go:102-106`), error text persisted as `provider_fault`
     (`pool.go:1762`);
   - the lingering `error` record counts toward `min_idle_runners`, suppressing
     backfill (see (c));
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
  inert and capacity exhaustion degrades to a ~30 s, 5-attempt failure cycle with a
  lingering charged record.
- Upstream path: if the proposal in (g) were ever accepted, the maintained patch
  could shrink toward config and the admission call site only. Until then, stock-behavior
  claims in this repo must continue to describe (b)-(f) above.

## Evidence notes

- integration/garm-v0.2.1/ in this repo is a staged patch + test harness, not vendored
  source (see its README); stock behavior was verified from the pinned commit in the
  garm checkout as described above.
- Nothing here accesses live GARM, Incus, or GitHub infrastructure; no credentials.
