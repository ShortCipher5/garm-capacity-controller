# Native settings revision contract (Agent 1 deliverable)

Status: draft/provisional, 2026-11-17 revision 14 (recovery hardening
complete offline). Review refs published on the controller repository
(`review/native-settings-c94c563b`, full hash
`c94c563bdd41151ac61e116439453cbf9dd74228`; all prior refs intact);
not merged to any product branch; not deployed. Experimental features
remain disabled by default and are offline work, not deployable. No
authority granted.

**HOLD, REVISED (owner instruction, 2026-11-16, latest):** Agents 2, 3 and 5
MAY implement OFFLINE EXPERIMENTAL vertical slices in per-agent worktrees of
a verified local checkout of upstream `cloudbase/garm` @
`154638445c3949c1958b01812f69d9a1e4d82684` (worktrees/branches:
`exp/agent2-native-settings`, `exp/agent3-client`, `exp/agent5-capacity-hold`,
all based on `15463844`). The hold now covers only: publishing, production
migration/rollout, any compatibility claim against CJ's private prototype,
and the reconciled production interface (DEC-1..8 remain open). Publication,
deployment and the denied-material rules below are unchanged.

**Review basis:** this comparison uses the interface facts CJ supplied plus
the pinned upstream source. The private native overlay is not available for
code review, and its relationship to the earlier denied route/dispatch/schema
publication is unresolved. Do not port it, and do not recreate denied
material under another name.

## 1. Verified pins

| Item | Pin | Verified |
|---|---|---|
| Controller base | `main` = `c2ee956449e15ab8320c67f93aa3a2ba7576ede6` | 2026-11-16 |
| Discovery draft | PR #82 head `e976d82478a011f755ee56bfc9e286b501f08651`, 4/4 workflows green | 2026-11-16 |
| Native base | upstream `cloudbase/garm` v0.2.1 @ `154638445c3949c1958b01812f69d9a1e4d82684` **plus private changes on top** (owner-confirmed) | 2026-11-16 |
| Native CLI | `cmd/garm-cli` in the same upstream repo at the pinned commit (tree verified) | 2026-11-16 |
| Fork/branch destination | none verified; owner checking exact checkpoints and CLI base | missing |

Upstream facts verified from the pin: all settings updates are `PUT`
(`apiserver/routers/routers.go`); no `If-Match`/ETag conditional machinery in
the API controllers; update payloads are `UpdateControllerParams`,
`UpdateEntityParams`, `UpdatePoolParams` (`params/requests.go`).

## 2. Scope boundary

In scope: reconciling a revision-bearing read + conditional-update interface
for settings-bearing entities, and the controller-side coordination protocol.

Out of scope, unchanged:

- **Capacity enforcement.** The global budget gate stays in the controller
  admission ledger. Settings concurrency serializes writers; it is not a
  capacity token. Settings synchronization alone is not a global capacity
  gate.
- **Physical fencing** (no-restart/no-recreation authority, host boundaries).
  Agent 4/5 territory; settings concurrency never substitutes for it.
- **Denied content.** Route guards, dispatch and schema-path material stays
  unpublished and is not recreated under any name.

## 3. The two provisional interfaces

### 3.1 Agent-1 proposal (revision 1 of this contract; superseded, kept for comparison)

- `PUT` on existing native routes: `/api/v1/controller`,
  `/repositories|organizations|enterprises/{id}`, `/pools/{id}` (+ nested).
- Per-entity opaque `settings_revision` token; `Settings-Revision` /
  `If-Settings-Revision` headers.
- `200` applied; `409` stale; `428` missing precondition (mode-dependent);
  `404` wrong scope/deleted.
- Rollout modes `open` → `conditional` → `enforced` via new config key.
- Guarded fields: all settings columns of the five entity classes.

### 3.2 CJ offline prototype (interface facts as supplied by owner across two rounds; code not yet reviewed)

- `GET`/`PATCH` on `/offline/entities/{organization|repository|enterprise}/{UUID}/pools/{UUID}/settings`.
- Strong quoted epoch/sequence `ETag` + `If-Match`; the epoch/sequence is
  maintained by **nine triggers over pools, tags and pool_tags only**.
  Repository, organization, enterprise and controller settings do **not**
  bump it. It is an order over those covered SQLite writes — not an order
  over all settings and not a physical fencing point.
- Epoch/sequence live in a separate singleton table in the same SQLite
  database. A full backup includes it, but restoring a backup can roll it
  back; there is no independent anti-rollback authority.
- `PATCH` is deliberately strict canonical JSON, not a general partial-JSON
  API: partial updates of exactly `priority`, `max_runners`,
  `min_idle_runners` (at least one required); unknown, duplicate or
  noncanonical fields are rejected. Ranges: priority and min-idle
  0–1,000,000; max 1–1,000,000; the combined min ≤ max invariant is checked
  against the current row inside the transaction whenever either count is
  supplied.
- Statuses: `428` missing precondition, `400` malformed, `412` stale,
  `403` policy refusal, `422` invalid coupled limits. `503` is a **generic**
  unavailable response, not a distinct server-issued "outcome unknown"
  receipt: it may follow pre-write failures or an uncertain commit, carries
  no request-id or authoritative receipt, and cannot by itself be
  distinguished from overload.
