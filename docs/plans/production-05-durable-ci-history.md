# Durable CI history and exact workflow navigation

**Plan 05 · Draft engineering handoff · Code and offline acceptance only**

Starting reference: [`65b7a33`](https://github.com/ShortCipher5/garm-capacity-controller/commit/65b7a33a1b0f00bc2c839e33c669b229d6660d4b). Recheck current `main` before implementing.

## Outcome
Retain useful GitHub timing and result history per enrolled repository across normal controller restarts without inventing clocks, losing measurements silently, or exposing data from another repository.

## Start here
Read `docs/ci-health-timing.md`. Reuse `internal/cihealth/history_snapshot_linux.go`, the history service/cycle/view under `internal/cihealthsource` and `web/src/ci-health.jsx`. There are bounded snapshots, reviewed reopen and exact live link checks; do not replace these with a second unchecked history store. Runtime restore enablement is not presumed.

## Implement
1. Preserve identities across workflow/job enumeration and retry: source, immutable repository and workflow IDs, run, attempt, job and step number. Keep 64-bit IDs exact in UI/routes. Distinguish current and prior attempts, two identical matrix labels, skipped jobs and genuine zero from missing time. Do not derive queue wait from workflow creation.
2. Extend the existing durable history lifecycle to use atomic cursor and fact commits, bounded pagination/backfill/retention, idempotent replay and explicit limits/watermarks. Handle interrupted cycles and restart without reopening the wrong tenant/source, silently enabling an imported store, or mistaking absent pages for completeness.
3. Add provenance-bearing retention for sparse later observations: keep prior positive measurements as their own version, not as fresh fields in the newer record. Mark source, observation time, freshness and reason for uncertainty. Do not reject an entire workflow or starve unrelated attempts simply because one valid record is sparse. Prevent terminal regression and keep both original-attempt and retry outcomes honest.
4. Render queue/execution/attempt/step durations separately, with coverage and timestamps. Use only verified canonical repo/run/job navigation; links unavailable when proof/authorization is absent. Plan 04 can attach verified usage records to the same keys; Plan 06 adds trusted test-level results independently.

## Code acceptance
- Duplicate/reordered pages and restart around commit preserve one record per exact observation/version and do not advance a cursor ahead of facts.
- A terminal observation with omitted start/end does not destroy the ability to see a previously measured result, refresh its age, or block unrelated jobs; the new row stays visibly incomplete.
- Purging retained data obeys scope and retention. Cross-repo reads, uncertain links and static job labels used as identity are rejected. Local fixtures cover a reopened store, resumable backfill, and late corrections without live GitHub access.

## Dependency boundary
This can start as a repo-only ticket based on existing scoped APIs and fixtures. Deploying or enabling background/restore modes and qualifying real historical coverage are separate operations.

## Handoff and definition of done
This draft contains the plan only. Take over this branch or split implementation into small reviewable commits/PRs referencing it. Identify the exact baseline and behavior already present, ship only the delta, and include offline automated regressions for the acceptance cases. Keep flags off where capabilities are unavailable, document rollback of the code change, and report outstanding dependencies explicitly. No live server changes, new access grants, production rollout, or operational rehearsal are assigned. See [original scope](https://github.com/ShortCipher5/garm-capacity-controller/issues/53) and [CI Health scope](https://github.com/ShortCipher5/garm-capacity-controller/issues/268); this plan and current stock-first code take precedence over older experimental notes.
