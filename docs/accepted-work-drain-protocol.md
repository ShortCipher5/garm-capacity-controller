# Accepted-work drain: concrete protocol requirements and a test-only experiment

**Status: BLOCKED for production. No runner stop, deregistration, DELETE,
accounting release, runtime receiver or permission/proof constructor is added.**
The current controller's existing permanent retirement blockers are unchanged.
The [six actual historical runner source counterexamples](../integration/actions-runner-v2.323.0/README.md)
show why shutdown, idle/terminal status and RunOnce cannot be used as a fence.
This document is a protocol specification plus necessary local experiments, not
an assertion that GitHub or the deployed runner implements the required APIs.

## Production entry conditions: if any is unavailable, do nothing and refuse

1. **Exact deployed artifact and path coverage.** Independently pin the installed
   runner binary/source, protocol/configuration, classic/broker/scale-set paths,
   self-update and supervision behavior. The historical d8b82f44 fixture under a
   test-only security-updated dependency graph is not installed-image authority.
2. **Authoritative remote acquisition seal/frontier.** The assignment service must
   atomically exclude NEW assignments for one exact runner registration/session
   generation, and enumerate/resolve every pre-seal accepted assignment, poll,
   lease, queue and broker handoff at a durable frontier. Client cancellation,
   a deleted session, offline/busy status, a watermark we invent locally or a
   failed HTTP request does not supply this contract. No such qualified API is
   implemented/enrolled here; a `fenced:true` response is not accepted.
3. **Control and renewals remain live.** Owner cancellation, lease renewal,
   protocol heartbeat and cleanup traffic must continue while accepted work runs.
   The current Runner's message stream mixes assignment and control. Merely
   suppressing `GetNextMessageAsync` is therefore NOT an acceptable production
   drain. A version-qualified service control-only mode/channel or equivalent
   proven demultiplexing contract is required; it must not hide new assignments.
4. **Independent native supervisor and no restart.** Trusted outside arbitrary job
   code, bind the intent to host boot/process-birth identities, runner registration,
   session, exact backend Incus UUID/generation/creation, original placement/request
   and history references. It must observe worker, descendants and post-job cleanup
   finished, and hold a no-restart/reconnect/re-register generation through any
   later retirement. A guest-readable key or process-exit boolean is not authority.

These are external mechanisms to build/qualify, not operator checkboxes or
constructible JSON. The current controller cannot establish them, so the safe
production outcome remains refusal, with all allocation charges retained.

## Proposed ordering once those contracts actually exist

1. The independent supervisor durably records a one-use drain intent and exact
   identities. No job-affecting action precedes durable local history. Process
   restart, ambiguous journal completion or identity mismatch invalidates it.
2. Serialize each local acquisition with the seal intent: register an unresolved
   acquisition BEFORE invoking the external service. Obtain the authoritative
   remote seal/frontier while preserving control/renewals. Resolve every old
   acquisition against that frontier. Do NOT cancel a poll and assume it accepted
   nothing. A late accepted job transfers from acquisition to accepted-work
   history in the same local transaction, with no observable empty interval.
3. Stop NEW acquisition only under that remote contract. Dispatch all accepted
   pre-seal work according to the service contract. An accepted message not yet
   dispatched remains an obligation; ephemeral skip/RunOnce cannot erase it.
   Duplicate/unknown paths, lost responses, lease ambiguity and generation drift
   refuse qualification rather than discarding work or retrying an assignment.
4. Let accepted work complete normally. Retirement must not cancel its token,
   send a worker cancellation/shutdown or kill it after a timeout. Continue
   legitimate owner cancellation independently, and still wait for its native
   worker/descendant/post-job completion. Deadline expiry cancels only observation
   and returns unresolved; it is never a permission to interrupt work.
5. Join the authoritative accepted frontier with trusted native completion,
   shutdown-safe empty dispatcher and held no-restart generation. Only then may a
   separately authorized, version-qualified normal listener shutdown be considered.
   The current `ShutdownAsync` cancellation behavior makes calling it earlier unsafe.
6. This still supplies NO physical/accounting release. External old-provisioning
   writers and accepted backend requests must be fenced, exact original DELETE
   history captured, and new complete post-delete observations collected under
   all fences. The [physical retirement boundary](physical-retirement-proof-boundary.md)
   remains separately blocked. Nothing here bypasses any of its five gates.

## What the new experiment actually proves and what it deliberately cannot

The only implementation is `LocalIntakeFenceExperiment.cs` under integration test
inputs. It is copied into the official source's disposable Test project, NEVER
into a production runner/controller target. Its irreversible in-memory seal and
lock model one necessary local serialization rule. A pre-seal acquisition remains
counted until its response is atomically recorded as accepted work. Successful
explicit worker AND cleanup tasks are both required for local quiescence. Poll
fault/cancellation, unrecognized paths, duplicate identity, failed/cancelled work,
unbound accepted work and invalidated generation refuse local qualification.
There is no timeout release, worker cancellation, external call except the injected
TEST poll delegate, production state mutation or retirement permission.

Three cases run the ACTUAL pinned Runner loop with test listener/dispatcher seams:
late acquisition after seal is retained through cleanup; premature RunOnce is
held at a MOCK shutdown boundary until worker+cleanup finish; and blocking new
message polls demonstrates lost control delivery. The last is an explicit
counterexample to deploying this coordinator as-is. The original six cases still
execute the real Dispatcher cancellation/RunOnce code without production patches.
Direct coordinator tests add ambiguous errors, unfinished/failed cleanup,
generation loss and concurrent poll-to-work transfer checks.

The worker/cleanup tasks are TEST DOUBLES, not a qualified native supervisor.
The control stream is NOT separated, the service frontier does NOT exist here,
local history is NOT durable, old/new object generation IDs do NOT fence a real
restart, and broker/other messages are NOT qualified. A test explicitly shows a fresh empty object can be sealed locally quiet while
the prior generation stays ambiguous. Fresh object state cannot recover earlier
work and must not be used as a restart policy. Local quiescence
is only an experimental observation and is never accepted by any runtime route.
All existing source pins, audit checks, no-install policy and honest hosted
compilation requirements continue to apply.
