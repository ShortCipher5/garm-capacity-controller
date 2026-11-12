# Native capacity and physical release: a bounded implementation path

Status: implementation plan plus a **read-only visible-cgroup quota reader**.
No writer authority, hardware registration, native parent enrollment, mutation,
release permit, recovery activation, or production deployment is added here.
The existing delayed-write, hidden-project and independent-ledger counterexamples
remain unresolved by this change.

## Choose one narrow first profile

Target a dedicated Linux cgroup-v2 runner partition, **containers only**, one
qualified Incus daemon and one host-local mutation authority. Exclude VMs,
privileged runners, host control sockets, other unaccounted daemons and arbitrary
host workloads from this initial profile. Trusted host administration is an
explicit assumption, not protection against a malicious root user.

A host-local broker must own the only controller-accessible Incus write credential
and socket path. Network policy, UNIX permissions and service isolation must make
all direct controller/guest writes impossible. Enumerate CLI, socket, proxy,
cluster/internal calls, start-on-boot, restore, migration, snapshot, device and
profile update paths. Unknown or unenforced paths make this profile ineligible.
Merely routing our provider through a proxy does not establish exclusivity.

The accounting authority is a *physical allocation domain*, not an Incus server
name, project or TLS key. Register its exact host/install generation and native
parent once. All participating projects/daemons must resolve to this same domain;
otherwise reject them rather than create another allowance. There must be one
local authoritative broker journal for the domain. Cloning or restoring it does
not automatically enroll another authority or restore the right to write.

## Enforce native capacity before scheduling

Put every guest process, nested descendant, exec session and runtime helper
charged to this domain beneath an independently enforced parent with hard CPU,
memory, swap and PID bounds. Reserve resources for the host/Incus/broker outside
that allowance. Prevent guest delegation, reparenting, host namespace access and
unrestricted device access from escaping the parent. Disk capacity and IO require
separate filesystem/block-device quota qualification; they are not cgroup memory.

Do **not** assume setting `MemoryMax` on `incus.service`, adding a container
profile, or finding the daemon's own cgroup establishes this. Container runtimes
may place payloads outside their daemon's service cgroup. Verify actual payload
birth and all fork/exec/restart/restore/migration paths. A PID moved beneath the
parent after it starts leaves an escape race; containment must apply at creation.

An Incus VM's QEMU process, vCPU threads, helpers and assigned storage must be
included and its overhead budgeted separately. Container limits alone say nothing
about an unrelated QEMU cgroup or a second daemon. Until native VM creation and
all related helpers are tested beneath the same parent, **VMs are unsupported**.
Out-of-band host workloads outside this partition do not consume its declared
allocation budget, but still compete for physical resources; OS headroom, the
underlying VM/provider limits and fair-use policy remain independent constraints.

Native hard caps prevent a modeled partition from exceeding its cap. They do not
by themselves uphold global/per-pool admission fairness, prove free capacity,
prove no accepted operation remains, or make a job safe to terminate.

## Accepted-work frontier, not operation-list polling

The existing pinned Incus source investigation shows a POST may be accepted before
`OperationCreate`; terminal operations are later discarded; collection visibility
is permission/project filtered; deletion is targeted by name. Therefore:

1. At the **sole enforced ingress**, durably record an epoch, sequence, immutable
   request ID, allocation-domain/install generation, resource intent and charge
   before forwarding. Validate epoch and record acceptance atomically. Reject
   stale/replayed/unknown identities, including requests on pooled connections.
2. Record a stop-new transition and frontier `F` in that same journal. New accepts
   after `F` are denied; already accepted work is still an obligation, not canceled
   by the transition. No successor writer is activated yet.
3. Forward each accepted intent at most once. A lost socket response, broker crash,
   absent operation or missing list item leaves the intent unknown and charged.
   A local lease check before an independent POST is not atomic native fencing.
4. A native adapter must resolve **every** accepted intent through `F`, including
   pre-operation handlers, asynchronous queues, process births and pending
   recreation. Its outcome history must survive operation-list expiry and cannot
   be supplied by the requesting controller as a boolean or arbitrary JSON proof.
5. Only after this native frontier is complete can the old writer be considered
   fenced. Credential revocation or broker exit alone cannot revoke a delivered
   native request. Native autostart/restore must either be covered by the same
   frontier or disabled and verified under the chosen installation contract.

