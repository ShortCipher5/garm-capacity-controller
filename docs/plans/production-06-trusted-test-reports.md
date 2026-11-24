# Trusted test report ingestion and test insights

**Plan 06 · Draft engineering handoff · Code and offline acceptance only**

Starting reference: [`65b7a33`](https://github.com/ShortCipher5/garm-capacity-controller/commit/65b7a33a1b0f00bc2c839e33c669b229d6660d4b). Recheck current `main` before implementing.

## Outcome
Add actionable test-level history separate from workflow timing: recurring failures, consistently passing cases, slow cases and verified retry outcomes. Start opt-in, and show unqualified or missing coverage rather than conflating it with success.

## Start here
Reuse `internal/demandrepair/ci_artifact_metadata.go` and `internal/cihealthreport/{reviewed_zip,junit,attempt_set}.go`. Current code has run-level artifact metadata, bounded parsing and attempt-set/cohort helpers; a run-scoped artifact name or metadata row does not prove exact job, shard, framework retry or source.

## Implement
1. Specify an authenticated, opt-in report provenance contract linking source/repository/workflow/run/attempt/job and producer/shard/retry to bounded artifact bytes. Distinguish proved job-level reports from run-only metadata. Use only existing authorized read scope where possible; otherwise fail closed and document exact capability. Do not request secrets, new repo access or workflow changes on customer repos in this ticket.
2. Implement a fixture-backed ingestion boundary to validate the exact manifest/metadata and digest where published against the archive/extracted members, enforce byte/count/structure ceilings via existing parsers, and reject mismatched scopes, duplicates, unsafe members and invalid content. No persistence of tokenized URLs, logs, command or environment output, or raw archive bodies in user-facing data.
3. Normalize the bounded case ID/class/file where authorized, outcome, duration, occurrence and verified shard/retry. Store idempotently with provenance and retention linked to Plan 05, and distinguish failed first execution from eventual success. Do not infer expected shards from names or treat missing shards as passed/failed.
4. Render sample count, coverage, timing/failure rates and exact source links; keep comparisons within verified comparable framework/cohort boundaries. Omit or explicitly label unverifiable results. No AI analysis, deployment or speculative pass/fail guessing.

## Code acceptance
- Exact authorized fixture proves one report with duplicate/retry/shard distinctions; missing producer proof, digest mismatch, oversized/invalid archive, revoked scope and cross-attempt reuse do not publish test-level results.
- Rankings and counts are deterministic and don't merge same display names across incompatible cohorts; partial report coverage is visible. A run-only artifact can remain metadata without masquerading as job-level evidence.
- If current API and workflow cannot establish provenance, complete the offline contract and unavailable behavior; do not claim real test insights yet.

## Dependency boundary
Can be coded with sanitized generated fixtures. Live ingestion depends on a supported already-authorized archive reader and independent producer binding; any approval/workflow enrollment needed to establish those is outside this ticket.

## Handoff and definition of done
This draft contains the plan only. Take over this branch or split implementation into small reviewable commits/PRs referencing it. Identify the exact baseline and behavior already present, ship only the delta, and include offline automated regressions for the acceptance cases. Keep flags off where capabilities are unavailable, document rollback of the code change, and report outstanding dependencies explicitly. No live server changes, new access grants, production rollout, or operational rehearsal are assigned. See [original scope](https://github.com/ShortCipher5/garm-capacity-controller/issues/53) and [CI Health scope](https://github.com/ShortCipher5/garm-capacity-controller/issues/268); this plan and current stock-first code take precedence over older experimental notes.
