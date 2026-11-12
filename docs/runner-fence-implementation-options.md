# Practical runner fence options: exact interfaces, no enabled retirement

**No production drain, stop, deregistration, runner/image change or release is
implemented.** This design narrows the next implementation choice; all current
retirement gates remain blocked. Checked sources: official actions/runner
[v2.323.0 d8b82f44](https://github.com/actions/runner/tree/d8b82f44a9ec0258904d5869ae683c31d5aac299)
and GARM [v0.2.1 15463844](https://github.com/cloudbase/garm/tree/154638445c3949c1958b01812f69d9a1e4d82684).
Neither identifies the installed/prebaked runner, agent or launch chain.

## What the existing runner interfaces actually offer

- `IRunnerServer.GetAgentMessageAsync` and `IBrokerServer.GetRunnerMessageAsync`
  accept a `TaskAgentStatus` hint and return a `TaskAgentMessage`. Their interfaces
  have NO typed drain generation, accepted-work frontier, acquisition seal or
  control-only acknowledgment. Online/Busy/Offline enum values are not such proof.
- Both actual listeners send status on their common message stream. `OnJobStatus`
  cancels the local current-poll token and changes the next request's hint. A late
  old-poll response can still return a job. The classic listener can recreate an
  expired session unless `SkipSessionRecover` is set. Neither observation qualifies
  the server's response races or prevents an independent process from reconnecting.
- Busy polling is preferable to dropping control traffic for a FUTURE qualified
  implementation, because it can continue owner cancellation. But a mock transport
  can return either job or cancellation with Busy, and the actual client accepts
  both. This is evidence of missing client-side proof enforcement, NOT evidence
  that GitHub actually assigns a second job contrary to its documented contract.
- Job request renewal/completion interfaces are separate from message polling.
  Keeping those alive does not by itself preserve message-based owner cancellation,
  and a terminal server result still does not prove native/post-job quiescence.

Exact source links: [classic listener](https://github.com/actions/runner/blob/d8b82f44a9ec0258904d5869ae683c31d5aac299/src/Runner.Listener/MessageListener.cs),
[broker listener](https://github.com/actions/runner/blob/d8b82f44a9ec0258904d5869ae683c31d5aac299/src/Runner.Listener/BrokerMessageListener.cs),
[RunnerServer interface](https://github.com/actions/runner/blob/d8b82f44a9ec0258904d5869ae683c31d5aac299/src/Runner.Common/RunnerServer.cs),
[BrokerServer interface](https://github.com/actions/runner/blob/d8b82f44a9ec0258904d5869ae683c31d5aac299/src/Runner.Common/BrokerServer.cs).

## Why ordinary no-restart settings and service stop are insufficient

The exact official `RunnerService.js` wraps the listener itself. Exit 2 retries
internally after five seconds; exit 3/4 (updates, including ephemeral update) does
likewise. `GITHUB_ACTIONS_SERVICE_EXIT_AFTER_N_FAILURES=1` limits unknown failures,
NOT those retry/update branches. So an outer manager's `Restart=no` alone does
not establish no listener restart. Interactive `run.sh`/`run-helper.sh.template`
also contain their own retry/update loop. Self-update must be qualified too.

The service wrapper's SIGINT/SIGTERM handler sends SIGINT to the listener, then
schedules SIGKILL after thirty seconds. Combined with the actual dispatcher's
shutdown cancellation, this is a counterexample to calling service stop a drain.
The unit's five-minute TimeoutStopSec does not remove that inner thirty-second
kill path. These tests invoke only fake processes/timers, NEVER send OS signals.

GARM's default [metadata unit](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/metadata.go)
starts `runsvc.sh` in classic JIT mode. Its
[Linux userdata](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/internal/templates/userdata/github_linux_userdata.tmpl)
can instead launch garm-agent with `Restart=always`, whose configured runner
command is `run.sh`. Thus qualifying only the runner systemd unit is insufficient.
The agent binary/version and its own restart/configuration channels also need
separate qualification. Its runner-user-owned configuration is not independent
supervisor authority against arbitrary job code.

## Options and recommendation

### A. Drain an arbitrary existing idle/persistent runner: currently unavailable

Changing labels/groups, setting Busy/Offline, pausing GARM creation, stopping a
scale-set acquisition loop, deleting a session/registration or cancelling a poll
cannot supply the required accepted-request frontier AND preserve existing work
and controls. GARM's `DeleteRunner` is a removal/mark-for-delete path, not a typed
job-preserving fence, and bypass/force variants make it still less suitable.
Scale-set AcquireJobs is another acquisition layer, not a runner no-job proof.
No deletion/label/status operation is executed by this slice.

This option needs a GitHub-supported contract or protocol enhancement covering
classic, broker/migration, scale-set and already accepted work. Do not ship a
client-only imitation of a server seal.

### B. Prefer NEW consumed ephemeral/JIT runners, then natural exhaustion

GitHub's [runner reference](https://docs.github.com/en/actions/reference/runners/self-hosted-runners#ephemeral-runners-for-autoscaling)
documents one assigned job per ephemeral runner and automatic deregistration
after processing it; [removal guidance](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/remove-runners)
also describes JIT runners as single-job. This is a real documented service-side
restriction, narrower than inventing a remote drain API. It is not native-worker
completion or old-provisioning-writer evidence.

GARM's default classic template uses `--ephemeral` in the registration fallback,
and its scale-set creation requests `Ephemeral:true`. Still qualify the ACTUAL
registration/JIT settings and deployed protocol; the presence of any JIT map,
RunOnce completion, webhook, row disappearance or local ephemeral flag is not
proof of the intended processed-job lifecycle. The source counterexamples stand.

**Recommendation:** develop this NEW-image, consumed-single-job path first. Keep
assignment/control polling and renewal unchanged while its one accepted job runs.
Use trusted native/descendant/post-job completion plus service consumption evidence,
then hold independent no-restart/re-register exclusion. Do not reclaim unused idle
runners, failed/ambiguous attempts or existing unqualified images on a timeout.
They remain charged. Broker/scale-set qualification is a separate gate, not assumed
from this historic classic-source fixture.

### C. Concrete next production foundation, separate and not built/enrolled here

A future independently privileged one-use activation supervisor for a NEW image:

1. Enroll a qualified immutable launch manifest: actual runner/agent binaries and
   source digests, configuration semantics, exact host boot/Incus incarnation and
   placement/request/binding references. No automatic enrollment from guest JSON.
2. Use root/host-owned durable one-use activation history with an atomic lock/CAS,
   fsync ordering and rollback-resistant generation authority. Record consumption
   BEFORE launch; ambiguous launch/journal completion must refuse reactivation.
3. Launch the pinned listener directly under native containment, NOT the unqualified
   retrying run.sh/runsvc/agent chain. Gate every manual/service/update/reconnect
   entry path, including inherited credentials and bootstrap replay. Merely changing
   systemd Restart or storing a guest-readable token is insufficient.
4. Keep supervision/control state outside arbitrary job access. Observe process
   birth/boot identity, cgroup/descendants and post-job completion independently.
   A job with host/root or supervisor-credential access defeats this trust boundary;
   such an image must remain unsupported, not be falsely attested.
5. Once naturally consumed and natively quiescent, durably seal activation forever
   and retain that fence across reboot/restore/re-registration. A fresh local object
   or restored old journal must not authorize another activation.

Implement/test that supervisor and crash/rollback/duplicate activation semantics
as a separate no-delete slice AFTER the manifest/enrollment trust model is reviewed.
It cannot be honestly produced by adapting the current in-memory experiment. It
still would not prove absence of old backend creates or authorize physical release.
Those independent fences and original deletion history remain required.

## What this current code slice can safely execute

Four new disposable C# tests call the ACTUAL classic/broker listeners with mocked
transports: shared Busy control/job responses, a late job after status cancellation,
classic session recovery versus SkipSessionRecover, and broker Busy behavior.
Eight Node VM tests execute the unmodified, hash-pinned `RunnerService.js` with fake
child-process, process and timers. They prove internal retry branches and the
interrupt/kill sequence without a real child, OS signal, service or network call.

The VM uses an ALREADY INSTALLED Node runtime and records its exact version. It is
not qualification of the installed runner's bundled Node. The C# tests still use
the reviewed test-only security dependency baseline, not the original production
graph. New C# cases require actual hosted compilation before being called passed.
No production assembly, receiver, configuration, credential, native fence, ledger,
stop/deletion or charge-release behavior changes.