- A no-op write may leave the revision unchanged: a revision is **not** an
  operation identity.
- No request-id execution journal or replay receipt — revision preconditions
  only. The controller's local request-id journal is separate and is not
  proof of native execution. In particular, a later `412` after an earlier
  lost response does not prove the earlier request failed; that request may
  itself have advanced the revision. Request receipts are a target-design
  addition (§4.1, DEC-9), not prototype behavior.
- Installed explicitly for testing; not yet a production migration and not a
  rollback-resistant authority.

### 3.3 Comparison table

| Dimension | A1 proposal | CJ prototype | Consequences | Compatibility / migration | Decision |
|---|---|---|---|---|---|
| Endpoint strategy | extend existing `PUT` routes | new `/offline/.../settings` namespace | Extending changes behavior of every existing writer (UI, CLI, scripts) and needs staged rollout; the namespace is additive and test-safe but does not protect real routes until merged into them | Existing writers break at enforcement time with extended routes; namespace can coexist with stock clients during evaluation | **DEC-1** |
| Method | `PUT` (entity semantics) | `PATCH` (partial settings) | `PATCH` narrows read-modify-write scope to declared fields; `PUT` matches upstream idiom | Client payload models differ; generated swagger client handles both | part of DEC-1 |
| Revision scope | per-entity token | one global epoch/sequence over pools/tags/pool_tags only (nine triggers); entity and controller settings do not bump it | Global over covered tables: any pool/tag write invalidates every client's token — conflict storms on busy servers; gives a total order **only over covered writes** — a single revision order over covered writes, which does not fence old binaries, restored databases or physical operations, and which entity-level changes bypass. Per-entity: fine-grained concurrency, no cross-entity order; reconciliation needs a separate fence. **This is the deep mismatch; renaming headers does not reconcile it.** | A global sequence is a superset over covered tables only (per-entity monotonicity implied there, isolation lost). Per-entity cannot emulate global ordering. | **DEC-2** |
| Wire mechanics | custom `Settings-Revision` headers | standard `ETag`/`If-Match` | ETag reuses HTTP semantics, proxies and client libraries; custom headers need bespoke plumbing but avoid HTTP-cache ambiguity on GETs | ETag requires cache-control discipline on responses | **DEC-3** |
| Guarded fields | all settings of 5 entity classes | capacity tuple only (priority, max, min-idle); strict canonical JSON; ranges 0–1,000,000 (max 1–1,000,000); min ≤ max enforced in-transaction when either supplied | Prototype scope matches the capacity-gate purpose; A1 scope also covers balancer/credentials/controller URLs (full settings sync) with a larger blast radius inside GARM | Widening later is additive for PATCH; narrowing after rollout breaks clients | **DEC-4** |
| Status mapping | `409` stale, no explicit uncertain code | `412` stale, `400` malformed, `428` missing, `403` policy refusal, `422` coupled limits; `503` is generic unavailable, NOT an uncertainty receipt | `412` is RFC-correct for `If-Match`; `403`/`422` give precise refusal reasons; there is no explicit uncertain receipt — after a lost response, `503` cannot separate overload from ambiguous execution, so ambiguity stays UNKNOWN client-side | Clients must handle both mappings during any transition | **DEC-5** |
| Idempotency / operation identity | none proposed (journal + readback only) | none; revision preconditions only; no-op may not bump revision | Neither interface produces request-correlated execution evidence; a lost-response write is permanently UNKNOWN at the native layer and a revision advance cannot be tied to a specific operation | A native request-id journal would be an additive extension — new scope decision | **DEC-9** |
| Rollout modes | `open`/`conditional`/`enforced` | none; installed explicitly for tests | Modes give a compatibility window but permit a silent-unsafe interval; prototype fails closed always but cannot roll out gradually | Mode flip needs updated UI/CLI first; enforced = `428` for old binaries | **DEC-6** |
| Production authority | production migration path with rollback | singleton table in the same SQLite DB; full backup includes it but restore can roll it back; no independent anti-rollback authority; triggers installed for tests only | Epoch/seq by trigger is simple, but any fencing or authority claim fails across restore until the sequence is restore-safe | Needs migration design + restore semantics before any authority claim | **DEC-7** |
| Code provenance | would land in a verified fork | private overlay; relationship to earlier denied publication unresolved | Cannot be reviewed, ported or published until scope is clarified | — | **DEC-8** |

No interface is assumed to win without code-level review (§1 review basis).

### 3.4 Provisional wire v0 (experimental slice, in flight on Agents 2/3)

Owner-directed experiment, 2026-11-16. One vertical slice, offline and
feature-flagged (default OFF; flag OFF -> routes act nonexistent, zero
schema side effects, stock behavior unchanged):

- `GET`/`PATCH /api/v1/offline/entities/{organization|repository|enterprise}/{entityID}/pools/{poolID}/settings`
- GET returns strict `{"priority","max_runners","min_idle_runners"}` plus a
  strong quoted `ETag: "<epoch>:<seq>"`.
- PATCH: strict canonical JSON over exactly those three fields (>=1 required;
  unknown/duplicate/noncanonical -> 400). Ranges: priority, min-idle
  0–1,000,000; max 1–1,000,000. min ≤ max checked against the current row
  in-transaction when either is supplied -> 422.
