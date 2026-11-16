# Current platform state and next plan

Status date: 2026-11-16 (UTC). Verified `main`:
`0e24cbdc8a798338acacb213d3fe256a804c7477` (merge of #82 atop
`76bcc628`, which merged #75–#81). Earlier workflow evidence belongs to
its tested PR heads; it must not be described as a separate verification
of the merge tree without exact-tree evidence.

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

- Native settings and hold-register review copies are now published at
  the refs listed below. These are not merge proposals or production
  integration. The production native destination remains unselected;
  controller-client work is separately reported, not included in those refs.
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
- Historical research drafts are recorded below for archival closure,
  not merger. Closing them preserves their review history and does not
  mean the underlying production requirements are implemented. Reuse of
  selected evidence remains conditional on the physical-authority design.

| PR | Reviewed head prefix | Preserved research / deferred purpose |
|---|---|---|
| [#37](https://github.com/ShortCipher5/garm-capacity-controller/pull/37) | `71cc18c3` | Runner lifecycle evidence; not qualified retirement |
| [#39](https://github.com/ShortCipher5/garm-capacity-controller/pull/39) | `f3a1f956` | Local drain-gate counterexample; polling suppression can block control/cancellation |
| [#40](https://github.com/ShortCipher5/garm-capacity-controller/pull/40) | `98d2bbc6` | Runner control/restart evidence |
| [#41](https://github.com/ShortCipher5/garm-capacity-controller/pull/41) | `617c8dd8` | Isolation tests/threat model |
| [#42](https://github.com/ShortCipher5/garm-capacity-controller/pull/42) | `1f70b8ac` | Local audit-history research, not production authority |
| [#48](https://github.com/ShortCipher5/garm-capacity-controller/pull/48) | `e5954652` | Conditional finite create-only pilot evidence, not sustained retirement |
| [#51](https://github.com/ShortCipher5/garm-capacity-controller/pull/51) | `e3289459` | Unix transport research; authority/integration deferred |
| [#52](https://github.com/ShortCipher5/garm-capacity-controller/pull/52) | `bc8ceab4` | Local client research; authority/integration deferred |
| [#70](https://github.com/ShortCipher5/garm-capacity-controller/pull/70) | `c39d42d9` | Compile-only native-parent experiment |
| [#71](https://github.com/ShortCipher5/garm-capacity-controller/pull/71) | `f989a1db` | Read-only capability survey; depends on historical audit stack |

The table records preservation targets, not proof that closure has already
succeeded. The linked PR state is authoritative for the cleanup outcome.
- This integration cannot comment on issue #53 (`addComment` is denied
  at the platform level), so this file and its PR serve as the fresh
  status ledger until that restriction changes.

## Native review access (added 2026-11-16)

The experimental native commits are available for independent review as
two read-only refs in this repository (review-only; no PR is opened
from them and they are not a merge proposal):

- `review/native-settings-a9d7b0a3` — tip
  `a9d7b0a3dafb5506ac54ad69e47165dea969d4c1`: the combined server
  (slices `013ed0e7` + `a895ba25`) and CLI/webapp client (`7e3cc544`)
  integration merge. Base is upstream
  `154638445c3949c1958b01812f69d9a1e4d82684` (v0.2.1), an ancestor of
  the tip. 25 files, +5222/-11: experimental additive files plus
  changes to existing native paths, including the stock pool-update
  helper's best-effort revision bump. The new endpoint is flag-gated;
  this does not mean every changed stock path is flag-gated.
- `review/capacity-hold-2971b0b6` — tip
  `2971b0b6e9a5e667c8c11abb1139f34a4078e1ca`: the separate offline
  hold-register leaf package (`experimental/capacityhold`), same base.

Integrity checks a reviewer can run:

```sh
git fetch origin review/native-settings-a9d7b0a3 review/capacity-hold-2971b0b6
git rev-parse origin/review/native-settings-a9d7b0a3
# expect a9d7b0a3dafb5506ac54ad69e47165dea969d4c1
git rev-parse origin/review/capacity-hold-2971b0b6
# expect 2971b0b6e9a5e667c8c11abb1139f34a4078e1ca
git merge-base --is-ancestor 154638445c3949c1958b01812f69d9a1e4d82684 \
  origin/review/native-settings-a9d7b0a3 && echo base-ok
git diff 154638445c3949c1958b01812f69d9a1e4d82684..origin/review/native-settings-a9d7b0a3
```

Git commit IDs identify the exact committed tree and ancestry; the patch
SHA-256 checks the transmitted patch bytes. These are complementary checks,
not a claim that SHA-1 is cryptographically stronger than SHA-256. The
manifest (`BUNDLE.txt`, `files.stat`, `SHA256SUMS`) remains available from
the orchestrator for cross-checking scope. Its report that the refs exclude
credentials and previously denied material still requires an independent
full-content/provenance review; availability alone does not establish it.

## Independent source-review limitations (2026-11-16)

At native review tip `a9d7b0a3`, the stock update helper calls a legacy
revision bump that discards database errors. This is not fail-closed
all-writer revision participation. The native CLI and web client do not
send `Idempotency-Key` or implement typed 409/replay handling in their
experimental request code. The reported replay-envelope/409 fixes belong
to the separate controller client, not these native clients. Complete
native review and fault tests remain open; this cleanup does not fix or
qualify the experimental implementation.

## Evidence rules (unchanged)

Record exact branch/head/base/merge tree, tested commands with
pass/fail/skip/not-run distinctions, fixture boundaries, and remaining
assumptions. No secrets, host addresses, credential-bearing
diagnostics, or deployment claims. Experimental features stay disabled
by default and are described as offline work, not deployable.
