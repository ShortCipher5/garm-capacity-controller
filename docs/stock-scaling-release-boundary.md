# Stock GARM scaling release boundary

GitHub owns job queuing. Stock GARM owns runner creation, assignment and
retirement. This controller compares demand and capacity across pools. The
current optional maximum preview is advice, not a change to GARM or a fleet
quota. No fork or host filesystem choice is required by the preview.

## Requirements for a change candidate

1. Prove which physical host the configured stock provider can actually use
   for the exact pool. A configured set of eligible hosts is not that proof.
   If host A is full and the provider routes to A, spare capacity on host B
   cannot justify growth unless the provider can demonstrably route there.
2. Evaluate CPU, memory and disk against that host and the effective runner
   size, with operator headroom intact. A low pressure sample is a veto
   input, not proof another runner fits. Missing or stale evidence must not
   justify growth. Continue charging unknown resources.
3. Require two independent, fresh observations of the same source, scope
   and policy. Confirm one bounded step, with equal weighting by default.
   Pause on drift, conflicts or unconfirmed outcomes. A scale-down must
   preserve configured idle floors and must not evict a busy runner.
4. Treat a previously observed maximum as history, not a safe rollback
   target. Re-evaluate any inverse change with current evidence and bounds.
   After a restart, do not infer that an uncertain operation succeeded merely
   because the current setting happens to equal its target.

The first code slices validate proposal inputs and expose reasons when the
controller cannot trust a recommendation. They do not create a writer,
enforce a physical quota, or make the preview safe to apply. Activation
requires a separately qualified bounded stock API path and rollout; it must
not be inferred from previews, local checks, or a successful image build.

## Pool settings ownership and maintenance

**Agreed operating model, not implemented or enabled.** The controller UI is
the normal entry point for edits to autoscaled pool settings. Stock GARM
remains responsible for its runner lifecycle; operators use its UI or CLI for
observation outside maintenance. The preview stays read-only. Stock GARM
v0.2.1 has neither a controller-specific pool-write role nor an atomic
conditional update; a local acknowledgement or matching readback cannot
establish exclusive ownership.

- **Normal operation:** Write readiness would require verified sole ownership
  of managed settings, a healthy durable journal with no unresolved writes,
  and fresh, matching scope and capacity evidence. If any prerequisite is
  missing, stale or contradictory, stay read-only. A settings change is not
  proof of released capacity.
- **Maintenance entry:** In a separately authorized window, stop controller
  settings writes and confirm the controller writer and accepted requests can
  no longer commit. Resolve every pending or unknown outcome under the
  [external completion fence](garm-management-recovery.md) before permitting
  direct stock edits. Stopping the controller, a timeout, and an elapsed
  maintenance window are not completion evidence.
- **Emergency and re-entry:** If the completion fence or outcome cannot be
  established, keep both sides closed to settings writes and preserve the
  journal. Do not retry, restore a prior setting by guess, or replace history.
  After maintenance, end direct stock write access, confirm outstanding
  requests have settled, and re-establish exclusive controller ownership.
  Review authoritative settings against saved policy and collect fresh
  evidence before an explicit return to normal operation. No automatic
  re-entry on a timer, restart, or matching GET.

Acceptance for future implementation: demonstrate exclusive ownership in
normal and maintenance modes, prove no overlap across handoff or restart,
show that delayed or unknown writes leave both sides blocked, and verify
that re-entry requires fresh evidence and explicit review. These checks are
not authorization to change an installed service.

## Incus provider qualification checkpoint

An unmodified upstream candidate was built twice offline from vendored
dependencies: [provider source at `d3cc787b`](https://github.com/cloudbase/garm-provider-incus/tree/d3cc787b39365df446f67f2b6e1e68a257ea29ab),
Go 1.26.10, Linux amd64 (v1), CGO disabled, Incus client v7.5.1 and
provider-common v0.1.9. The two local executables had the same SHA-256:
`a5f5edab2c207a69ce37091702931455a0a588265116f1676fe92cba1846c256`.
This verifies the candidate build, not any installed executable or live route.

Static review did not qualify the candidate for strict routing. Its
[vendored HTTPS transport](https://github.com/cloudbase/garm-provider-incus/blob/d3cc787b39365df446f67f2b6e1e68a257ea29ab/vendor/github.com/lxc/incus/v7/client/util.go#L24-L158)
accepts environment proxies and follows redirects. Clearing proxy variables
does not stop redirects. Before counting that provider as verified, require a
reviewed strict transport option or independently verified controls that bind
it to the intended Incus daemon and project. Do not treat a successful build,
configured endpoint, or apparent project match as sufficient for writes.

### Preserve provider routes through cutover and rollback

Pinned source analysis, not evidence about the installed provider binary.
For ordinary pools, stock GARM v0.2.1 takes the provider name from the
[current pool configuration](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/database/sql/util.go#L89-L99),
not an immutable route on the runner row. Cleanup uses the stored provider
ID or falls back to the runner name. [Its pending-deletion worker](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/pool/pool.go#L1543-L1702)
can remove the row after provider deletion reports success. In the
[reviewed Incus provider](https://github.com/cloudbase/garm-provider-incus/tree/13e6dd70f71c7a7a9ae768695556c8b28056266f),
the ID is the instance name, lookup uses the configured project, and
instance-not-found can be treated as successful deletion. Therefore a provider
name repointed to another project can leave an old runner in its original
project while the matching GARM row disappears. The same risk applies in
reverse on rollback.

- Keep the old pool-to-provider and provider-to-project mappings intact while
  any old runner, pending start, or uncertain cleanup can still exist. Before
  changing a mapping in place, independently confirm no associated guests
  remain in the old project, no unresolved GARM rows or start operations
  remain, and no GitHub runner assignments still depend on the route. A
  disabled pool is not a drain result: stock idle scale-down can skip it,
  while [already-pending creates](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/pool/pool.go#L1709-L1759)
  can still reach the provider.
- If independently qualified routes coexist, retain both mappings and count
  both projects against the same physical host budget. Verify the destination
  runner’s expanded flavor profile, root device and resource limits under the
  exact account and project. If coexistence cannot respect the host ceiling,
  leave the mapping unchanged until the original route is independently drained.
- Use the same checks for rollback. A disappeared GARM row, a provider
  success response, or a pool toggle alone does not prove the old Incus
  runner is gone or its capacity is free. If any source is incomplete,
  pause the change and preserve the existing mapping.

## Minimum-idle shadow comparison

With the existing read-only minimum-idle preview enabled, the same scoped
capture can also emit one optional `stock_idle_decision_preview` on the
allocation-plan response. Its `direction` is `increase` or `decrease`; it
reports the observed and suggested minimum, the unchanged maximum, and an
independent evidence expiry. The separate legacy reduction preview is unchanged.
Absence means no decision was confirmed, not that no change is needed.

An increase is one slot toward the saved warm target and requires two fresh
matching captures with a classified quiet observed queue, no ambiguous warm
supply, and fitting the existing floors as well as the extra slot. The saved
policy repositories must exactly match the configured observation scope for
an increase; narrower policy coverage is not treated as a quiet queue. This is
configured-only modeling, not verified provider placement. A decrease is not
accounted as a release and cannot fund an increase. No recommendation queues or
applies a change, and history is held only in memory.
