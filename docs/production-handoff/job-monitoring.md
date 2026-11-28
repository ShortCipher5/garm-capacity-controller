# Live per-job runner resource monitoring

**Planning PR only.** Baseline: `fb8c39c26b865cc277783d8899af32dd63e6a836`. Tracks [#268](https://github.com/ShortCipher5/garm-capacity-controller/issues/268). This work runs separately from controlled autoscaling. It must not confer capacity, dispatch, pool-write, teardown, or admission authority. Basic autoscaling does not depend on it.

## Outcome and existing starting point

On the existing CI Health job view, distinguish **unavailable**, **stale**, **partial**, and **verified** usage for the exact GitHub attempt/job. Show the source observation time, sample count, actual observed coverage, and explicit omissions. Timing/history and GitHub navigation must continue working even when resource telemetry cannot be attributed. Never present requested limits, host consumption, or inferred values as usage.

- `internal/demandrepair/job_lifecycle.go` and `internal/cihealthsource/job_link_linux.go` are the existing exact-job and navigation seams. Use the `JobKey`/ `RecordRef` identity from `internal/cihealth/records.go`, not display names; preserve numeric identities losslessly on the web.
- `internal/cihealth/attribution_capability.go`, `attribution_claim.go`, and `stock_job_resource_binding.go` define the support matrix, independent proof checks, and sample brackets. `verified_usage.go` and `resources.go` already reduce/project observed coverage and omit unsupported evidence. The documented stock matrix must remain fail-closed: a live provider-origin source is not established by this code.
- `internal/incusapi/runner_runtime_health.go` is a scoped instance CPU/RAM read primitive, not an allocation or exact job receipt; `docs/ci-health-evidence.md` documents limitations. Existing timing snapshots and exact scoped reads are in `internal/cihealth/history_snapshot_linux.go` and `internal/cihealthsource/history_view_linux.go`. Neither supplies a durable independent usage attachment. Offline reports and advisory sizing are distinct consumers; leave them unwired to new records in this scope.

## Implementation sequence

### A. Fix the offline disk projection first

The reducer already derives read and write counter deltas independently and tracks used-space as a gauge. The current wire projects only read coverage onto a shared pair, and the CI Health component does not show the gauge-only peak. Change the Go/browser wire and display together:

- Represent each available read/write total with its own byte count, covered nanoseconds, and completeness flag. Coverage must be positive, no longer than the exact job window, and complete only at full coverage. The gauge is a separate sampled peak in bytes, count of gauge samples, and last sample timestamp (or an explicitly tracked peak timestamp, not `LatestAt` relabeled as peak time). A gauge never implies interval coverage.
- Preserve zero only when measured. Missing metrics have no value; a partial counter total is not a whole-job estimate. Validate bounds, lossless decimal strings and zero with the same units on both sides. Host free disk, filesystem quota, I/O totals and occupied bytes are distinct quantities.
- In `web/src/ci-health-usage.js`, account for the existing exact-key decoder. Prefer an explicitly versioned measured disk shape and an intentional migration/rollback compatibility branch; if decoding old mixed results, never assign historical read coverage to writes. An incompatible display may fail closed to unavailable; do not rewrite history to manufacture compatibility. Update `web/src/ci-health.jsx` to present each supported counter and gauge with its own semantics.
- In `verified_usage_test.go`, `web/src/ci-health-usage.test.js` and the CI Health render/browser fixtures, cover read only, write only, differing windows, gauge only, true zero, absent/unsupported values, invalid bounds and old payload handling. The stock unsupported fixture must still produce no attributed disk numbers.

### B. Qualify the source before implementing live ingestion

Write the existing-source support/limitations matrix, using verified observations from an authorized isolated runner. Document each input's exact installed version, authenticated scope, unit and freshness rules. Only a positively proved chain is eligible:

1. Exact enrolled GitHub source, immutable repository/workflow/run IDs, attempt and job ID, actual execution window and positive numeric runner registration.
2. Authenticated scoped stock GARM row for that same registered runner and enrolled repository/pool.
3. Positive, authenticated proof of the **original** provider allocation linked to that row and the exact Incus daemon/project/instance incarnation. Current name, mutable provider ID, tags, matching times, a successful independent GET, or absence of competing candidates cannot establish this link.
4. Same immutable incarnation and independently evidenced job-window brackets for the counters, bounded sample cadence and real supporting observation time. Reject reused instance identity, resets, ambiguous concurrent jobs, foreign scopes, stale or missing brackets; do not attribute an uncovered interval or a cgroup-lifetime peak to the job.

Use approved, narrowly scoped existing read interfaces. No general SSH, new privileged collector, broader app permissions, runner bootstrap, proxy/fork, or dispatch/ledger integration in this plan. A caller-constructible usage record, fabricated allocation receipt, or supplied capability flag cannot authorize measurements. If exact origin or any counter cannot be obtained from a qualified source, explicitly report unavailable for the unsupported attribution or metric; proceed with offline display/fixtures without enabling a fake fallback. Any new source or permissions would require a separate design and approval.

### C. Add a bounded private attachment only after the input contract is stable

Use a validated internal attachment factory at the qualified collection boundary. It should produce only the approved display projection for an exact `JobKey`, accepted evidence time, measured window and coverage; do not store source credentials, raw samples, host identities in user responses, job output, logs, commands or environment variables. Keep provider evidence private and retain only the minimized provenance necessary to revalidate access and the exact binding. Preserve the distinction between capture/observation time and request/render time.

- Design an independent opt-in sidecar so stock v1 timing history remains rollback-compatible and opt-in v2 timing snapshots are unchanged. Reuse the reviewed directory and restart-safety patterns, not the timing file format itself. Bounds must cap bytes, records, lookback and per-cycle work; document defaults and enforce them before allocation. Never use unverified data as an input to sizing or enforcement.
- Require an actual scoped retained-history proof/cursor and check exact current job membership on reads, not merely a nonnegative revision or matching job ID. Authorize enrollment before storage lookup, cache, response, navigation and aggregation; revocation immediately prevents a historical attachment from being shown. History/cursor drift must not resurrect a stale job or contaminate sibling attempts, repos or source instances.
- Require canonical idempotent replay for the same observation, reject conflicting payloads or mismatched key/window/lineage, and never convert old evidence into a fresh sample. After restart, uncertain or corrupt records fail closed; test bounded pruning, retention cutoffs, cancellation, and downgrade without touching existing timing data.
- Add only a feature-gated, read-only projection to the existing scoped CI Health surface. Show peak/coverage/sample semantics and explicit reasons; never expose host/runner secrets or broaden gateway permissions. Keep the feature off unless qualified. Disable the projection without invalidating timing history or the ability to roll back.

## Acceptance and release gate

- Offline: exact-identity negatives (foreign repository, source, fork, sibling attempt/job, reused runner, changed generation, overlapping windows), wrong units/reset/drift, partial coverage and job-too-short, enrollment revocation, data omission rules, malformed wire rejection, restart/dedupe/conflict/retention, and baseline behavior with the option off. Run focused Go race/vet, web unit and browser coverage; verify the actual merge candidate.
- Authorized isolated qualification, not an action requested by this PR: on the approved bounded Mumbai test, an exact known job should line up with independently observed original allocation and exact instance/window. Compare only the metrics the installed sources support, including documented gaps; verify clean teardown separately. Do not provoke operational faults, dispatch extra jobs or change host or production access under this plan. If no positive original-allocation contract is demonstrable, do not activate live usage.
- Roll out only in a separately approved scoped cohort where the protected evidence chain and resource limits have been verified. Roll back by turning off the optional live projection/collector, keeping existing GitHub timing usable and preserving records under configured retention. No production-readiness claim from fixtures or a merged plan.

**Reviewer checklist:** scope and exact source established; stock unsupported remains explicit; disk and host metric semantics separated; stored data minimized and bounded; authorization enforced before joins; old timing history compatible; exact merger CI passes; isolated evidence reviewed before any activation.
