# Continuous cross-repository allocator and verified placement

**Plan 01 · Draft engineering handoff · Code and offline acceptance only**

Starting reference: [`65b7a33`](https://github.com/ShortCipher5/garm-capacity-controller/commit/65b7a33a1b0f00bc2c839e33c669b229d6660d4b). Recheck current `main` before implementing.

## Outcome
Use regular stock GARM repository pools on a shared fleet. When several repositories have queued jobs, award only capacity that fits an eligible host and the requested runner profile. A quiet pool may target zero idle or its configured floor. GitHub assigns jobs and stock GARM creates and retires runners. The controller decides how much capacity each managed pool should request; it is not a queue proxy.

## Start here
Reuse `internal/capacitybroker/{planner,job_plan}.go`, `internal/brokerinput/{stock_scaling_linux,stock_routed_preview_linux}.go` and the existing discovery/configuration UI and policy model. At the pinned baseline, previews are not a continuous production allocator. [Draft #378](https://github.com/ShortCipher5/garm-capacity-controller/pull/378) adds a source-stamp guard; recheck its state and use the merged version if present. Do not rewrite working policy or onboarding code.

## Implement
1. Assemble normalized allocator inputs from existing authorized observations: stock source and pool, exact provider destination where established, configuration revision, freshness, actual host capacities, configured CPU/RAM/disk headroom, runner-count caps and budgeted runner profile. Keep discovered capacity, user-configured budgets, measured usage and enforceable provider limits distinct. Preserve light/medium/heavy and custom sizes; guard units, overflow and over-allocation. A disk margin warning is not a per-runner disk quota; no ZFS requirement.
2. Wire periodic, bounded reconciliation in an observe-only mode first. Consider exact demand and retained physical charges, pool maximum and minimum idle together; default weights equal, persist documented per-repository weights, and use deterministic tie-breaking. A stale, unreachable, paused or ambiguous host may not fund an increase. An eligible host list does not establish where stock GARM will create a runner.
3. Reuse existing atomic capacity accounting where it fits. Persist outstanding commitments before awarding more capacity, charge busy/creating/unresolved runners, and make restart accounting idempotent. No scale-up may be funded by a proposed reduction. Keep demand signals separate from actual charges.
4. Produce a bounded, explainable proposal: selected pools/destinations, risks, input version, held capacity and reason codes. Do not send writes from this PR. Connect actual applied/unknown status only through Plan 02; release charges only through Plan 03.

## Code acceptance
- Two repositories competing for one remaining slot cannot both get it, even across overlapping ticks or restart. At equal weights sustained demand receives fair service; configurable weights change preference without bypassing hard caps.
- Wrong-source, stale or conflicting observations and a provider destination/profile that cannot be established do not award growth. Mixed runner sizes fit CPU/RAM/disk/runner budgets independently.
- Zero-idle and configured warm floors behave predictably. Growth never treats a lower maximum, unknown state, or missing observation as free capacity.
- Each reason and capacity decision is inspectable in fixtures and operator output. Existing GARM lifecycle behavior is unchanged.

## Dependency boundary
Can be implemented and reviewed with repository fixtures only. Claim physical enforcement only when supported stock provider/project/profile observations establish actual placement and limits. If the stock path cannot guarantee the destination, keep it unqualified for automatic growth. No server access, host reconfiguration or production enablement is part of this ticket.

## Handoff and definition of done
This draft contains the plan only. Take over this branch or split implementation into small reviewable commits/PRs referencing it. Identify the exact baseline and behavior already present, ship only the delta, and include offline automated regressions for the acceptance cases. Keep flags off where capabilities are unavailable, document rollback of the code change, and report outstanding dependencies explicitly. No live server changes, new access grants, production rollout, or operational rehearsal are assigned. See [original scope](https://github.com/ShortCipher5/garm-capacity-controller/issues/53) and [CI Health scope](https://github.com/ShortCipher5/garm-capacity-controller/issues/268); this plan and current stock-first code take precedence over older experimental notes.
