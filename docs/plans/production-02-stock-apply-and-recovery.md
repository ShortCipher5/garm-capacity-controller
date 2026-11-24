# Safe stock GARM pool changes and interrupted update recovery

**Plan 02 · Draft engineering handoff · Code and offline acceptance only**

Starting reference: [`65b7a33`](https://github.com/ShortCipher5/garm-capacity-controller/commit/65b7a33a1b0f00bc2c839e33c669b229d6660d4b). Recheck current `main` before implementing.

## Outcome
The controller UI is the normal control surface for enrolled pools. Stock GARM remains unmodified and handles runner lifecycle. Direct/native editing is a maintenance exception: pause controller changes and resolve outstanding uncertainty first. This ticket does not assume exclusive write authority merely because the UI can propose settings.

## Start here
Read `docs/stock-scaling-release-boundary.md` and reuse the existing management model, read-only snapshots, change planning and inverse advice in `internal/garmmanagement` and `internal/brokerinput/{stock_change_plan_linux,stock_inverse_advice_linux}.go`. Stock v0.2.1 pool readback gives the current value, not proof that a timed-out write was ours or that it cannot still settle.

## Implement the code-only portion
1. Complete the inactive-by-default controller state and operator views: proposed, paused/maintenance, unavailable, applied when proven, and outcome unknown. Show why a particular action is disabled and what is still charged. Persist restrictive state across restart. A pause takes effect for new work and must never silently clear pending/unknown state.
2. Define supported capability inputs and explicit refusal reasons for missing external ownership or completion proof. Keep the stock path observe-only unless a supported, independently verified upstream interface satisfies the requirements below. Do not add any replacement dispatch protocol, broaden trust or permissions, or make an ordinary stock PUT plus GET appear conditional or authoritative.
3. Define what any future compatible adapter must establish: an exclusive authorized change boundary; an independently authoritative result after interruption; prevention of late-writer races; drift handling; and a safe rollback only after the original outcome and current state are known. Separate verified settings from physical allocation/teardown. An unsupported adapter must return unavailable, not approximate these semantics.
4. Only after a supported stock interface has been established, implement the narrowly scoped adapter under a separately reviewed follow-on to this plan. It may manage an enrolled ordinary pool's bounded capacity settings only, honoring Plan 01 constraints. No native GARM or provider replacement.

## Code acceptance
- An interrupted or ambiguous change cannot become applied or release capacity simply because a read matches either the old or new value. Stale data, restart, manual drift, late completion, and unavailable capability all prevent unsafe apply/resume.
- No action is enabled in read-only/paused state, and the UI commands never imply a production permission grant. Unsupported combinations have a visible reason and stay off by default.
- Tests can demonstrate refusal and conservative state handling offline. Do not report end-to-end stock writes as implemented unless a supported adapter meets every requirement, including observable settlement and rollback.

## Dependency boundary
The fail-closed software, UI and capability checks are assignable now. The pinned stock interface does not currently provide the evidence needed to promise unattended write/recovery. Do not require the developer to rewire servers, secure new access, or make a fork. Deliver an observe-only implementation plus precise compatibility gap if no supported upstream path exists. Begin alongside Plan 01; enabling an apply path is not in the offline definition of done.

## Handoff and definition of done
This draft contains the plan only. Take over this branch or split implementation into small reviewable commits/PRs referencing it. Identify the exact baseline and behavior already present, ship only the delta, and include offline automated regressions for the acceptance cases. Keep flags off where capabilities are unavailable, document rollback of the code change, and report outstanding dependencies explicitly. No live server changes, new access grants, production rollout, or operational rehearsal are assigned. See [original scope](https://github.com/ShortCipher5/garm-capacity-controller/issues/53) and [CI Health scope](https://github.com/ShortCipher5/garm-capacity-controller/issues/268); this plan and current stock-first code take precedence over older experimental notes.
