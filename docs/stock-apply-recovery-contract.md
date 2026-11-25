# Stock settings apply: recovery contract and compatibility gap

**Code and offline acceptance only.** This document is the precise gap
analysis required by plan 02 in place of a stock write adapter. It defines
what any future compatible adapter must establish, shows why the pinned stock
GARM v0.2.1 interface cannot satisfy it, and points at the fail-closed
implementation that enforces the boundary today. It authorizes no live server
change, new access grant, or unattended write.

## What exists now

`internal/brokerinput/stock_apply_state_linux.go` implements a durable,
inactive-by-default controller state per reviewed scope:

- Operator-facing states: `unavailable` (the default), `proposed`,
  `paused_maintenance`, `applied_when_proven`, and `outcome_unknown`.
- The operator view reports why each command is disabled and what is still
  charged. Mutating Garm commands (`apply`, `resolve_outcome`, `rollback`)
  are never enabled on this path; a matching old or new settings readback is
  recorded as an observation only and can never complete, apply, or release
  an operation.
- Pause and resume are controller-local maintenance controls. Pause blocks
  new work and persists across restart; resume never resolves, clears, or
  reclassifies a pending or unknown operation.
- Restrictive state lives in an owner-private SQLite journal (lineage-pinned,
  instance-bound, single-writer, one BEGIN IMMEDIATE transaction per mutation
  plus audit). Restart presents an in-flight write as `outcome_unknown` and
  holds all charges.
- The only exit from `outcome_unknown` is `RecordProvenOutcome` with a
  supported capability claim and a complete proof bound to the exact
  operation. No stock path can produce either today, so the exit is
  unreachable in production.

`internal/brokerinput/stock_apply_capability_linux.go` defines the
capability inputs and the adapter contract assessment. The qualified
interface registry is empty on purpose: every production claim fails closed.

## What the authenticated dashboard shows

The repository policy area explains that stock settings apply and its
operation status are unavailable here. It does not poll a status route,
open a journal, or derive charge, pause, or readiness from another
dashboard endpoint. A missing operation charge is explicitly unknown, not
zero; if an offline view has a recorded charge, it labels that value as
last recorded, not current capacity. The dashboard offers no write controls.

### Reading a scoped recovery view

The bound controller's read-only `View` takes the reviewed scope and its
settings evaluation. The optional `readback` contains the measured maximum
and the caller's freshness assessment for that capture. A missing capture
omits the field. Earlier projections without the field remain readable.
The proposed old and desired maxima remain labeled as proposal history.

| Operator field | Evidence and interpretation |
| --- | --- |
| Local admission pause | The mode row for the reviewed scope records whether admission is paused. Lifting the pause keeps the existing operation and charge. |
| Managed listener drain | The stock-apply journal has no witness for listener drain. Only the protected Service owner observes successful suspension after the listener closes, entered handlers finish and the old session is revoked. Existing idle runners can still accept jobs. |
| Exact upstream operation outcome | A dispatched or uncertain operation remains unresolved under either pause state. An unsubmitted proposal has no recorded submission. A historical proven outcome applies only to its specific operation. |
| Observed pool settings | The read-only capture can report a fresh or stale value, including a match with the old or desired maximum. The operation still needs its own authoritative result. |

After a lost pause acknowledgment, inspect with `RecoverScope` before
deciding whether to request a stop again. Reissuing the request after a
resume starts a new local pause. A pending upstream operation continues to
block new admission, apply and rollback. For stock v0.2.1, exact completion
after interruption, continued exclusion of late writers, and exclusive
ownership of the controlled fields remain unproved.

## What the durable SQLite and lineage checks establish — and what they do not

The stock-apply journal reuses the management journal's local durability
model. Its checks are LOCAL guarantees about this controller's state file;
they say nothing about the upstream Garm service.

Established, locally:

- **Owner-private storage.** The journal is created exactly once with mode
  0600 inside a 0700 directory owned by the invoking uid; group/other-writable
  parents and non-regular files are refused before any write. Initialization
  never adopts an existing file, even an empty one, or one with WAL/SHM
  sidecars already present.
- **Atomic, serialized mutation.** One connection, WAL, `synchronous=FULL`,
  busy timeout, and every state transition plus its audit row committed in a
  single `BEGIN IMMEDIATE` transaction. A crash mid-transition cannot
  half-apply a change, and concurrent local writers serialize on the gate.
- **Schema and identity pinning.** A fixed `application_id`/`user_version`,
  the exact column set, and a 64-hex random lineage recorded at creation and
  supplied from trusted configuration at open. A swapped, recreated, legacy,
  or foreign file fails closed before any mutation; a different lineage is
  rejected. After first use the journal is additionally bound to one
  controller instance identity, so a second instance cannot silently adopt
  the restrictive state.
- **One open operation per scope.** A partial unique index enforces at most
  one unresolved operation per reviewed scope, and drift flags, pause flags,
  charged baselines and the audit trail persist across restart exactly as
  recorded.

