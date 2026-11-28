# Show workflow resource trends, sizing suggestions and anomalies

Status: implemented and tested as a read-only, optional dashboard consumer. The exact-history producer is not enabled or wired on `main`.

## Scope

Reuse verified per-job handoff (#395), retained exact workflow history (#384), and advisory sizing (#383). Convert attributed observations into useful workflow insights without feeding them to autoscaling by default.

## Requirements

- Wire exact retained job usage into workflow views by immutable IDs, attempts and supported job/profile cohort. Aggregate only comparable work; expose sample count, coverage, provenance and window. Do not sum unrelated or overlapping peaks into invented workflow peaks.
- Build conservative sizing recommendations and baselines for CPU/RAM and supported disk. Show peak/typical values where supported, current vs baseline, direction and magnitude of anomalies, including both jumps and drops.
- Suppress advice/alerts when data is sparse, stale, incomplete, or when workflow revision or profile changes make the cohort incomparable. Retry observations must not inflate counts. Dashboard only; no paging or auto-resizing.

## Acceptance

Fixtures for repeated runs, workflow/profile changes, reruns and matrix jobs, missing versus measured zero, coverage floors, and positive up/down anomalies. Tests verify unsupported usage stays unavailable.

## Estimate and release boundary

4–7 engineer-days once the verified usage record contract is stable. Engineering estimate excludes rollout and access lead time. Build against disposable fixtures; no live infrastructure, GitHub installation, or production mutations are needed for this PR. Keep stock GARM and existing queue/runner ownership. Mark any unimplemented or unsupported path plainly; do not mistake a fixture or plan for active capability.

## Implemented consumer and remaining producer contract

The existing `/admin/api/v1/ci-health/sizing` response gains an optional history section, with no new endpoint, write controls, automatic requests or notifications. A qualified publisher calls `CISizingSnapshot.PublishWorkflowResourceHistory(enrollment, input)`; it must cancel the enrollment on loss of authorization, storage-lineage change, or shutdown. The history expires on the existing display lease. The dashboard compares one job's demonstrated CPU mean/interval peak, time-weighted RAM mean/sample peak and, only if independently supported and adequately sampled, occupied-disk peak. Up to 12 recent exact completions (including retries, separately labeled) can be inspected before enough baseline runs exist. No summed workflow peaks or smaller-runner advice are produced.

Qualification requires a bounded enumeration (at most 1,000) with the source, repository, workflow, SHA-256 definition digest, stable job definition, matrix leg, runner profile and profile revision positively associated with each exact run/attempt/job. Supply \#395's verified resource record, independent completion, outcome, risk status and installed shape; never construct usage from names, host headroom or allocation limits. Globally missing or withdrawn evidence prevents comparison; a dimension missing in any primary job cannot generate that dimension's signal. The latest successful primary is compared against at least 20 prior primary runs spanning seven days (not retries), using a median, a 10th–90th percentile band, a minimum 25% change, and a floor of 50 millicores or 64 MiB. The current completion must satisfy the configured freshness rule and be no older than 24 hours. A drop is descriptive, not a recommendation to shrink.

**Still required for live use:** the qualified runtime/history owner must publish the complete enumerable cohort above, not a selection of healthy jobs, and supply a cancellable enrollment tied to the actual source, capability and retained history generations. The existing single-job wire display is not such a cohort: it does not prove a whole workflow definition, job identity across runs, comparable runner profiles, risk coverage or membership completeness. Keep the publisher detached where those facts are unavailable. This PR does not fabricate a stock-GARM allocation join or change production.