- `If-Match` required: 428 missing, 400 malformed, 412 stale (carries current
  ETag); 403 wrong entity / pool not a member / no access; 503 generic.
- One DB transaction covers membership check, revision check, update and
  revision advance; stale/failed commits leave state untouched. True no-ops
  do not bump the revision (DEC-10).
- Revision mechanism (per-pool column vs global epoch/sequence) is Agent 2's
  documented choice for the slice — an input to DEC-2, not a resolution.
- Receipts (§4.1) are deliberately OUT of the slice; UNKNOWN after a lost
  response is held client-side with no retry and no unconditional fallback.

Flagged differences (to verify against the landed branches, not assumed):
route shape deliberately mirrors CJ's prototype namespace for comparability
— compatibility is NOT claimed until code-level comparison; status mapping
follows the prototype's 412/428/400/403/422; rev-1's five-entity PUT scope
remains superseded; the slice has no rollout modes (flag on/off only), so
DEC-6 is untouched.

## 4. UNKNOWN handling and evidence (corrected)

A matching read is **not** proof that a particular request succeeded, and a
readback must never trigger charge release or retry by itself:

- Equivalent settings can arise from another writer, an earlier equivalent
  request, or replay; the current controller code already encodes this in
  `Manager.ReconcilePending` — observe-only, `observed_desired` never clears
  an intent, and there is deliberately no clear/reset method
  (`internal/garmmanagement/manager_linux.go:304`).
- Releasing a held charge requires **request-correlated authoritative
  evidence**, e.g. a server-side idempotency journal mapping request-id to
  the applied revision (a receipt bound to request-id and post-image), or a
  verified server response whose provenance ties it to this exact request
  (complete response over authenticated transport, correlatable request-id).
  A `412`/`409` stale qualifies only under that provenance; by definition it
  then proves non-effect of the conditional write.
- Until such evidence exists on the native path, uncertain outcomes keep
  intents `unknown` and keep any related charges held.
- Physical fencing is a separate prerequisite: request-correlated settings
  evidence says nothing about whether a previously accepted runner request
  can still execute (Agents 4/5).

Owner round-2 corrections sharpen this further:

- A stale response is non-effect proof **only** when it is the verifiable
  response to the same request. After a lost response, a later `412` (or any
  later observation) does not prove the earlier request failed — that
  request may itself have advanced the revision.
- The native prototype has no request-id execution journal, and a revision
  is not an operation identity (no-ops may not bump). Neither interface, as
  specified, produces request-correlated execution evidence.
- Consequence: settling a charge held over a native UNKNOWN cannot be
  grounded in native evidence today. Owner direction (round 3): make
  request-correlated receipts part of the target design (§4.1, DEC-9), keep
  true no-ops off the revision (DEC-10), and treat permanent observe-only
  recovery as the fallback that can block the controller indefinitely. A
  verifiable per-request stale response remains the only existing native
  signal that releases; a receipt settles an UNKNOWN to a known outcome and
  finalizes a consumed charge — it is not a release signal by itself.

### 4.1 Request-correlated receipt semantics (paper design, target interface)

Owner direction, 2026-11-16 (DEC-9 resolved in principle; paper design only —
the implementation hold is unchanged):

- The client sends `Idempotency-Key: <request-id>` (a fresh UUID per logical
  operation) on conditional writes.
- The server stores one receipt per request-id **in the same transaction as
  the settings update**: request-id, entity kind/id, canonical payload hash,
  precondition token presented, terminal outcome (`applied_changed`,
  `applied_noop`, `rejected_stale` with the observed revision,
  `rejected_invalid`, `rejected_policy`), resulting revision, timestamp.
- Replay rules: same request-id + identical payload hash returns the stored
  outcome (safe retry). Same request-id + different payload is refused —
  one request-id never names two different executions (exact status code,
  Q11).
- True no-op: outcome `applied_noop`, revision unchanged (DEC-10). The
  receipt — not the revision — records that the operation ran; the revision
  stays about state, avoiding gratuitous conflicts.
- What a receipt proves: this exact request, with this exact payload, reached
  a terminal outcome at this server. What it does not prove: survival across
  a database restore (same-database storage shares DEC-7's rollback
  exposure), fencing of old binaries or direct DB writers, or any physical
  effect.
- Capacity consequence: an `applied_*` receipt lets the controller settle the
  journal intent and finalize a consumed charge; it never justifies
  releasing one. Release still requires proven non-effect (a verifiable
  per-request stale response) or operator reconciliation. Rollback-resistant
  receipt authority (DEC-7) and physical fencing (Agents 4/5) remain separate
  prerequisites.
- Open details: reuse-conflict status (Q11 — **decided for v1: 409**),
  receipt retention/purging (Q12 — **v1: none, no GC, documented**), whether
  reads participate (Q13 — **v1: writes only, GET ignores the header**).

## 5. Ledger sequence — verified against actual controller code

The revision-1 draft claimed a "reserve budget delta in the admission
ledger" step. **That step does not exist.** The actual management write path
has no admission-ledger interaction:

1. `Manager.UpdatePool`: request-id replay check; fresh GET; fingerprint
   compare (pre-dispatch conflict) — `manager_linux.go:173-188`.
2. Durable journal intent under a one-unresolved-write barrier
   (`journal_linux.go:20`).
