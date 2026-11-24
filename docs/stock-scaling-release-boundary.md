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

The durable stock-apply controller state added under plan 02 keeps that
boundary: it records operator-reviewed proposals, maintenance pauses and
interrupted-write uncertainty in a restart-safe journal, and renders every
mutating command disabled with an explicit reason. A matching old or new
settings readback is an observation only; `applied_when_proven` is reachable
solely through an independently authoritative outcome, which no pinned stock
interface can supply today. See
[the recovery contract and compatibility gap](stock-apply-recovery-contract.md).

## Qualify a missed initial job webhook at zero idle

This is a separate, unrun rollout scenario for an ordinary repository pool,
not part of the approved single-job connectivity smoke. It describes a known
stock behavior, not an observed outage or live acceptance result. The pinned
[stock queue consumer](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/pool/pool.go#L2045-L2084)
reacts to jobs already recorded in its database. Its stale-queue check
reconciles existing rows; it does not import a missing job. At zero minimum
idle, an otherwise eligible GitHub job can therefore stay queued if its
initial `workflow_job: queued` delivery never reached GARM.

1. In a separately authorized disposable test, start with the exact pool
   at zero minimum idle and independently verify no idle eligible runner.
   Record the exact GARM build, pool, and GitHub repository/run/attempt/job
   identity. Establish that the initial delivery never reached the handler,
   that GitHub still reports that job queued, and that a complete scoped
   GARM queue read lacks the corresponding row. A response timeout, failed
   read, or incomplete queue view leaves receipt unknown; it does not prove
   absence. This is not the [provider refusal case](stock-garm-provider-capacity-behavior.md#what-the-existing-full-capacity-qualification-must-demonstrate),
   where GARM already knows the job and creation is attempted.
2. If separately authorized and supported in the exact setup, exercise
   normal redelivery of that exact original event or one bounded warm runner.
   Do not substitute a synthetic webhook, rerun, or second job. The
   [read-only demand reconciliation](demand-repair.md) can flag a discrepancy,
   but it does not feed the stock queue or prove recovery.
3. For the redelivery path, if the delivery mechanism supports it and its
   use is separately authorized, verify duplicate queued delivery does not
   multiply queue entries or runner creation for the same job. Once the
   terminal event is confirmed recorded, verify a late redelivery of the same
   queued event does not reopen that job or create another runner. Record
   unsupported, unapproved, or unobservable replay cases as unqualified.
4. Count recovery only if GitHub shows the same repository/run/attempt/job
   assigned to the expected runner, started, and completed within the
   predeclared observation window. A successful delivery response, new GARM
   row, ready runner, or a different job is not a pass. If neither recovery
   path is supported and authorized, or exact completion is not observed,
   record recovery as unproven. For a temporary warm runner, restore and
   verify the original zero-idle setting and independently verify teardown of
   that exact runner and its allocation before counting capacity as free.
   An absent GARM row alone does not prove teardown. This requirement does
   not authorize changes to fleet settings, security controls, or live rollout.

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

### Check the effective Incus profile and root volume

Use existing authorized, independent read-only evidence for one naturally
occurring allocation on the exact normal pool. Keep the provider invocation,
Incus operation, authenticated daemon/project and exact instance tied to that
allocation. Do not combine a past placement with today's provider, profiles or
storage defaults.

1. **Loaded provider and profile order.** Record the exact provider build and
   loaded `include_default_profile` setting alongside the pool flavor. The
   [stock Incus provider v0.1.5](https://github.com/cloudbase/garm-provider-incus/blob/v0.1.5/provider/incus.go)
   requests the flavor alone, or `[default, Flavor]` when that setting is true.
   Compare the expected list to the actual instance's ordered `profiles` list.
   For each applied profile, independently verify its owning project and
   reviewed config/device contents or content digest. A profile name, including
   `default`, is not proof of identity or content.
2. **Local and expanded configuration.** Compare the instance's local
   `config` and `devices` with its `expanded_config` and `expanded_devices`.
   [Incus v7.0.2](https://github.com/lxc/incus/blob/v7.0.2/internal/server/db/cluster/profiles.go#L117-L153)
   applies the profiles in order, replacing a same-named device as a whole,
   then applies local instance overrides. Verify effective CPU, hard absolute
   memory and the single named storage-pool root disk at `/`. The v0.1.5
   create request does not set a local root device; do not assume that from
   an unverified installed executable.
3. **Root volume and disk pressure.** On that same daemon and allocation, match
   the expanded root's named pool to the resulting instance volume by project,
   type and instance identity. Check the pool driver, the applicable
   `volume.size` default (set, absent or unknown), and any effective volume size
   or override independently observed. An omitted root-device `size` is not
   evidence of unlimited storage: [Incus volume sizing](https://github.com/lxc/incus/blob/v7.0.2/internal/server/storage/drivers/volume.go#L141-L154)
   can inherit a pool default; the [create path](https://github.com/lxc/incus/blob/v7.0.2/internal/server/storage/drivers/volume.go#L589-L675)
   also depends on volume type and source. A size setting does not prove
   enforcement. Evaluate free bytes, free inodes and reserve separately on
   the verified backing storage, not from the guest, profile or pool name.

If the existing evidence cannot corroborate these checks, leave the affected
claim unknown. Do not enable tracing, request broader access, launch a job, or
change pool settings just to fill a gap. The
[offline v3 model](incus-project-declaration.md#optional-countcpuram-model-without-sized-disk-declarations)
still compares only count, CPU and RAM; its disk assessment is `not_evaluated`.
Neither that model nor a single observed route verifies fleet-wide limits or
authorizes a settings write.

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
