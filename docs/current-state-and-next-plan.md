# Current platform state and next plan

Status date: 2026-11-17 (UTC). Verified `main`:
`fc2fa3ed` (conditional client hardening and settings wiring atop
`0daa398b`, which merged #84 and the conditional settings client; that
chain merges #75–#84 and the closed-research cleanup record). Earlier
workflow evidence belongs to its tested PR heads; it must not be
described as a separate verification of the merge tree without
exact-tree evidence.

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

| PR | Reviewed head SHA | Preserved research / deferred purpose |
|---|---|---|
| [#37](https://github.com/ShortCipher5/garm-capacity-controller/pull/37) | `71cc18c3f62fdefe91027fdbb9d338f615d8fa8a` | Runner lifecycle evidence; not qualified retirement |
| [#39](https://github.com/ShortCipher5/garm-capacity-controller/pull/39) | `f3a1f956051439bbab8ee7a919ec683e837167a0` | Local drain-gate counterexample; polling suppression can block control/cancellation |
| [#40](https://github.com/ShortCipher5/garm-capacity-controller/pull/40) | `98d2bbc6908f5c04a5521dc5d2f2c4abf3cb30b9` | Runner control/restart evidence |
| [#41](https://github.com/ShortCipher5/garm-capacity-controller/pull/41) | `617c8dd82ecc5251bc9b56f2239acb89155f36b3` | Isolation tests/threat model |
| [#42](https://github.com/ShortCipher5/garm-capacity-controller/pull/42) | `1f70b8ac61f76b60b91750c7c3270409f59bd403` | Local audit-history research, not production authority |
| [#48](https://github.com/ShortCipher5/garm-capacity-controller/pull/48) | `e5954652a5396f7003d80e394ceadcb6abcb5bd9` | Conditional finite create-only pilot evidence, not sustained retirement |
| [#51](https://github.com/ShortCipher5/garm-capacity-controller/pull/51) | `e3289459ae2913a798ec6d408baa91be10e391f6` | Unix transport research; authority/integration deferred |
| [#52](https://github.com/ShortCipher5/garm-capacity-controller/pull/52) | `bc8ceab44b76ca7570a66031a2ba2572c5cc62b4` | Local client research; authority/integration deferred |
| [#70](https://github.com/ShortCipher5/garm-capacity-controller/pull/70) | `c39d42d9b071f7c270dbbe740e402fdb1c133d77` | Compile-only native-parent experiment |
| [#71](https://github.com/ShortCipher5/garm-capacity-controller/pull/71) | `f989a1db8bf0d452f00ec8e83f52dd8dedc8db5d` | Read-only capability survey; depends on historical audit stack |

The table records preservation targets, not proof that closure has already
succeeded. The linked PR state is authoritative for the cleanup outcome.
- This integration cannot comment on issue #53 (`addComment` is denied
  at the platform level), so this file and its PR serve as the fresh
  status ledger until that restriction changes.

## Native review access (consolidated 2026-11-17)

The experimental native commits are available for independent review as
three read-only refs in this repository (review-only; no PR is opened
from them and they are not a merge proposal). On 2026-11-17 the branch
inventory was consolidated: 90 stale branches were deleted (merged work,
superseded docs, closed-PR heads preserved via GitHub `refs/pull/*`, and
intermediate native review refs whose commits remain reachable from
`3a714496`). The four remaining branches are `main`,
`feat/route-revision-guards` (denied material, untouched pending scope
resolution), and these two experiment lines:

- `review/native-settings-3a714496` — tip
  `3a7144960a84d52aaf76489b0a5df78e763a4ed5`: the current native
  settings experiment, including the second-pass recovery hardening
  (nonce-owned cross-tab locks, resolve-path 412 hold, flock-based CLI
  journal locking with profile/server binding, four-quadrant schema
  audit, legacy-marker upgrade, commit-then-drop-response coverage).
  Base is upstream `154638445c3949c1958b01812f69d9a1e4d82684` (v0.2.1),
  an ancestor of the tip. It supersedes the earlier snapshots
  (`a9d7b0a3`, `a1ff1716`, `494899b8`, `8407d624`, `c94c563b`), all of
  whose commits remain reachable from this tip.
- `review/native-settings-c94c563b` — kept as the exact slice the
  second-pass review examined; fully contained in `3a714496`.
- `review/capacity-hold-2971b0b6` — tip
  `2971b0b6e9a5e667c8c11abb1139f34a4078e1ca`: the separate offline
  hold-register leaf package (`experimental/capacityhold`), same base;
  unchanged.

Integrity checks a reviewer can run:

```sh
git fetch origin review/native-settings-3a714496 review/capacity-hold-2971b0b6 review/native-settings-c94c563b
git rev-parse origin/review/native-settings-3a714496
# expect 3a7144960a84d52aaf76489b0a5df78e763a4ed5
git rev-parse origin/review/capacity-hold-2971b0b6
# expect 2971b0b6e9a5e667c8c11abb1139f34a4078e1ca
git merge-base --is-ancestor 154638445c3949c1958b01812f69d9a1e4d82684 \
  origin/review/native-settings-3a714496 && echo base-ok
# every earlier native settings snapshot is contained in the current tip:
git merge-base --is-ancestor origin/review/native-settings-c94c563b \
  origin/review/native-settings-3a714496 && echo superseded-ok
git diff 154638445c3949c1958b01812f69d9a1e4d82684..origin/review/native-settings-3a714496
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

## Review-finding fix status (orchestrator-reported, 2026-11-16)

The findings summarized above, plus the two reported separately (a late
revision rejection committing a preceding settings change; no
concurrency reachability testing), are addressed offline by native
commit `a1ff1716` on top of `a9d7b0a3`, published as review ref
`review/native-settings-a1ff1716`:

1. The conditional revision advance now runs before the pool settings
   write, so a race detected at advance time commits only the rejection
   receipt and a generic settings-write failure rolls back both.
2. The legacy revision bump ignores only the absent-side-table case; any
   other error fails the legacy write's transaction instead of leaving a
   stale revision.
3. The native CLI and webapp now mirror the controller client's replay
   and 409 handling (stored outcome with resulting ETag; 409 as a
   definitive rejection, never UNKNOWN). Neither native client sends a
   request id yet.
4. A new real-SQLite concurrency test races 8 conditional updates on
   one observed revision: exactly one commits, losers are typed stale,
   the revision moves once, the row carries only the winner.

Reported gates on the fixed tree: gofmt/vet/build clean; Go race suites
for `database/sql`, `apiserver/routers`, `config`, `cmd/garm-cli/cmd`
pass; webapp 562/562 with build. These fixes are reported by the
orchestrator, NOT independently re-reviewed; the experiments remain not
ready to merge or deploy, and the destination/denial-scope decisions
still gate any product path.

## Recovery slice status (orchestrator-reported, 2026-11-16)

Commit `494899b8` (review ref `review/native-settings-494899b8`)
implements the no-host-access recovery slice from the task list:

1. The native CLI and webapp editor now send a persistent request
   identity (`Idempotency-Key`) on every settings write — `--request-id`
   or an auto-generated UUID in the CLI, a per-save UUID in the editor.
2. Both clients journal the exact request (id, scope, precondition,
   payload, time) before dispatch and clear it on definitive outcomes;
   a browser or process restart re-enters the UNKNOWN hold with the
   draft intact, and re-presenting the id resolves the outcome from the
   server's durable receipt.
3. The editor's UNKNOWN state now actually blocks: Save and the draft
   inputs are disabled, a re-read no longer clears it, and a deliberate
   "Resolve outcome" action settles it via the receipt. The CLI no
   longer treats any HTTP 200 as success (applied bodies must validate).
4. The legacy revision bump's benign absence check is dialect-aware
   (SQLite/MySQL/PostgreSQL forms), unit-tested per dialect; live
   MySQL/PostgreSQL qualification remains open (no such databases here).

Reported gates: gofmt/vet/build clean; Go race suites for `database/sql`,
`cmd/garm-cli/cmd`, `apiserver/routers`, `config`, `runner` pass; webapp
563/563 with build. Orchestrator-reported, not independently re-reviewed.
The controller editor/journal binding remains blocked on the previously
denied dispatch-envelope scope; nothing here bypasses it.

## Workstream A status (orchestrator-reported, 2026-11-16)

Commit `8407d624` (review ref `review/native-settings-8407d624`)
completes the host-free items of Workstream A:

1. Web editor: per-pool pending storage; fail-closed persistence (no
   journal write, no dispatch); per-pool multi-tab lock; full-draft
   restart recovery; conflict latch after 412 requiring an explicit
   "Mark reconciled".
2. Web client: strict bounds and same-patch min/max validation;
   corrupt or ambiguous server documents fail closed.
3. CLI: read-before-write refusal while a request is unresolved;
   `patch --resolve` re-presents exactly the journaled request;
   atomic journal writes; journaling failure aborts pre-dispatch.
4. Server: install marker distinguishes "never enabled" from "revision
   table lost after use" (hard refusal); reinstall after loss moves to a
   fresh epoch, invalidating outstanding ETags.
5. Loopback evidence (real server+CLI from this tree, temp SQLite):
   seeded UNKNOWN refuses a new write with the revision unmoved;
   `--resolve` applies idempotently without a receipt and replays with
   no re-application when one exists.

Reported gates: Go race suites (database/sql, routers, config, garm-cli,
runner) pass; webapp 574/574 with build. Field-scope decision recorded:
v0 covers priority/max/min-idle only; tags, enabled and repository
balancing stay on stock routes or require an explicit contract
extension. Still open: live MySQL/PostgreSQL qualification, browser and
network-kill fault injection, independent re-review, and the
destination/denial-scope decisions.

## Recovery hardening status (orchestrator-reported, 2026-11-17)

Commit `c94c563b` on `exp/integration-check` (review ref
`review/native-settings-c94c563b`, full hash
`c94c563bdd41151ac61e116439453cbf9dd74228`; all prior review refs
intact) closes the second-pass static-review findings — the remaining
host-free recovery defects. Contract advanced to revision 14
(`docs/native-settings-revision-contract.md` in this PR). Summary:

1. Web editor: atomic owned cross-tab lock (`{tabId, acquiredAt}`,
   synchronous claim, own-release only, explicit stale takeover with
   storage-event loss detection on the displaced tab); corrupt pending
   or lock records fail closed (persistent error, Save disabled, bytes
   preserved); "Mark reconciled" armed only by a fresh GET after the
   conflict. Note: journaled records now carry `tabId`; pre-change
   records surface as a recovery error, not silently.
2. Web client: strict 2xx validation pinned to the landed server wire
   shapes (strong quoted `"<epoch>:<seq>"` ETag, exact 3-field tuple,
   `{outcome, resulting_etag}` replay envelope with
   `resulting_etag === ETag` header as the identity check — the wire
   has no request-id echo); malformed success never mutates client
   state.
3. CLI: exclusive adjacent lock around all journal
   read-modify-write incl. `--resolve` (live-pid refusal naming the
   holder; dead-pid reclaim once; true two-process re-exec test); each
   record binds profile + effective server base URL (includes
   `/api/v1`); apply and resolve refuse mismatched bindings pre-network
   with no override.
4. Server: schema-loss hard failure extended to the receipts table on
   GET and PATCH; install marker gains durable `generation` and
   `last_epoch` floor (epochs minted as `max(unix_now, last_epoch+1)`,
   persisted in-transaction) so no lost-generation ETag can match after
   reinstall; recovery is the explicit `ReinstallOfflinePoolSettingsSchema`
   primitive, unreachable from request paths; documented that a local
   epoch is still not an anti-rollback authority and the
   marker-lost-with-tables hole remains.
5. Commit-then-drop-response handler test: commit survives a dropped
   response, retry answers from the durable receipt, revision static,
   no second receipt.

Reported gates (orchestrator-run on the final tree): gofmt/vet clean;
race suites green for `database/sql` (84s), `apiserver/routers`,
`config`, `cmd/garm-cli/cmd`; webapp 590/590, `svelte-check` clean,
build clean. Loopback (fresh env, real server+CLI binaries from this
tree): 11/11 — GET+ETag, quoted-If-Match apply advancing the revision,
journal clearing, lock refusal, binding refusals on apply and resolve,
`--resolve` idempotent apply, receipt id→payload refusal with no
double-apply.

Still open: live MySQL/PostgreSQL qualification, browser-level and
network-kill fault injection, independent re-review of `c94c563b`,
destination/denial-scope decisions. The controller editor/journal
binding remains blocked on the previously denied dispatch-envelope
scope; nothing here bypasses it. All experimental features remain
disabled by default — offline work, not deployable.

## Conditional client hardening and settings wiring (orchestrator-reported, 2026-11-17)

Addresses every statically reviewable item of review section B, on main:

1. Client library hardening (`internal/garmapi`): replay envelopes are
   validated against the response ETag (`resulting_etag === ETag`,
   applied-only outcomes, exact two-key shape) with any mismatch failing
   closed to UNKNOWN; the patch result is now statically split
   (`ConditionalPatchResult{Replay|Settings}`, exactly one set) so a
   replay can never masquerade as pool settings; organization and
   enterprise scopes fail before any network use with the typed
   `ErrConditionalScopeUnsupported` (repository scope remains the only
   implemented scope); the safeguard boundary is documented — the
   low-level client never enforces controller policy such as the
   minimum-idle decrease block.
2. Settings wiring (B.5): the manager (`NewConditional`, off-by-default
   via the existing `experimental_conditional_settings` config opt-in)
   routes pool writes that touch only priority/max_runners/min_idle_runners
   on repository-scoped pools through the native conditional API, inside
   the unchanged durable journal intent/finish lifecycle; the legacy
   policy guards (including the minimum-idle decrease safeguard) are
   mirrored manager-side; tags/enabled/balancing and non-repository
   scopes keep the legacy path. Confirmed results are refreshed through
   a legacy read. Definitive conditional rejections (400/403/404/409/
   422/428/scope) are journaled as rejected, not unknown.
3. Authoritative UNKNOWN resolution: `Manager.ResolvePool` re-presents
   the journaled request (same Idempotency-Key + payload, fresh
   precondition) so the native durable receipt answers definitively;
   ambiguous outcomes and stale preconditions keep the intent UNKNOWN —
   a 412 after possible receipt loss is never treated as proof of
   non-effect. Journal `finish` now accepts intents in state `unknown`
   (previously only `pending`), enabling that lifecycle. Exposed as
   `POST /admin/api/v1/garm/resolve` and a "Resolve pending write"
   button in the settings UI; discovery-only deployments report
   disabled.
4. Branch consolidation: 90 stale branches deleted; four remain (`main`,
   the two native experiment refs above, and the denied route-guard
   branch kept untouched pending denial-scope resolution).

Reported gates: gofmt/vet clean; `go test -race ./...` green except the
two documented environmental `internal/admission` root-permission
failures that also fail on unmodified main; web suite 58 pass with the
same 8 pre-existing module-resolution failures present before this
change (verified by stash-and-rerun on clean main). New coverage:
manager conditional tests (confirmed write via native API with quoted
If-Match + journaled request id, pre-dispatch min-idle refusal with zero
network calls, tags keep the legacy path, raced-revision conflict is
rejected, commit-then-drop UNKNOWN resolves from the durable receipt,
ambiguous resolve retains the intent).

Still not done (unchanged blockers): physical capacity enforcement and
host inventory, safe retirement, onboarding/two-way sync, live
backup/recovery, native destination selection, denial-scope resolution.
All conditional behavior is opt-in and off by default; the native work
remains experimental review refs, not deployable.

## Evidence rules (unchanged)

Record exact branch/head/base/merge tree, tested commands with
pass/fail/skip/not-run distinctions, fixture boundaries, and remaining
assumptions. No secrets, host addresses, credential-bearing
diagnostics, or deployment claims. Experimental features stay disabled
by default and are described as offline work, not deployable.
