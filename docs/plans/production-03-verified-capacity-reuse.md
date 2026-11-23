# Verified runner teardown and safe capacity reuse

**Plan 03 · Draft engineering handoff · Code and offline acceptance only**

Starting reference: [`65b7a33`](https://github.com/ShortCipher5/garm-capacity-controller/commit/65b7a33a1b0f00bc2c839e33c669b229d6660d4b). Recheck current `main` before implementing.

## Outcome
Return capacity to the fleet only after the particular runner and its applicable allocation have ended. A busy, possibly live or unproven runner must remain charged. Stock GARM retains ownership of ephemeral runner lifecycle; the controller observes and accounts rather than adding a competing delete path.

## Start here
Read `docs/{physical-retirement-proof-boundary,retirement-safety-hold}.md`. Reuse qualified parts of `internal/incusapi/{retirement_observation,terminal_evidence}.go`, `internal/demandrepair/job_lifecycle.go` and the existing hold/accounting model. Read-only observations and an adapter tied to another reservation or version must not be presented as equivalent stock teardown proof.

## Implement
1. Define/verifiably consume terminal evidence for the original charge: exact stock source, pool and runner registration/row; authenticated provider allocation; enrolled daemon/project/member and immutable instance incarnation; applicable exact volumes or other charged allocation. Account for a replacement with a reused display name as a distinct charge.
2. Use positive, scoped lifecycle/workload evidence and exact cleanup state. Do not infer no assignment, stopped state or teardown from timeout, delete acknowledgement, a depleted page, reduced pool limit, a missing row, or absence under an unverified identity. Where the supported provider proves that an instance and its storage are inseparable, record that audited capability rather than assuming it for all providers.
3. Integrate observe-only accounting so held results and their reason are visible. Handle duplicate, reordered, partial, conflicting and delayed evidence deterministically. Once qualified evidence exists, release only the matching charge once, atomically with the existing capacity state. No release upon unknown or after scope/profile/authority changes without valid evidence.
4. Keep restart and policy change behavior conservative. Separate CPU/RAM/count from residual disk footprint where a provider can verify only some resources; never free an unverified resource dimension by convenience. Operator UI shows pending cleanup vs verified reusable without requiring direct deletion.

## Code acceptance
- Same-name replacement, delayed teardown, residual applicable volume and a still-assigned runner all remain charged. Scoped positive matching terminal proof releases once, even on duplicate delivery or restart.
- An incomplete inventory or failed read cannot convert an unknown into free capacity. No reduction alone funds growth.
- Local fixtures cover released and retained cases with two competing repos; code does not touch hosts or add runner deletion.

## Dependency boundary
Assign evidence types, validators, accounting and fixture-backed integration now. The live verifier depends on Plan 01's trusted placement identity and a supported original stock provider allocation/cleanup observation. Where those facts are unavailable, the result must remain held; no server access or manual data cleanup is a developer deliverable.

## Handoff and definition of done
This draft contains the plan only. Take over this branch or split implementation into small reviewable commits/PRs referencing it. Identify the exact baseline and behavior already present, ship only the delta, and include offline automated regressions for the acceptance cases. Keep flags off where capabilities are unavailable, document rollback of the code change, and report outstanding dependencies explicitly. No live server changes, new access grants, production rollout, or operational rehearsal are assigned. See [original scope](https://github.com/ShortCipher5/garm-capacity-controller/issues/53) and [CI Health scope](https://github.com/ShortCipher5/garm-capacity-controller/issues/268); this plan and current stock-first code take precedence over older experimental notes.
