# Manage named shared-capacity server groups

Status: draft implementation plan. This document is not an implemented or activated feature.

## Scope

Reuse canonical global/host/pool budget and policy editing (#23, #128, #134) and the shared-capacity handoff (#394). Add named groups as a real operator workflow, not a second capacity ledger.

## Requirements

- Create and edit groups, eligible host and repo/pool membership, and runner-slot/CPU/RAM ceilings. Version decisions and show pending changes, effective constraints and charged usage distinctly.
- Compile group settings into the existing policy and shared-cap path. Do not double-count any host across groups or treat a membership edit as released capacity. Prefer nonoverlapping host membership initially unless canonical reservations safely support overlap.
- Refuse unsafe moves/deletion while charges or unresolved operations remain, or preserve those charges explicitly; revalidate policy and current membership at the transaction boundary.
- Show inactive or preview-only status until actual group enforcement is qualified. Do not configure groups by changing unrelated stock GARM pools.

## Acceptance

Fixtures cover two groups with multiple hosts/repos, conflicting membership, active charges, stale edit, restart, shared cap under concurrency and readable UI for pending vs effective policy.

## Estimate and release boundary

2–4 engineer-days if the shared-cap allocator contract is available. Engineering estimate excludes rollout and access lead time. Build against disposable fixtures; no live infrastructure, GitHub installation, or production mutations are needed for this PR. Keep stock GARM and existing queue/runner ownership. Mark any unimplemented or unsupported path plainly; do not mistake a fixture or plan for active capability.
