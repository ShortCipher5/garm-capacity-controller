# Current platform state and next plan

Status date: 2026-11-16 (UTC). Verified `main`:
`0e24cbdc8a798338acacb213d3fe256a804c7477` (merge of #82 atop
`76bcc628`, which merged #75–#81). All 13 exact-head CI checks pass on
that tree.

This document is the current status ledger for the project. It
supersedes the stale snapshot sections of issue #53 (use #53 for
architecture and safety background only) and replaces the intent of
#22, which is closed in favor of this file.

## What `main` provides today (verified)

- Durable admission ledger and placement core (`internal/admission`):
  SQLite schema/migrations, `BEGIN IMMEDIATE` budget accounting,
  bindings, source receipts, catalog, archives.
- Verified read-only transports (`internal/garmapi`, `internal/incusapi`):
  exact observations, bounded inventory, conservative ambiguity handling.
- Existing-resource discovery and visibility (merged via #82):
  existing GARM pools and scale sets are discovered and shown without
  being recreated; management views refresh; drafts are preserved
  across refresh/cancel/conflict; read-only observations reject expired
  or ambiguous snapshots (#78, #80, #81).
- Job-safe retirement assessment helpers (#76) and hard-evidence
  Incus memory-limit checks (#79).
- Provider/gateway preflight and strict role boundaries (#75, #77).
- Disposable qualification harnesses (pinned-garm, paired artifacts,
  browser) that run in CI against exact heads.

## What `main` does not provide

- No global physical capacity fence. Nothing on `main` can prevent a
  competing create at the host/provider boundary.
- No two-way conflict-safe settings editing. Discovery and visibility
  are read-mostly; edits are not yet coordinated against a revision
  contract on the verified tree.
- No safe retirement/reclaim. Charges and holds are not yet bound to
  authoritative physical completion evidence.
- No live deployment, activation authority, or pilot entitlement of
  any kind. The milestone ladder in issue #53 still governs.

## Native conditional-settings workstream (offline, provisional)

A parallel experimental workstream explores revision-bearing
pool-settings reads/writes for native GARM (base
`cloudbase/garm@15463844`, v0.2.1) plus matching CLI/webapp/controller
clients. State as of this writing:

- Code exists only as **local, unpushed branches**. No PRs, nothing
  merged anywhere, no native fork destination selected.
- Wire shape (provisional, "wire v0"): `GET`/`PATCH` on an opt-in,
  flag-disabled experimental endpoint under
  `/api/v1/offline/entities/{organization|repository|enterprise}/{id}/pools/{id}/settings`;
  strong quoted `"<epoch>:<seq>"` ETag with `If-Match`; strict
  three-field JSON (`priority`, `max_runners`, `min_idle_runners`);
  typed 400/403/404/409/412/422/428 rejections; optional
  `Idempotency-Key` bound durably to scope+payload with same-transaction
  persistence; no-op does not advance the revision.
- Claimed locally, **not independently verified**: atomic
  conditional writes, legacy-writer revision participation, receipt
  replay, and a real loopback API-to-client test. Outstanding per the
  workstream's own last report: real UI-to-server testing and
  lost-response fault injection (UNKNOWN handling is unit-level only).
- Known open differences against the private prototype it must
  eventually reconcile with: per-pool revision versus one global
  SQLite-wide epoch/sequence; inline legacy bump versus triggers;
  replay envelope shape. No compatibility claim is made in either
  direction.

The full provisional contract (revision 10) is attached in this PR as
`docs/native-settings-revision-contract.md`. It is a design and
comparison document: it does not publish an interface, grant authority,
or assert deployability.

## Ordered next work

1. **Physical authority design gates everything.** Produce an
   authorized, read-only host/provider inventory (installed versions,
   processes, database ownership, principals that can
   create/delete/restart runners, credential paths — no secrets).
   Select the enforcement boundary, exclusive authority and
   old-writer/rollback fencing from that evidence. Discovery alone
   cannot prevent competing creates. Decide the research stack only
   after this design is selected.
2. **Conflict-safe settings integration (still offline).** Integrate
   and qualify the provisional native/controller settings writes:
   every relevant legacy writer must advance the revision atomically;
   uncertain outcomes stay charged/held until authoritative; old
   clients need a staged compatibility and enforcement-switch story.
   Partially blocked pending resolution of the previously denied
   dispatch-envelope publication scope.
3. **Job-safe retirement and recovery.** Retain charges while physical
   outcome is uncertain; require operation-specific, authenticated
   terminal evidence from a qualified verifier; handle lost
   acknowledgements, concurrent job assignment, crash/restart and
   restored databases.
4. **Combined-system qualification.** Concurrent writers, legacy
   bypass attempts, entity/auth boundaries, commit failure, lost ACK
   and duplicate delivery, process restart, database restore/clone,
   authority loss, credential isolation, job-assignment versus
   retirement, provider failure and physical-fence bypass attempts —
   then an isolated canary with separate database/credentials and an
   explicit rollback plan.

## Housekeeping record (2026-11-16)

- Merged: #75, #76, #77, #78, #79, #80, #81, #82 (all fresh
  exact-head checks green; #82 required a conflict-resolution update
  against current main, re-verified before merge).
- Closed unmerged: #2 (synthetic web demo superseded by the real UI),
  #22 (replaced by this document), #74 (test-only permission check,
  purpose served).
- Still open as drafts, needing owner decisions: #70, #71
  (test/qualification-support material) and the older streams #52,
  #51, #48, #42, #41, #40, #39, #37.
- This integration cannot comment on issue #53 (`addComment` is denied
  at the platform level), so this file and its PR serve as the fresh
  status ledger until that restriction changes.

## Evidence rules (unchanged)

Record exact branch/head/base/merge tree, tested commands with
pass/fail/skip/not-run distinctions, fixture boundaries, and remaining
assumptions. No secrets, host addresses, credential-bearing
diagnostics, or deployment claims. Experimental features stay disabled
by default and are described as offline work, not deployable.
