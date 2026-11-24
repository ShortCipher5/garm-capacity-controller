# Trusted test report provenance and ingestion

This document specifies the opt-in contract for turning bounded JUnit report
artifacts into trusted, test-level history. It is the Plan 06 companion to
[CI Health timing](ci-health-timing.md) and the [evidence matrix](ci-health-evidence.md).
The code is offline-only: it ships the contract, the ingestion boundary,
idempotent retention and deterministic rendering, with fixture regressions.
No live capability is enabled here.

## What current authorized scope can prove

The existing CI Health reader (`internal/demandrepair`) already holds an
explicit, enrolled, read-only repository scope. From it, two observations are
available today:

- a run-scoped artifact listing (`ListCIRunArtifactMetadata`), where each row
  is metadata only; and
- a short-lived minted capability for one exact repository + run + attempt
  (`ReadCIAttempt`), which gates reads but does not identify producers.

GitHub's run-level artifact metadata carries no `run_attempt`, no job ID and
no producer attestation. An artifact name, its archive path, its XML content
and its creation time cannot prove the exact job, shard, framework retry or
source that produced the bytes. The existing reader already refuses to join
listing rows to attempts or jobs.

**Exact capability gap.** With the existing authorized read scope alone, a
listed artifact can be established only as `ProvenanceRunMetadata` (see
`internal/cihealthreport/provenance.go`). Test-level results require
`ProvenanceJobReport`: an independently reviewed binding of producer job,
shard and retry, plus byte-exact archive and extracted-entry pins. Nothing in
the current API or workflow establishes that binding without new out-of-band
review; this ticket therefore does not claim live test insights, request new
secrets or repo access, or change customer workflows.

## The provenance contract

One artifact enters the trusted pipeline only as a `ReportManifest` row
(`internal/cihealthreport/manifest.go`) that binds all of the following,
reviewed out of band before ingestion:

| Claim | Field | Rule |
|---|---|---|
| source label | `source` | existing configured source atom |
| repository / workflow | `repository_id`, `workflow_id` | positive immutable IDs |
| run / attempt | `run_id`, `attempt` | positive; one manifest is one attempt |
| artifact | `artifact_id`, `archive_bytes`, `archive_sha256` | exact downloaded bytes, always required |
| producer job | `job.job_id` | required for job-level rows; absent rows stay run-only metadata |
| shard / retry | `job.shard`, `job.retry` | reviewer-bound atoms; `retry: 0` is a known initial execution |
| report entry | `entry_name`, `entry_bytes`, `entry_sha256` | exact extracted XML bytes; required exactly when `job` is present |
| case bindings | `cases: [{occurrence, case_id}]` | optional with `job`; reviewer-bound numeric case identity per occurrence |

The contract is opt-in and fail-closed:

- A row without a complete job-level claim is **metadata only**. It is never
  parsed as a report and can never render test-level results.
- A nil job, a nil retry, an entry claim without a job, or a digest that does
  not match the bytes demotes or rejects the row; nothing is promoted from
  names, paths or XML.
- Ingestion (`IngestReports`) re-validates everything against the caller's
  authorized scope (`ReportScope`, e.g. the minted attempt capability). A
  nil, revoked or mismatched scope refuses the whole ingestion. Cross-attempt
  evidence reuse is refused at retention time by the store.
- Ceilings are inherited from the bounded parsers: `MaxJUnitZIPBytes`,
  `MaxJUnitZIPEntries`, `MaxReportBytes`, `MaxCases`, and at most
  `MaxRetainedAttemptReports` (32) artifacts per attempt. Archive count and
  actual byte length are validated against the manifest before any hashing,
  for metadata-only rows too. Archive count is checked before allocating the
  archive index. Oversized archives, unsafe members, duplicates, digest
  mismatches and invalid XML refuse the entire ingestion; there is no partial
  publication.
