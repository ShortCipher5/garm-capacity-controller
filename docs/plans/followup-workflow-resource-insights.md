# Show workflow resource trends, sizing suggestions and anomalies

Status: draft implementation plan. This document is not an implemented or activated feature.

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
