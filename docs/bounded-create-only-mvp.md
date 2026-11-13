# Bounded create-only pilot, not an elastic runner service

This is an operating-limit analysis of pinned Garm v0.2.1
[`154638445c3949c1958b01812f69d9a1e4d82684`](https://github.com/cloudbase/garm/tree/154638445c3949c1958b01812f69d9a1e4d82684),
provider-common v0.1.9 default bootstrap, and this repository's experimental
source-bound CREATE path. It does not enable deployment, automatic retirement,
manual release, or a production drain command. Installed images, service wrappers,
runner versions and provider binaries remain separately qualified prerequisites.

## What can be useful now

A finite, explicitly experimental batch can exercise admission, original JIT
preparation, placement, one physical CREATE, readback, observations and accounting
without implementing physical retirement. Keep native guests and all dispatched
charges after the batch. The next batch eventually receives capacity deferral.
This is useful creation/accounting validation, not sustained autoscaling or a
production job-safety certification. The default-off execution gate stays off
until the separate deployment qualifications are satisfied.

The implemented provider accepts Admit/RecordPreparation/ValidatePreparation,
Create/Get/List and proven never-dispatched CancelAdmission. Delete, Start, Stop
and RemoveAll remain unsupported. These restrictions must apply to **every**
provider path and identity; substituting the original Incus provider, direct Incus
credentials, another daemon, or another controller bypasses this boundary.

### Keeping a guest running is not keeping a reusable worker

* Default non-JIT Linux **and Windows** registration in provider-common v0.1.9
  explicitly uses `--ephemeral`, including the runner-group branches.
* GitHub JIT registration is intended for one job. This premise does not prove
  native one-use/no restart/no alternate registration. Scale-set creation sets
  `RunnerSetting.Ephemeral: true`.
* A consumed runner can deregister or exit while its native guest stays running.
  The guest's name/status/list presence does not make it reusable pool capacity.
* Setting `disable_jit_config`, raising minimum idle, changing labels, re-running
  registration or restarting the listener is not a supported persistent-worker
  mode or an accounting reset. Custom registration/templates are outside this
  qualified source path, not a ready-made solution.

For homogeneous runners on a selected host/pool, a local upper bound on **new
physical starts** is the minimum, over global/host/pool CPU, RAM and runner-slot
limits, of `floor(max(0, budget - charged) / per_runner)`. Zero-valued resource
requirements are omitted, and all placement and configuration checks still apply.
Shared host/global budgets couple pools: do not add separate pool bounds. Each
dispatched attempt consumes budget permanently in this MVP, including uncertain,
failed or manually removed instances. Therefore N starts provides **at most N newly provisioned native guests**.
Plan for nominal one-job consumption and possibly fewer successful jobs, not N
concurrent reusable workers. This is not a hard job-count ceiling: restart,
registration-token reuse, alternate entry paths and a compromised guest remain
unqualified without the one-use supervisor/fences. Do not confuse default
ephemeral registration with proven one-use native execution. No replenishment follows native deletion, empty inventory or a timeout.
Only a transactionally proven never-dispatched cancellation can free its original
reservation; it cannot clear a dispatched or ambiguous create.

## Why stock Garm controls are not manual-drain fences

The source-contract fixtures compare exact official functions with the patched
checkout and preserve these counterexamples:

| Observation/control | Actual source behavior | Not proved |
| --- | --- | --- |
| Ordinary pool `enabled=false` | Scale-down/creation paths have guards, but completed-job handling can still mark pending_delete; delete-pending runs with `alwaysRun=true` | No accepted work, no in-flight CREATE, no cleanup, or no new assignment |
| Runner idle / minimum-idle reduction | Scale-down selects locally idle running instances then calls DeleteRunner | Atomic idle-to-unassignable transition |
| Completed-job webhook | Marks runner terminated and instance pending_delete | Guest descendants/hooks/cleanup complete, or native birth frontier closed |
| Offline/missing registration / bootstrap age | Reaper/orphan heuristics can request deletion | Native absence, job absence, or immutable incarnation history |
| Garm restart | Interrupted creating/deleting instances can call DeleteRunner with unauthorized-error bypass | Old CREATE requests fenced or job-safe recovery |
| Force removal | Pending-force-delete permits row removal even after provider deletion failure | Physical absence or capacity reclaimed |
| Entity manager Stop | Closes its local quit channel | Existing guest listener stopped, remote assignment sealed, or accepted work settled |
| Scale-set disable | Stops the scale-set demand listener; cleanup/target reconciliation is separate | Individual runner listener/control/native work sealed |

The provider's refusal prevents **this provider** from deleting a native guest;
it does not undo stock Garm's GitHub deregistration requests, status transitions,
force-row-removal behavior or external actors. Garm's comments about GitHub busy
runner deletion are not a documented atomic drain acknowledgment. An old idle
read followed by deletion is not a race-free accepted-work fence. Do not advertise
this combination as a fully job-safe lifecycle.

## Manual maintenance: stop-new and wait, not drain-and-release

This is a constrained maintenance posture, not an executable cleanup runbook:

1. The operator first arranges an external maintenance window covering **all**
   assignment and creation sources, including other repositories/groups,
   reruns, direct jobs, JIT issuers, queued provider/RPC requests, old controller
   processes and service/guest restart paths. No existing Garm setting supplies
   that complete fence. If coverage cannot be established, report it as unknown.
2. Stop accepting new desired work under the controller's supported admission
   controls. A disabled pool/host or stopped Garm process is not a barrier for
   work admitted or sent before the change. Preserve its dispatch correlation
   and full charge; no timeout discards it.
3. Leave guest listeners and job/control delivery alone while already accepted
   jobs finish. No SIGINT/SIGTERM/service-stop, force runner removal, native stop,
   kill or delete is justified by this document. The pinned runner/service tests
   show those can cancel work or restart a listener. Do not suppress the shared
   message poll: it also carries job cancellation/control messages.
4. Record independent workflow/native observations for diagnosis. Busy=false,
   offline, completed webhooks, matching empty project reads, elapsed time,
   local poll seals and a new process generation are **not** completion/fencing
   authority. If cleanup is unnecessary, simply retain the guests and charges.
5. Any actual drain/destruction requires a separately qualified, authorized
   external procedure: exact installed runner/supervisor, all-entry-path native
   work isolation, remote no-new-assignment/control frontier, no-restart and
   old-writer/queued-request fence, immutable incarnation and authoritative
   absence evidence. If those are absent, destructive maintenance stays blocked.
   Even externally performed cleanup does **not** permit this controller to
   release or recreate a dispatched identity.

Do not reset/reinitialize the ledger, restore an older snapshot as active, delete
charges/tombstones, rename a host alias/project, rotate identities, or raise a
budget to make a spent finite batch appear reusable. A legitimate independently
reviewed capacity expansion is a different action, not proof of retirement.
Native quotas and an exclusive whole-host budget remain necessary; daemon TLS
identity is not physical hardware uniqueness or proof that another writer cannot
allocate. Runtime lineage guards detect some local mistakes, not external
rollback, compromised root or all already accepted network requests.

## Verification boundary

`integration/garm-operating-limits/source_test.go` inspects compiled default
bootstrap templates without executing shell, plus hash-pinned official Garm git
objects. Relevant lifecycle functions must be identical in the disposable patched
checkout. The canonical compatibility harness runs these checks with
`GCC_PINNED_GARM_WORKTREE` set. The optional source check explicitly skips without
that fixture; an ordinary unit-suite pass alone is not source qualification.
These checks detect source-contract drift, **not** actual GitHub policy, installed
runner behavior, atomic fencing, physical isolation, native absence or release
safety. They start no Garm services, jobs, runners, containers or virtual machines.
The read-only hosted M2 metadata survey likewise grants no runtime authority.