- No tokenized URLs, logs, command or environment output, report bodies or
  raw archive bytes are persisted. Retained records carry bounded projections
  (counts, outcomes, durations) plus private pins used only for idempotency
  and reuse rejection, and optionally reviewer-bound per-case observations
  (`StoredCaseObservation`, bounded by `MaxCases` per report) so retry
  histories and rankings have real per-case data to work from.

## Normalization and retention

`NewPublication` converts a validated ingestion into the retained form:
per-report totals, timed counts, exact mean/P95 durations, the exact total
timed duration, and any reviewer-bound per-case observations, each tagged
with artifact, job, shard and retry. Case IDs, class names, files and display
names are not retained; identity for cross-run comparison remains the
reviewer-bound numeric cohort identity used by `CompareReviewedCase` and
`RankReviewedCaseImpact`.

Retention is enforced at read time, not only by purge-on-write: a record at
or past the shared cutoff is excluded by `LoadPublication` and
`LoadScopePublications` even on an idle store with no save or restart since
it aged out.

A first execution (`retry: 0`) is stored and rendered separately from
framework retries, so a failed first run that eventually passes stays visible
as both facts. Missing shards are neither passed nor failed: expected slots
come only from reviewer-declared plan knowledge and render as `missing`.

Retention is linked to the Plan 05 durable history contract through the
shared surface in `internal/cihealth/retained_reports.go`:
`HistoryRetentionWindow` (30 days, equal to the durable history window),
`MaxRetainedAttemptReports`, `ReportRetentionScope` (the same
`HistoryScope` identity) and `RetainedReportKey`. Publications are retained
per scope, purged by that window, and a report proven for one attempt is
never retrievable as evidence for another attempt, run, workflow, repository
or source. The durable multi-writer lifecycle belongs to Plan 05; the store
here (`PublicationFileStore`) is the offline single-writer reference
implementation of the `Store` contract.

## Rendering

`RenderPublication` produces a deterministic, display-safe view: exact
canonical source references (`ReportLink` — numeric identities only, no host
or credential), sample counts, coverage (proved vs metadata-only vs missing
slots), exact aggregate means, and failure/error rates whose zero denominator
explicitly means "insufficient observations." `RenderCaseRankings` keeps
comparisons within one verified category; incompatible cohorts refuse rather
than merge, and the same display name can never join two cohorts. From
retained publications, `RankStoredCases` and `CaseHistories` give
deterministic rankings and per-case retry histories over the reviewer-bound
case observations (mixed scopes and over-ceiling observation sets refuse).
`CheckLiveAvailability` keeps live display disabled until producer trust and
the exact unexpired parent attempt are independently verified; rendered views
carry that gate label. There is no AI analysis, no deployment advice and no
speculative pass/fail guessing.

## Unavailable behavior and rollback

- Live ingestion is **unavailable**: there is no supported authorized archive
  reader plus independent producer binding in this repo yet, so no gateway
  feature flag is added and no test-level results are claimed from live runs.
  Run-only artifacts remain visible as metadata only.
- Rollback: the change is additive and offline. Removing the configuration
  that points at a publication store file (or deleting that private file)
  drops retained publications; no other state is touched. Reopening the
  offline snapshot reads at most 8 MiB plus one byte to detect an overrun.
  Opening a damaged or foreign store file fails closed instead of truncating it.
- Follow-up dependencies, outside this ticket: an already-authorized archive
  reader, an independent producer-binding enrollment, and the Plan 05
  durable lifecycle adopting the shared retention surface.

See the fixture regressions in `internal/cihealthreport/{provenance,manifest,
ingest,store,render,history}_test.go`; they use synthetic in-memory ZIP/XML
fixtures only and never contact GitHub, GARM, Incus or any runner
infrastructure. `TestEndToEndReportToRanking` proves the full chain from
reviewed manifest and exact bytes through retention to deterministic
rankings and retry histories, including that a tampered archive breaks the
chain at ingestion and that run-only metadata never ranks.
