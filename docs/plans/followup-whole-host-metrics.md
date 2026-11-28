# Show live whole-server CPU, RAM and storage metrics

Status: implemented for SSH-target OS CPU, RAM and root storage; not deployed. See scope limit below.

## Scope

Reuse authenticated host access and existing observation dashboard (#105, #428). Show actual whole-host metrics including non-runner load, distinctly from per-guest Incus metrics.

## Requirements

- Read a fixed, authenticated and bounded host metrics contract using supported scoped access; no user-supplied command or general terminal capability. Poll at bounded cadence with per-target isolation, timeouts and known identity.
- Distinguish host CPU utilization, RAM total/available from live absolute readings, and root filesystem versus applicable runner storage-pool headroom. CPU rates require two compatible observations; reset/first sample have no inferred rate.
- Include source timestamp and explicit unavailable/partial/stale status. Expose consistent units and no silent zero when evidence is absent. Keep this an observation path, not standalone scaling authority.

## Acceptance

Synthetic two-host, first sample, reset, partial storage coverage, stale clock/data, host key mismatch and timeout; verify healthy host still renders and guest versus host values are unambiguous.

## Estimate and release boundary

2–4 engineer-days assuming the approved host access path is reusable. Engineering estimate excludes rollout and access lead time. Build against disposable fixtures; no live infrastructure, GitHub installation, or production mutations are needed for this PR. Keep stock GARM and existing queue/runner ownership. Mark any unimplemented or unsupported path plainly; do not mistake a fixture or plan for active capability.

## Implementation in this PR

Implemented the protected observer API, embedded dashboard, bounded in-memory polling and versioned forced-command SSH host probe. See [configuration and metric semantics](../observer-dashboard.md#whole-server-headroom-optional-independent-of-guest-telemetry). Root space is observed; Incus runner pool space is deliberately unavailable until an exact, separately reviewed binding exists. Neither root nor guest data is substituted. No live hosts have been touched.