3. `ManagementClient.UpdatePool`: GET; fingerprint check; `PUT`; GET readback
   comparison; divergence or failed readback → `ErrManagementOutcomeUnknown`
   — `internal/garmapi/management.go:327-400`.
4. `complete`: nil error → `confirmed`; Disabled/Conflict/Unsafe →
   `rejected`; anything else → `unknown` — `manager_linux.go:125-144`.
5. `ReconcilePending`: observation only (§4).

Corrections carried into this contract:

- Budget reservation for settings-driven capacity deltas is a **proposal**,
  not current behavior. Designing it is admission-owner work (with Agent 5),
  must respect the journal's single-unresolved-write barrier, and needs its
  own release-evidence chain (§4). Do not describe it as implemented.
- A settings conflict — even a request-correlated one — does not prove a
  capacity reservation is safe to release, and a settings success does not
  prove capacity accounting happened. The two systems exchange evidence only
  through explicit, designed handoff points.
- Capacity admission and physical fencing stay separate from settings
  concurrency (§2).

## 6. Migration and rollout (provisional, blocked by DEC-1..8)

- Extended-route path (if DEC-1 chooses it): staged `open`/`conditional`/
  `enforced` with updated UI/CLI first; enforced = `428` for header-free
  binaries; rollback is a visible downgrade that re-disables controller
  writes.
- Namespace path (if DEC-1 keeps `/offline`): real routes remain unprotected
  until the interface is merged into them; treat namespace results as test
  evidence only.
- Either way: no enforcement claim until the epoch/revision survives restore
  (DEC-7) and the release-evidence chain of §4 exists. A receipt table (§4.1)
  lives in the same database and inherits the same restore exposure; making
  receipts rollback-resistant is a separate prerequisite, not a receipt
  feature.

## 7. Ownership map (unchanged; hold preserved)

Controller repo, branch per agent off `c2ee956`, PRs stay draft:

