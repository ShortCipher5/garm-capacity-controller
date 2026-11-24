# Verified runner teardown and safe capacity reuse

**Plan 03 · Offline evidence contract only · Production capacity reuse deferred**

Starting reference: [`65b7a33`](https://github.com/ShortCipher5/garm-capacity-controller/commit/65b7a33a1b0f00bc2c839e33c669b229d6660d4b). Recheck current `main` before implementing.

## What this change delivers

The standalone Go fixtures under `internal/reuse` exercise candidate identity matching, hold reasons, and both orders of incomplete compute/storage evidence. They are test files only: candidate dimensions are not a release receipt or trustworthy physical evidence. Project-only absence, even with matching reads, does not establish an atomic boundary or prove that an exclusive fence was held. Fixture cleanup/capability records are not proof about an installed provider or an original volume.

This change adds **no** runtime verifier, route, UI, mutation, accounting adjustment, migration, reconciliation change, or new release path. Existing charges, retirement guards, and historic status semantics remain unchanged. The ledger remains on schema 29; there is no new startup data migration or downgrade procedure. This is not permission to use a previously upgraded experimental ledger with schema 29 code, change its version marker, or restore stale allocation authority.

## Intended future outcome
Return capacity to the fleet only after the particular runner and its applicable allocation have ended. A busy, possibly live or unproven runner must remain charged. Stock GARM retains ownership of ephemeral runner lifecycle; the controller observes and accounts rather than adding a competing delete path.

## Start here
Read `docs/{physical-retirement-proof-boundary,retirement-safety-hold}.md`. Reuse qualified parts of `internal/incusapi/{retirement_observation,terminal_evidence}.go`, `internal/demandrepair/job_lifecycle.go` and the existing hold/accounting model. Read-only observations and an adapter tied to another reservation or version must not be presented as equivalent stock teardown proof.

## Future implementation requirements (not shipped here)
1. Define/verifiably consume terminal evidence for the original charge: exact stock source, pool and runner registration/row; authenticated provider allocation; enrolled daemon/project/member and immutable instance incarnation; applicable exact volumes or other charged allocation. Account for a replacement with a reused display name as a distinct charge.
2. Use positive, scoped lifecycle/workload evidence and exact cleanup state. Do not infer no assignment, stopped state or teardown from timeout, delete acknowledgement, a depleted page, reduced pool limit, a missing row, or absence under an unverified identity. Where the supported provider proves that an instance and its storage are inseparable, record that audited capability rather than assuming it for all providers.
3. Integrate observe-only accounting so held results and their reason are visible. Handle duplicate, reordered, partial, conflicting and delayed evidence deterministically. Once qualified evidence exists, release only the matching charge once, atomically with the existing capacity state. No release upon unknown or after scope/profile/authority changes without valid evidence.
4. Keep restart and policy change behavior conservative. Separate CPU/RAM/count from residual disk footprint where a provider can verify only some resources; never free an unverified resource dimension by convenience. Operator UI shows pending cleanup vs verified reusable without requiring direct deletion.

## Future runtime acceptance (not established by fixtures)
- Same-name replacement, delayed teardown, residual applicable volume and a still-assigned runner all remain charged. Scoped positive matching terminal proof releases once, even on duplicate delivery or restart.
- An incomplete inventory or failed read cannot convert an unknown into free capacity. No reduction alone funds growth.
- Local fixtures cover released and retained cases with two competing repos; code does not touch hosts or add runner deletion.

## Deferred production work

Keep the existing hold when stock/provider proof is unavailable. Production reuse requires independently recorded original stock registration and volume identity, supported evidence of cleanup of that exact allocation, and a terminal operation matched to the original durable retirement intent and its required safety fences. None of those facts may be replaced by compatible field shapes, a missing row, a non-atomic absence read, or a synthetic positive.

Durable partial replay and supplemental settlement are deferred. A future implementation must cover both proof orders with synthetic SQLite close/reopen tests, comparing the exact remaining footprint in the reply, status and accounting. Repeated or unqualified evidence must never change a charge; qualified later evidence, if separately supported, must credit only still-held dimensions once without losing the lifecycle linkage. Any future schema change also requires explicit state-aware rollout and rollback acceptance preserving committed charges, pending retirement and held dimensions.

No host access, new permissions, live changes, deployment, or operational rehearsal are part of this handoff. See the [original scope](https://github.com/ShortCipher5/garm-capacity-controller/issues/53) and [CI Health scope](https://github.com/ShortCipher5/garm-capacity-controller/issues/268).