Stock Incus API observations do not currently supply step 4's complete contract.
An actual implementation needs either a native daemon extension owning acceptance
and incarnation outcomes, or an independently qualified enclosing execution
boundary that can fence and observe **all** enclosed native state without killing
accepted jobs. This is a hard prerequisite, not a missing parser or polling retry.
Do not implement a `fenced=true` override or expose a release route based on these
steps until the boundary exists and has passed native fault-injection tests.

## Exact physical release is a separate transaction

A future release intent must bind reservation/placement, domain/install generation,
immutable native incarnation, original accepted request and the job-safe completion
frontier. It must reject replacement resources with the same name. Conditional
incarnation deletion must be enforced at the native mutation point, not by a GET
followed by name-only DELETE. A terminal CREATE is not a terminal DELETE; a
successful DELETE response is not proof that a delayed CREATE cannot recreate it.

The adapter must establish exact incarnation absence *after* both the complete
accepted-work frontier and native deletion, with no remaining recreation path.
An authoritative, unfiltered whole-domain view or enforced exclusive containment
is necessary; credentials that can see the configured project are insufficient.
Only then may a separately reviewed atomic ledger operation retire the matching
charge exactly once. No such adapter or release operation is implemented here.

Loss/rollback of the host journal requires an independent rollback-resistant
anchor or a separately qualified destructive retirement of the whole old domain.
Restoring a backup with the same UUID is not an anchor. A host reboot clears some
processes but is neither job-safe retirement nor an automatic accounting reset.

## What the implemented reader actually does

`internal/nativecapacity.Read(mountRoot, target)` performs bounded read-only reads
of a real cgroup-v2 filesystem. It returns the smallest **reported** `cpu.max`,
`memory.max`, `memory.swap.max` and `pids.max` across the target's visible ancestors,
with the source directory and an explicit list of missing controls. The kernel
root has no `cgroup.type`; absence is accepted and recorded only at the visible
mount root, while missing/non-domain types below it are rejected. CPU quota is
an exact rational core value, not rounded millicores. It rejects malformed,
unreadable, oversized and unsupported threaded-cgroup inputs, and refuses ordinary
filesystems rather than treating test fixtures as native evidence. Every opened
control file is also checked as a regular cgroup-v2 file, so a nested ordinary
mount is rejected; nonblocking opens avoid waiting on an injected pipe. These
checks do not authenticate ownership or rule out a different cgroup bind mount.

Important limitations:

- Values are quota configuration facts, not measured usage or free capacity.
  Siblings share the ancestor quota; their occupancy is not subtracted here.
- The reads are not an atomic kernel snapshot. Administrators may alter limits
  between reads; a second matching read would still not prove immutability.
- A cgroup namespace or subtree mount can hide ancestors. Successful reads cannot
  certify the physical root, parent ownership or complete workload membership.
- CPU bandwidth periods/burst, cpusets, host scheduler and provider limits affect
  real throughput. This reader does not claim dedicated cores or instantaneous
  CPU bounds; memory reclaim/OOM and swap behavior need native stress tests.
- Missing controls remain explicit unknowns. `max` is not proof that the host is
  unbounded. The reader does not set limits, inspect secrets, move processes,
  fetch credentials, issue tokens, or authorize admission, deletion or release.

No service/UI route or accounting consumer uses the reader yet. Its purpose is to
supply a bounded prerequisite for a future independently privileged qualification
agent, not to turn caller-controlled filesystem values into authority.

## Next implementable qualification slices

1. In an authorized disposable **real cgroup-v2/Incus** environment, record native
   payload/helper ancestry and hard-parent stress results across supported lifecycle
   paths; fail on any escape or unsupported VM. Do not run privileged experiments
   on existing runner hosts as a shortcut.
2. Implement the sole-ingress journal/epoch adapter with fail-closed recovery,
   and native acceptance/outcome instrumentation. Test delayed pre-operation
   requests, lost ACKs, pooled stale credentials, foreign projects, aliases and
   restored journals against the actual boundary, not only an in-memory model.
3. Combine that evidence with the separately implemented job-safe supervisor and
   immutable-incarnation deletion contract before adding an accounting finalizer.

A quota reader, passing mocks, successful TLS or a complete-looking inventory
cannot skip any of these gates.