| Agent | Owns | Must not touch |
|---|---|---|
| 1 (this contract) | `docs/native-settings-revision-contract.md` | all code |
| 3 (controller client + native UI/CLI) | `internal/garmapi/management.go`, `internal/garmmanagement/`, `internal/adminview/garm_*.go`, `cmd/capacity-gateway/garm_management.go`, `web/src/**`, `web/e2e/**`, `cmd/garm-cli` fork | admission/ledger core, discovery read models |
| 4 (host authority) | `qualification/**`, host-evidence docs | no product code without approval |
| 5 (retirement/uncertainty) | `internal/admission`, `internal/gateway` release protocol (with 4's design) | `garmapi` write path, journal schema shape |
| 6 (reviewer) | review only | — |

Native destination: none yet (DEC-8). When one exists: `database/sql/**`,
`apiserver/controllers/**`, `params/**`, `config/**`, `client/` regen owned by
Agent 2; native webapp + CLI by Agent 3.

## 8. Decisions and remaining questions

Decisions (recommendations deferred until code review, per owner; round-3
owner directions marked; DEC-1/3/4/5 are provisionally informed by the wire
v0 experiment §3.4 but resolved by nothing until the branches are compared):

- **DEC-1** endpoint strategy + method: extend native routes vs `/offline`
  namespace (and PATCH vs PUT).
- **DEC-2** revision scope: per-entity vs global epoch/sequence vs hybrid
  (per-entity token plus a global order over covered writes for
  reconciliation barriers).
- **DEC-3** wire mechanics: custom headers vs `ETag`/`If-Match`.
- **DEC-4** guarded field scope: capacity tuple vs all settings.
- **DEC-5** status mapping: adopt `412` stale / `400` malformed / `428`
  missing / `403` policy refusal / `422` coupled limits, or keep `409`;
  `503` must not be treated as a server-issued uncertainty receipt.
- **DEC-6** rollout modes vs fail-closed-always.
- **DEC-7** productionization: trigger-maintained epoch as a real migration
  (today a singleton table that backup-restore can roll back; no independent
  anti-rollback authority), restore semantics, who installs.
- **DEC-8** code provenance/destination fork once the overlay's relationship
  to the denied publication is clarified.
- **DEC-9** request-correlated execution evidence — **owner direction, round
  3: receipts are part of the target design**, with the §4.1 semantics
  (idempotency record bound to exact scope and payload, persisted
  atomically with the update, reuse with different content refused). A
  receipt still needs separate rollback-resistant authority (DEC-7) and
  physical fencing; it never by itself justifies releasing capacity.
  Permanent observe-only recovery is the documented fallback, with its
  indefinite-block risk.
- **DEC-10** true no-op semantics — **owner direction, round 3: a true no-op
  does not bump the settings revision**. The revision stays about state; the
  receipt records the operation. Proposed contract choice, not a claim about
  prototype behavior.

Exact remaining questions for CJ (round-2 answers to Q2–Q6 and Q8 are
incorporated in §3.2 and the table; Q9/Q10 resolved by the DEC-9/DEC-10
directions above):

- **Q1** (open; owner round 3) The cumulative private API overlay is not
  cleared for external sharing. Keep open; do not select an upstream
  destination or move potentially denied material.
- **Q7** (open; owner round 3) No verified Chris-owned native fork exists;
  destination remains unselected.
- **Q11** Exact status code/refusal shape for idempotency-key reuse with
  different content (§4.1).
- **Q12** Receipt retention/purging policy (§4.1).
- **Q13** Do reads participate in idempotency/receipts, or writes only (§4.1)?

## 9. Experimental branch comparison and proposed integration path

Reference implementation of wire v0, landed 2026-11-16 (all based on
`15463844`, local worktrees; published for review as immutable
`review/*` refs on the controller repository — no product branch, no
deployment):

| Branch | Commit | What landed |
|---|---|---|
| `exp/agent2-native-settings` | `013ed0e7` + `a895ba25` | `013ed0e7`: as in revision 6 (flag-gated offline API, per-pool revision side table, atomic conditional writes). `a895ba25` (slice 2): **legacy-writer participation** — the stock pool-update path (`UpdateEntityPool`, behind `PUT /api/v1/pools/{id}` and nested entity PUTs) advances the per-pool revision in the same transaction via a best-effort inline UPDATE (silent no-op when the side table is absent; stock write never fails; coarse: any pool-row write bumps; never double-bumps); and **durable request receipts** — optional `Idempotency-Key` on PATCH, lazy `offline_pool_settings_request_receipts` table, receipt committed in the same transaction as the effect, replay returns the stored outcome with zero re-application, id reuse with different payload/scope -> 409, rejected outcomes (400/412/422) persist their receipt, 403/404/428 and generic failures persist none, GET does not participate. Orchestrator-verified: gofmt/vet clean; `go test -race -tags testing ./apiserver/routers ./database/sql ./config` all pass. |
| `exp/agent3-client` | `7e3cc544` | Opt-in clients: CLI `experimental pool-settings get|patch` (env `GARM_CLI_EXPERIMENTAL=1`, PATCH requires --etag, 412 preserves draft+adopts fresh ETag, ambiguous -> UNKNOWN hold, no retry) and webapp editor (build flag `VITE_GARM_EXPERIMENTAL_POOL_SETTINGS`, same conflict/UNKNOWN semantics, stock modal untouched when off). 9 Go tests + 558 vitest tests pass. |
| `exp/agent5-capacity-hold` | `2971b0b6` | Offline SQLite hold register (`experimental/capacityhold`): pending->unknown->settled, matching reads never clear, NOT_FOUND/delete/DB-flag/unverified-receipt inadmissible, stale receipts rejected+audited, snapshot-rollback detection via out-of-band sequence expectation, `AbsenceEvidence`/`EvidenceVerifier` interface reserved for Agent 4's fencing. Race tests pass. |
| `exp/conditional-management-client` (controller repo, off `c2ee956`) | `da51cd87` + `99a8344` | Opt-in controller-side client (`internal/garmapi`): `ManagementConfig.ExperimentalConditionalSettings` (default off; constructor fails closed with `ErrConditionalManagementDisabled` rather than falling back to the unconditional client). GET/PATCH wire v0 with strong quoted If-Match + optional Idempotency-Key; 412 -> typed `SettingsConflictError` carrying fresh ETag; transport errors, timeouts, 5xx and committed-but-unreadable 200s -> `ErrManagementOutcomeUnknown` (never retried, never an unconditional write); 400/403/404/409/422/428 typed; strict client-side validation rejects malformed input before any dispatch. `99a8344` reconciles the client with the landed server after a client-vs-landed-server loopback pass found two mismatches: replay-of-applied returns a 200 outcome envelope (`{outcome, resulting_etag}`, no settings tuple stored server-side) which the client had failed into UNKNOWN, and id-reuse returns 409 which was unmapped into UNKNOWN — now a typed `ErrConditionalSettingsRequestIDReuse`. 12 test functions over an httptest fixture incl. receipt-style replay and exactly-one-dispatch accounting; gofmt/vet clean; `go test -race ./internal/garmapi/` and full `./...` race suite pass except two pre-existing root-user permission-bit failures in `internal/admission` that fail identically on the unmodified base. Not wired to controller HTTP/UI. |

Flagged differences (prototype facts §3.2 vs landed v0):

1. **Revision scope/mechanism** — prototype: one global epoch/sequence via
   nine triggers over pools/tags/pool_tags; legacy writes to covered tables
   bump it. Landed v0: per-pool side-table counter; legacy stock PUT now
   bumps it in the same transaction (slice 2, `a895ba25`). Remaining
   reconciliation for DEC-2: per-pool versus global order scope (stale ETag
   hazards pool-local vs anywhere), tag-coverage breadth, and
   trigger-maintained versus inline-bump mechanism.
2. **Coverage** — prototype triggers also cover tags; v0 covers only the
   three settings fields through its own writes.
3. **Strictness** — v0 does not enforce JSON field order (documented
   deviation); status set otherwise matches (403 covers both non-admin and
   wrong-entity; prototype's policy refusal maps to the same code).
4. **Auth deviation** — v0 returns 403 for authenticated non-admin callers
   where stock GARM returns 401 (documented; must be reconciled before any
   production claim).
5. **Receipts** — prototype has none; landed v0 has them (slice 2:
   `Idempotency-Key` journal, receipt persisted in the same transaction as
   the effect, replay returns the stored outcome, reuse with different
   payload/scope -> 409; §4.1 DEC-9/DEC-10 decisions applied). The
   controller-side UNKNOWN hold (`ErrManagementOutcomeUnknown`) and the new
   conditional client (`da51cd87`) give the controller its client half; the
   controller journal<->native-API<->ledger binding remains blocked on the
   denied-dispatch scope (Q1/DEC-8).

Proposed integration path (one):

1. **Now (experimental, done):** v0 server + clients + hold register,
   flag/env-gated, nothing stock changed.
2. **Next native slice (still flag-gated) — DONE 2026-11-16 (commit
   `a895ba25`):** (a) legacy-writer participation (stock pool update path
   bumps the revision in the same transaction, closing the stale-ETag hazard
   without triggers); (b) request-correlated receipts per §4.1 (v1 slice
   decisions: reuse-with-different-payload -> 409; no expiration/GC yet;
   writes only). Combined client/server integration merge `a9d7b0a3`
   (agent2 + agent3) verified with full Go race suites and the 558-test
   webapp suite on the merged tree. **Real HTTP end-to-end also verified
   2026-11-16** (server built from the merge tree, temp SQLite, loopback
   only, real `garm-cli` + curl): GET returned settings + ETag; conditional
   PATCH applied and advanced the ETag; stale PATCH -> 412 with draft
   preserved and fresh ETag surfaced; min>max -> 422; a stock
   `PUT /pools/{id}` legacy write advanced the ETag (legacy participation);
   `Idempotency-Key` replay returned the stored outcome with no
   re-application; id reuse with different payload -> 409; unauthenticated
   -> 401; wrong entity -> 403. UNKNOWN-after-loss remains covered by unit
   tests only (no fault injection in this pass). Review bundle for
   independent inspection: `/workspace/garm-native/review-bundle/`
   (`BUNDLE.txt`, `integration.patch` = diff of `15463844..a9d7b0a3`,
   `files.stat`, `SHA256SUMS`; patch SHA-256
   `64a9afa9d7cd83d463623426b456feefeb457913018aa2ca71ca808de94d91cf`).
3. **Reconcile clients** against the landed server per the reviewer's
   mismatch list; re-run both suites. **Controller client done
   2026-11-16** (commit `99a8344`): a loopback pass of the controller
   client against the real landed server (temporary `reale2e`-tagged
   test, since removed; same method as step 2) passed end to end — GET,
   conditional PATCH, receipt replay with no re-application, typed 409
   on id reuse, typed 412 with fresh ETag, typed 422 on min>max, and a
   stock legacy PUT advancing the revision so a stale-ETag patch was
   refused. The pass exposed and fixed the two client-side mismatches
   recorded in the branch table (replay envelope; typed 409). CLI/webapp
   (agent3) were already validated against this server in step 2;
   re-run their suites only if the server tree changes.
4. **Production path (blocked):** only after Q1 checkpoint comparison and
   DEC-8 destination resolution — fold the namespace into the real routes
   behind rollout modes (DEC-6), with entity/controller settings coverage
   (DEC-4) and restore-safe revision authority (DEC-7) designed first.
5. **Retirement track (blocked):** Agent 4 host evidence implements the
   `EvidenceVerifier` class; until then `Release` cannot succeed honestly
   and unknown holds stay charged by design.

### 9a. Independent-review findings and fixes (2026-11-16)

An independent review of the `a9d7b0a3` review branch found four
defects. All are fixed offline by `a1ff1716` (new tip of
`exp/integration-check`; published for review as ref
`review/native-settings-a1ff1716`, full hash
`a1ff171691edf803f4421e4337b33c6dc8dca139`; the reviewed `a9d7b0a3`
ref remains intact). No wire-shape change; semantics now match the
documented contract.

1. **Lost-race partial commit (server, confirmed real):** the pool
   settings UPDATE ran before the conditional revision advance, and the
   stale rejection flowed through the non-fatal path, so the transaction
   committed the rejected request's settings change while leaving the
   revision unadvanced — a silent violation of the stale-write
   invariant. Fixed by advancing the revision first; a race detected at
   advance time commits only the rejection receipt, and a generic
   settings-write failure aborts the transaction (settings and revision
   unchanged).
2. **Ignored legacy revision-bump errors (confirmed real):** the
   best-effort inline bump discarded all errors, so a stock write could
   commit with a stale revision. It now ignores only the absent-side-table
   case (feature disabled); any other error fails the legacy write's
   transaction.
3. **Native CLI/webapp replay + 409 (confirmed):** both clients mapped a
   200 replay envelope and a 409 id-reuse refusal to UNKNOWN. They now
   report the stored outcome with the resulting ETag and classify 409 as
   a definitive rejection, mirroring the controller client's `99a8344`
   fixes. Neither client sends a request id yet; handling is
   forward-compatible.
4. **Concurrency reachability (test gap):** new
   `TestConcurrentConditionalUpdatesExactlyOneApplies` races 8
   conditional updates holding the same observed revision against one
   real SQLite database — exactly one commits, losers are typed stale,
   the revision moves exactly once, the row carries only the winner's
   value, all requests persist receipts. Fails on the pre-fix ordering.

Gates on the fixed tree: gofmt/vet/build clean; `go test -race -tags
testing ./database/sql ./apiserver/routers ./config ./cmd/garm-cli/cmd`
all pass; webapp 562/562 (four new tests) and `npm run build` pass.
Still open per the reviewer: these branches are experimental, not ready
to merge or deploy; DEC-1..8 and Q1/Q7 remain the gating decisions.

### 9b. Request IDs, UNKNOWN gating and client recovery (2026-11-16)

Commit `494899b8` (new review ref `review/native-settings-494899b8`,
full hash `494899b8b77331a7b52e5d7a12c263082dda5f19`; prior refs intact)
implements the no-host-access recovery slice:

- **Request identity everywhere:** the native CLI sends an
  `Idempotency-Key` on every PATCH (operator-supplied `--request-id` or
  auto-generated UUID, printed for correlation); the webapp editor
  generates a per-save UUID. The server already consumed the header.
- **Client-side pending journal:** the exact request (id, scope,
  precondition, payload, time) is persisted before dispatch — CLI in a
  pending file under the user config directory, the editor in
  localStorage — and cleared on every definitive outcome. A browser or
  process restart re-enters the UNKNOWN hold with the operator's draft
  restored; the CLI resolves via `--request-id <printed-id>` re-presentation.
- **UNKNOWN gates writes:** the editor disables Save and the draft
  inputs while unresolved; a re-read no longer clears the state. A
  deliberate "Resolve outcome" action re-presents the journaled request
  so the server's durable receipt (or a definitive applied/conflict/
  rejected answer) settles it. A 412 on re-presentation additionally
  proves the original unknown request never committed (a committed
  request would have been answered by replay).
- **Applied-response validation:** the CLI no longer treats any HTTP 200
  as success — a non-replay 200 must decode as the settings tuple with a
  strong quoted ETag, else UNKNOWN (webapp already enforced this).
- **Dialect-qualified absence detection:** the legacy bump's benign
  check recognizes SQLite ("no such table"), MySQL 1146 and PostgreSQL
  42P01 absent-table forms only, with per-dialect unit tests plus a
  no-schema legacy-write test. Live MySQL/PostgreSQL runs remain
  unqualified (no such databases in this environment).

Gates: gofmt/vet/build clean; race suites for `database/sql`,
`cmd/garm-cli/cmd`, `apiserver/routers`, `config`, `runner` pass;
webapp 563/563 with build. Orchestrator-run, not independently
re-reviewed. The controller editor/journal binding to this API remains
blocked on the denied dispatch-envelope scope (task 1.4/1.5 of the
current plan) — unchanged and not bypassed.

### 9c. Workstream A completion (2026-11-16)

Commit `8407d624` (new review ref `review/native-settings-8407d624`,
full hash `8407d6243401f99c1928529749c730798c9e1159`; all prior refs
intact) completes every host-free item of the reorganised Workstream A,
built by four parallel agents and integrated by the orchestrator:

- Web editor: per-pool pending keys (shared-slot overwrite fixed);
  fail-closed persistence (change NOT sent when the journal cannot be
  written); per-pool multi-tab lock; full-draft restart recovery
  (untouched fields no longer reset); conflict latch after 412 (Save
  disabled until an explicit "Mark reconciled").
- Web client: strict field bounds and same-patch min/max validation
  pre-dispatch; corrupt server documents (bad values, min>max, mixed
  replay/settings keys) fail closed to UNKNOWN.
- CLI: read-before-write (an unresolved journal refuses new writes
  without contacting the server); `patch --resolve` re-presents exactly
  the journaled request as the deliberate UNKNOWN-resolution path;
  atomic journal writes (temp+fsync+rename); journaling failure aborts
  before dispatch.
- Server: install-marker table distinguishes "feature never enabled"
  (benign skip) from "revision table lost after use" (hard error,
  legacy write refused); experimental path reinstalls with a fresh
  epoch after a loss, invalidating outstanding ETags; residual hole
  (marker and table lost together read as never-enabled) documented.
- Loopback integration evidence (real server+CLI binaries from this
  tree, temp SQLite): `--request-id` apply; seeded UNKNOWN refusing a
  new write with the revision unmoved; `--resolve` applying idempotently
  without a receipt; `--resolve` answering from the durable receipt
  with the revision static when a receipt exists.

Field-scope decision (Workstream B input): v0 covers exactly
`priority`, `max_runners`, `min_idle_runners`. Tags, `enabled` and
repository balancing are NOT covered by the conditional path; clients
must keep them on their stock (unconditional) routes and the UI must
say so, or an explicit contract extension must be agreed first
(DEC-4). Silently mapping unsupported fields through the conditional
API is prohibited.

Still open (not host-free): live MySQL/PostgreSQL qualification
(classifier unit-tested only); browser-level and network-kill fault
injection (unit + loopback coverage only); independent re-review of
this slice; the destination/denial-scope decisions.

### 9d. Recovery hardening (2026-11-17)

Commit `c94c563b` (new review ref `review/native-settings-c94c563b`,
full hash `c94c563bdd41151ac61e116439453cbf9dd74228`; all prior refs
intact) closes the second-pass static-review findings — every remaining
host-free recovery defect:

- **Web editor — atomic, owned cross-tab coordination.** The lock
  value is `{tabId, acquiredAt}`; claims are a synchronous
  read-then-write (no await between), which is atomic against other
  tabs; a tab releases only its own lock; a foreign live lock refuses
  the save with an explanatory state and no takeover offer. A stale
  lock (older than 5 minutes) offers an explicit "Take over lock"
  operator action only, and the displaced tab detects the loss via the
  `storage` event and blocks editing (inputs and Save disabled). Two
  tabs on one injected Storage are covered by tests. Journaled records
  now carry `tabId`; a record written by the pre-change code reads as
  corrupt and is surfaced as a recovery error, never silently
  discarded (acceptable for this unreleased flag-gated feature).
- **Web editor — fail-closed corrupt recovery data.** A malformed
  pending record or malformed lock value latches a persistent
  recovery error: Save disabled, operator informed, bytes preserved.
- **Web editor — reconcile gating.** "Mark reconciled" is armed only
  by a successful GET completed after the conflict and re-arms
  against newer reads; the handler re-checks the guard.
- **Web client — strict validation of successful responses**, pinned
  to the landed server wire shapes (verified against
  `database/sql/offline_pool_settings.go`, the apiserver handlers and
  `params/experimental.go`, not invented): strong quoted
  `"<epoch>:<seq>"` ETag (epoch>0, seq>=0); the exact
  `{priority, max_runners, min_idle_runners}` tuple within bounds with
  min<=max; the exact `{outcome, resulting_etag}` replay envelope with
  applied-only outcomes on 200. The wire carries **no request-id echo**,
  so the identity/consistency check the protocol supports is
  `resulting_etag === response ETag header`; mismatch is UNKNOWN.
  A malformed success never updates stored revision or pending state.
- **CLI — cross-process journal safety and exact binding.** All
  journal read-modify-write runs under an exclusive adjacent lock
  (`<journal>.lock`, O_CREATE|O_EXCL, `{pid, acquired_at}`; stale locks
  from dead pids reclaimed once; anything else fails closed), held
  through dispatch on every path including `--resolve`. Each pending
  record binds the active profile name and the effective server base
  URL (which includes `/api/v1`) captured at dispatch; apply and
  resolve refuse a mismatched binding before any network I/O, naming
  both values, with no override flag. Pre-change records (no
  profile/server) are refused on both paths with the manual-recovery
  instruction. A true two-process re-exec test proves exclusion.
- **Server — durable generation and receipts-table-loss detection.**
  The schema-loss hard failure now covers the **receipts** table as
  well as the revision table, on **GET as well as PATCH** — a silent
  recreate would erase the replay record of requests whose responses
  may never have arrived, enabling double-apply. The install marker
  gains `generation` (1 on fresh install, +1 on every explicit
  reinstall) and `last_epoch`: revision epochs are minted as
  `max(unix_now, last_epoch+1)` with the floor persisted in the same
  transaction, so no `"<epoch>:<seq>"` pair from a lost or restored
  generation can ever match a post-reinstall revision row. Table
  creation runs only while the marker is absent; recovery is the
  explicit `ReinstallOfflinePoolSettingsSchema` store primitive
  (generation+1, revision and receipt rows wiped, epoch floor
  preserved), deliberately not reachable from request paths. A local
  epoch is still **not** an anti-rollback authority: a full-file
  restore rolls epoch/sequence back, and the documented recovery is
  operator-driven reinstall plus client UNKNOWN→fresh-read; requests
  whose receipts were lost stay UNKNOWN until readback plus an
  explicit operator decision. Residual hole unchanged: if the marker
  is lost together with the tables, the next write reads as
  never-enabled.
- **Commit-then-drop-response coverage.** New handler-level test
  (`apiserver/routers/offline_pool_settings_drop_response_test.go`)
  drives a PATCH whose transaction commits and whose response then
  dies: the settings change and receipt are durable, the retry is
  answered from the stored receipt (200 + envelope, static revision),
  and repeated replays neither advance the revision nor write a second
  receipt.

Gates (orchestrator-run on the final tree): gofmt/vet clean; race
suites green for `database/sql` (84s), `apiserver/routers`,
`config`, `cmd/garm-cli/cmd`; webapp 590/590 with `svelte-check`
clean and `npm run build` clean. Loopback against real server+CLI
binaries built from this tree (fresh SQLite env, bootstrap via real
HTTP): 11/11 — settings GET with ETag, conditional apply with quoted
`If-Match` advancing the revision, journal cleared on definitive
outcome, live-pid lock refusal naming the holder, server/profile
binding refusal before network on both apply and resolve, `--resolve`
applying the journaled request idempotently, and the durable receipt
refusing id-reuse with a different payload with no double-apply.

Still open (not host-free): live MySQL/PostgreSQL qualification
(classifier unit-tested only); browser-level and network-kill fault
injection; independent re-review of `c94c563b`; destination and
denial-scope decisions (Q1/Q7, DEC-1..8). The controller
editor/journal binding remains blocked on the denied
dispatch-envelope scope — unchanged and not bypassed.

## 10. Verification expectations (unchanged from revision 1)

Repo gates per agent before handoff: `go test -race -count=1 ./...`,
`go vet ./...`, clean `gofmt -l .`; web `npm ci && npm test && npm run
build`; pinned-GARM opt-in gates for touched integration paths. Docs-only
changes run no Go gates. The hold in the header overrides all Agent 2/3
implementation until CJ lifts it.
