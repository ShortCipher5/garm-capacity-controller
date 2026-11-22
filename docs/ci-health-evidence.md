# CI Health evidence and job attribution

This matrix describes what the repository can report, not what is deployed or
qualified on a live host. The linked tests use disposable fixtures.
[CI Health timing and history](ci-health-timing.md) explains the existing
owner-only, opt-in view and retention limits.

| Evidence | Supported observation | Limits and fixture |
|---|---|---|
| GitHub Actions run, attempt, job and step timing | **Opt-in gateway feature.** Reads the existing authorized repository scope and binds exact repository, workflow, run, attempt and job IDs. Reports explicit source phases and conclusions, and elapsed times only where the necessary timestamps are observed. | Listings are bounded and non-atomic. A rerun's creation time is not its execution start. A missing queue transition, timestamp, row or conclusion stays unknown; missing or removed runs do not prove coverage or outcome. See the [scoped reader](../internal/demandrepair/ci_health.go), [pagination and identity tests](../internal/demandrepair/ci_health_test.go), and [gateway-to-owner fixture](../cmd/capacity-gateway/ci_health_runtime_fixture_test.go). |
| Assigned GitHub runner | **Scoped read primitive.** A positive, exact GitHub job receipt can report the assigned numeric registration ID, exact job/run/attempt, and an evidenced execution start or terminal result. The JIT comparison additionally requires an independently obtained matching registration observation. | A runner name, label or ID alone does not establish an Incus incarnation, stock pool placement or current authority. An unassigned or missing job gives no receipt. See the [lifecycle reader](../internal/demandrepair/job_lifecycle.go) and [exact scope and identity fixture](../internal/demandrepair/job_lifecycle_test.go). |
| Stock GARM queue and inventory | **Scoped read primitive.** A bounded stock snapshot can report queue demand, eligible pools, runner rows and supply in that observation. | An eligible pool is not a dispatch record. Stock snapshots do not reconstruct historical job-to-runner execution or prove a runner was deleted; an unmatched or missing row cannot free capacity. See the [stock demand bridge](stock-demand-allocation-bridge.md) and [supply-without-binding fixture](../internal/garmapi/demand_test.go). |
| Incus CPU and memory | **Scoped read primitive.** For an independently selected existing container on a pinned standalone Incus 7.0.2 daemon, the named state read can report cumulative CPU time, current memory and an optional cgroup-lifetime memory peak. | Bracketing checks detect visible drift, not every replacement or reset between GETs. Values can include non-CI work; the cgroup high-water mark is not a job-window peak. This read supplies no disk counters or measurements for the host, an external builder or another target. See [Incus runtime health](incus-runner-runtime-health.md) and its [pin, drift and counter fixtures](../internal/incusapi/runner_runtime_health_test.go). |
| Per-job resources and sizing | **Offline model only.** Exact job/runner identity, matching initial and closing bindings and separate execution evidence must cover the sampled job window. CPU averages and interval peaks, RAM gauges and time-weighted mean, and optional disk metrics describe only measured coverage. | The reducer refuses resets and wrong incarnations and never fills cadence gaps or missing fields. Disk requires its own independently bound samples. The sizing model also requires reviewed comparable history and risk evidence; it does not authorize changes. See the [stock binding gate](../internal/cihealth/stock_job_resource_binding.go), [replacement and cutoff fixtures](../internal/cihealth/stock_job_resource_binding_test.go), [cadence fixtures](../internal/cihealth/resources_test.go), and [sizing fixtures](../internal/cihealth/job_sizing_test.go). |
| Test reports and artifacts | **Offline JUnit parser; separate metadata reader.** A bounded JUnit report, independently bound to an exact artifact and attempt, can yield occurrence-level outcomes, counts and durations. The scoped GitHub reader returns run-level artifact metadata, not the archive. | An artifact name or its XML cannot prove attempt, job, shard, retry or reporter identity. The parser does not retain labels or report bodies, infer tests outside the report, or fetch artifacts. See the [metadata fixture](../internal/demandrepair/ci_artifact_metadata_test.go) and [JUnit binding, privacy and limits fixtures](../internal/cihealthreport/junit_test.go). |

The offline sizing window consumer bounds the review to at most 30 days and
requires a recent primary completion before returning advice. The caller
supplies the review clock and maximum age. An out-of-window observation
invalidates the entire review rather than being silently dropped from a
supposedly complete cohort. Retries do not refresh primary evidence. This
consumer has no gateway or UI wiring and does not verify the data source.

## What remains unavailable

- **Verified stock job-to-resource attribution.** There is no live producer for
  the required initial stock binding, the matching end-of-window evidence and
  a qualified counter series covering that exact job on that incarnation.
  An assigned GitHub runner, a GARM row and an Incus sample are each useful
  evidence; their separate existence does not prove the join.
- **Live per-test results.** The timing view does not obtain authorized report
  bytes or prove their producing job, attempt and reporter. Artifact metadata
  and an offline parser do not close that gap.
- **Resource recommendations in the dashboard.** The offline sizing model
  consumes separately reviewed observations. Missing scope, coverage or risk
  evidence stays unknown; no live sizing, host budget, runner control or
  allocation authority follows from CI Health.
