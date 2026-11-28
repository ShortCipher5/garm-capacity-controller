# Manage named shared-capacity server groups

Status: implementation submitted for review; not deployed or qualified on live infrastructure.

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

## Implemented on this branch

The management UI's **Repository capacity policy → Shared server groups** section edits a local SQLite policy (not stock GARM pool settings). A group has a stable UUID, a display name, disjoint host membership, entire repositories and/or explicit member pools, and nonnegative CPU (millicores), RAM (MiB) and slot ceilings. Zero blocks new growth. Review draft changes, save with the separate management token, then reload to verify. Saved charge/limit totals are historical obligations, not measured utilization; global, host and pool budgets still bind.

Both the allocator and the atomic ledger consult this policy. The group budget counts physical host subtotals once, including outstanding grants, unresolved inventory and retained overhead. Direct reservations, placement and grant creation check the group; redemption substitutes for the existing charge. Rejecting a group limit does not evict an existing runner. A membership edit requires fresh complete ledger observations and zero retained charge on every affected host/pool; reducing a ceiling does not release anything. An old client omitting groups preserves the current partition; an explicit empty list proposes to remove it, subject to those same checks. Legacy direct stock-GARM writes outside this ledger are not covered.

Upgrade order: back up the ledger using its supported SQLite backup, stop existing writers, then upgrade all ledger readers/writers to the same version. This branch allocates schema 32 on main 53eda6b; coordinate with other pending migrations at merge time. Older binaries must not be deployed over the upgraded database.

No production route, live server, GitHub installation, or external provider is changed by this branch. Separate shared-capacity deployment qualification is still required.