Not established — by design and by limitation:

- **Nothing upstream.** The checks prove nothing about write outcomes,
  completion, exclusive settings ownership, or fencing of native Garm UI/CLI,
  direct database sessions, delayed requests, or other controllers. A local
  journal cannot supply the adapter requirements above.
- **Lineage match is not recency.** A same-lineage raw historical copy could
  lack the newest committed rows; matching lineage detects a *different*
  journal, not a stale replay of the same one. No supported operation
  activates such a copy.
- **No anti-rollback proof against the owner.** A privileged local owner who
  substitutes an older same-lineage file or edits rows directly is not
  detected; these checks resist accident and casual tampering, not the owner.
- **Stored charging is faithfully persisted caller accounting.** The journal
  stores the caller-supplied current ledger-charged baseline durably — it
  does not verify it against physical allocation, and it never converts a
  settings observation into a capacity release.

## Capability inputs and refusal reasons

`StockApplyCapabilityClaim` presents the verified evidence an upstream
interface must supply; `AssessStockApplyCapability` returns explicit
refusals, never caller-trusted booleans:

| Refusal code | Meaning |
| --- | --- |
| `interface_unsupported` | Empty or unknown interface kind. Unknown kinds fail closed. |
| `stock_garm_v0_2_1_observe_only` | Stock GARM v0.2.1 cannot satisfy the write contract; returned even when other evidence fields are populated, so an ordinary stock PUT plus GET never appears conditional or authoritative. |
| `external_ownership_unproven` | No verified evidence of exclusive ownership of the managed settings for the exact enrolled scope. |
| `completion_proof_unavailable` | No verified evidence of an independently authoritative completion result after interruption. |
| `late_writer_fence_unproven` | No verified evidence that delayed or concurrent writers cannot still settle. |

## What a future compatible adapter must establish

`AssessStockApplyAdapter` evaluates a candidate adapter against
`StockApplyAdapterRequirement`. An adapter is `available` only with a
qualified interface, verified settings only (`SettingsOnly`), and every
requirement carrying non-zero evidence digests:

1. `exclusive_authorized_change_boundary` — prove and hold exclusive,
   authorized write access to the managed pool settings for the exact scope,
   fencing every other writer: native Garm UI/CLI, another controller,
   queued or retried requests, and direct database access. A configured
   fingerprint check is not this proof.
2. `independently_authoritative_result_after_interruption` — after any
   timeout, cancellation or restart, return the authoritative outcome of the
   exact original request from a durable receipt, not a readback comparison.
   Old settings still visible, desired settings visible, and elapsed time
   are all explicitly not completion evidence (see
   `docs/garm-management-recovery.md`).
3. `late_writer_race_prevention` — prove no delayed or concurrent writer can
   commit after an outcome is recorded. Token revocation, pausing admission,
   stopping the controller, or one process exiting does not fence admitted
   work.
4. `drift_handling` — detect external settings drift and stay blocked
   rather than apply or resume over changed state; recovery requires fresh
   evidence and explicit review, never a timer, restart, or matching GET.
5. `safe_rollback_after_known_original_outcome_and_current_state` —
   rollback only after the original outcome and the current state are both
   independently known. A previously observed maximum is history, not a safe
   rollback target; an inverse change must be re-evaluated as a fresh,
   validated plan under current constraints (the existing inverse-advice
   validator already enforces this for proposals).

Verified settings stay separated from physical allocation and teardown: the
adapter may manage an enrolled ordinary pool's bounded capacity settings
only. Runner lifecycle, placement, drain, and retirement remain with stock
GARM and the existing admission boundaries, and a settings change is never
accounted as released capacity.

An unsupported adapter returns `unavailable` and never approximates these
semantics. The built-in `StockGarmAdapterClaim` always assesses as
`unavailable` with the observe-only refusal, one unestablished-requirement
refusal per requirement, and the physical-scope refusal.

## Why the pinned stock interface cannot be adapted today

Stock GARM v0.2.1 has no controller-specific pool-write role and no atomic
conditional update (`docs/stock-settings-ownership.md`). Concretely, against
the five requirements:

- No exclusive boundary: any writer with the pool-write credential can edit
  settings at any time; the controller cannot fence native UI/CLI, direct
  database sessions, or queued requests.
- No authoritative receipt: the only read is a GET of current values. After
  a timed-out PUT, a matching GET neither proves the write was ours nor that
  it cannot still settle (`docs/garm-management-recovery.md` documents the
  pinned handler/transaction semantics).
- No late-writer fence: admitted requests are not bound to the caller's
  context, so cancellation cannot be used as a completion fence.
- Drift indistinguishable from completion: an external edit and our own
  delayed write produce the same observable state.
- No safe rollback anchor: with neither exclusive ownership nor receipts,
  restoring a historical value is a guess that can overwrite another
  writer's change.

Until an interface is independently qualified for the installed stack, the
compatibility gap stands: the controller proposes, pauses, observes, and
preserves uncertainty durably — it does not write.
