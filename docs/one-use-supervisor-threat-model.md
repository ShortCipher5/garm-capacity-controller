# One-use supervisor: threat model and implementable isolation plan

**Proposed NEW-image profile, not an enabled supervisor or isolation assertion.**
No enrollment, privileged daemon, namespace/container/VM launch, network policy,
stop, deregistration, DELETE or capacity release is implemented. A durable file,
SQLite row, systemd setting, guest JSON or successful local test is NOT host
isolation. Default GARM images and launch chains remain unqualified.

## 1. Scope and attacker model

Target the narrower naturally consumed ephemeral/JIT route from
[the implementation options](runner-fence-implementation-options.md). Arbitrary
idle/persistent drain remains unavailable without a qualified service frontier.

The attacker controls repository code, actions, scripts, container images,
Dockerfiles, workflow environment/paths, options/volumes, normal job outputs and
background processes. Assume it attempts credential theft, a copied listener,
reconnect/re-registration, hook/RPC spoofing, symlink/path attacks, host Docker
access, cgroup escape and a forged completion message. Also test crashes, process
and PID reuse, reboot, old backups/snapshots, delayed requests and duplicate starts.

Trusted computing base: the privileged host/enroller, qualified kernel/container
runtime, immutable control-image/hook/interpreter, supported patched runner and
GitHub's actually applicable single-job service contract. A malicious/compromised
host administrator, hypervisor or kernel is NOT defeated by this design. Detection
of changed enrollment or administrative ownership must close qualification. Jobs
must never receive runner-admin, GARM/Incus/supervisor credentials; a workflow
intentionally given that authority invalidates this threat model.

The historical v2.323.0 fixture and its security-patched TEST dependency graph do
not identify a production-safe installed binary. No installed artifact or host
isolation has been inspected here.

## 2. Why the default shortcuts fail: exact-source evidence

- `ContainerInfo` adds a writable `/var/run/docker.sock` mount for default Linux
  job containers. `container:` and `ACTIONS_RUNNER_REQUIRE_JOB_CONTAINER=true`
  therefore do not alone isolate arbitrary jobs from the host Docker authority.
- Workflow container options and user volumes survive into `HookContainer` input;
  the defaults can carry privileged/host-namespace options and a `/` host bind.
  A mount being called a *system mount* is not permission to forward it.
- `FeatureManager.IsContainerHooksEnabled` requires both the service feature
  variable AND a nonempty hook path. Setting only a root-owned hook path is not
  fail-closed enforcement: the ordinary non-hook paths still exist.
- `ProcessInvoker` starts children under the current process identity. It does
  not itself establish listener/job privilege separation.
- JS hook selection uses an absolute bundled Node path. Shell/PowerShell hook
  selection uses PATH/prepend-path lookup. Select only a qualified immutable JS
  bridge initially, and never let it resolve host children from job-controlled
  PATH, loader flags or module paths. An absolute interpreter alone is insufficient.
- `run.sh`, `runsvc.sh`, `RunnerService.js` and garm-agent may introduce their own
  restart/update paths. A supervisor must not wrap an unqualified retry chain and
  call it one-use. See the already executed source counterexamples in #40.

Source links at exact d8b82f44:
[ContainerInfo](https://github.com/actions/runner/blob/d8b82f44a9ec0258904d5869ae683c31d5aac299/src/Runner.Worker/Container/ContainerInfo.cs),
[HookInput](https://github.com/actions/runner/blob/d8b82f44a9ec0258904d5869ae683c31d5aac299/src/Runner.Worker/Container/ContainerHooks/HookInput.cs),
[FeatureManager](https://github.com/actions/runner/blob/d8b82f44a9ec0258904d5869ae683c31d5aac299/src/Runner.Worker/FeatureManager.cs),
[StepHost](https://github.com/actions/runner/blob/d8b82f44a9ec0258904d5869ae683c31d5aac299/src/Runner.Worker/Handlers/StepHost.cs),
[HostContext](https://github.com/actions/runner/blob/d8b82f44a9ec0258904d5869ae683c31d5aac299/src/Runner.Common/HostContext.cs).

## 3. Achievable first profile: Linux, unprivileged job sandbox, protected control

Do not start by supporting arbitrary existing workflows/images. Initial support
must require a mandatory job container and a closed, reviewed set of script/Node/
composite action paths. Privileged workloads, host execution, host Docker/socket,
custom host mounts/namespaces, services or Docker-action modes not yet qualified
must be refused before unsafe execution, not silently sent to the host.

**Privilege/authority split:**

- A privileged supervisor/broker owns activation history, cgroup/namespace handles,
  enrollment keys and admission/native references, outside job namespaces/mounts.
- Listener/Worker control processes run as a distinct control UID with only their
  limited registration credentials. They cannot edit image policy, broker state
  or enrollment. No GARM/Incus/app-admin credential enters the job trust domain.
- Arbitrary payloads run under a different unprivileged UID/user namespace with
  bounded native limits and no delegated cgroup escape. No host `/proc`, devices,
  privileged capabilities, sudo, Docker/admin socket, control home, registration
  credentials, install metadata, agent endpoint or supervisor IPC is accessible.
  Root *inside a user namespace* must not be VM/host/control root.
- The root-owned JS hook is a bounded data bridge, not a command shell. It calls
  only a fixed broker endpoint. The broker authenticates the actual control peer
  (for example kernel peer credentials plus enrolled process/generation), never a
  claimed UID/state field. Hook response/state scratch and native/activation
  handles must stay in protected control storage OUTSIDE all job-mounted workspace
  and temp paths. Expose only explicitly mapped job subdirectories, never the whole
  default shared temp tree. ResponseFile/state fields are not attestation authority.
- Mounts use fixed approved workspace/tool mappings, canonical handle-based
  resolution and symlink/traversal refusal. Reject arbitrary createOptions, host
  binds, privileged/namespace flags and unknown fields. Rebuild executor requests
  from the whitelist; do not forward user or default Docker socket mounts.
- Remove local Docker CLI/socket authority from the control image. Missing hook
  feature/path or an unsupported route must fail before payload execution, not
  fall back to ordinary Docker or DefaultStepHost. Verify every pre/main/post,
  script/Node/composite, image/build, cancellation, update and cleanup path against
  the actual runner. A missing feature flag must be an adversarial test case.

This requires an actual hardened image/runtime and likely runner/bridge changes,
not a config toggle. All custom commands must use fixed absolute binaries and a
closed environment. Runner parsing/download/extraction and the bridge are part of
the TCB and need a supported security-patched artifact, not our historic test pin.

For workloads requiring root/privileged Docker, a separate job VM outside the
control trust domain is an alternative FUTURE design, not this first profile.
It needs separate native lifecycle qualification and admission/native budgeting;
spawning an extra VM must not bypass the capacity controller. No such path exists.

## 4. One-use activation protocol: local durability is necessary, not sufficient

The following is a future actor API/ordering, NOT a currently callable endpoint:

1. `EnrollVerifiedImage` accepts only independently measured/enrolled image,
   runtime and policy identity, exact Incus UUID/generation/creation, original
   request/placement/binding references, host boot and supervisor generation.
   Reject operator/guest `isolated:true`, copied manifests and raw proof JSON.
2. `RecordActivationAttempt` atomically consumes the one-use slot BEFORE launch,
   with an audit sequence and fsync/commit guarantee. Use host-owned history and a
   monotonic anchor outside the job/guest snapshot/backup domain. Two actors must
   serialize against the SAME authoritative anchor. A restored local DB alone,
   file lock or independent ledger is not global no-restart authority.
3. `LaunchOnce` is permitted only within the protected broker generation and
   authority boundary, not by replaying a returned record. Directly launch the
   qualified listener, not runsvc/run.sh/agent retry wrappers. Place processes in
   the protected native group at creation (for example qualified atomic
   clone-into-cgroup semantics), not a fork-then-move race. Retain pidfd/process
   birth/boot and cgroup handles, not a reusable numeric PID/path alone.
4. No automatic retry if a crash occurs after consumption, including before
   launch confirmation. Leave the attempt ambiguous and charged. Never create a
   fresh history/generation to make a second start look legitimate. Unknown commit
   or lost reply must not become a second activation permission.
5. Record every acquisition/accepted message before dispatch/ephemeral skip across
   supported classic/broker/migration paths. Keep owner controls and lease renewals
   live. An unexpected extra assignment or unsupported path invalidates eligibility;
   a RunOnce signal, server result or ignored message is not native completion.
6. On drain intent, wait for natural single-job consumption and independently
   observed native worker/descendant/post-job completion. Do not cancel work,
   invoke shutdown, stop the service or kill after a deadline. Observation timeout
   only returns unresolved. Legitimate owner cancellation is distinct and still
   needs qualified native cleanup, not a cancellation-token shortcut.
7. Seal further broker/runtime submissions AND resolve every pre-seal accepted
   RPC/queued runtime handler at an authoritative frontier. A lost reply remains
   unresolved; it must not become a retry or native-empty proof. A queued command
   can otherwise spawn after an empty observation. Only under that held generation
   observe retained native handles/groups empty and join applicable service
   consumption evidence. A point-in-time cgroup-empty read is not a creation fence.
   Every reconnect/registration/launch authority must remain excluded, including
   copied clients and readable credential replay elsewhere.
8. `RecordSealed` is permanent history, not a physical retirement permit. The
   existing backend-writer frontier, original DELETE operation, complete post-delete
   observations and separately reviewed accounting finalizer are still absent.

One-use local history blocks duplicate cooperating actors, not an attacker who
can start another binary, read credentials, restore old state or bypass the broker.
The isolation/authority boundary in section 3 is inseparable from this protocol.

## 5. Concrete milestones and acceptance gates

**M0, available safely now:** exact-source tests of unsafe defaults and hook
prerequisites; threat model, policy/path inventory and explicit refusal criteria.
This slice adds four actual constructor/feature/interpreter tests. They never
create a container, contact Docker, launch a supervisor or prove host isolation.

**M1, next non-executing implementation:** a separate append-only activation-audit
journal and crash/concurrent-process harness, with permanent attempt consumption,
immutable identity/sequence and restore/unknown-commit refusal. It must return only
journal facts, no launch/drain token. Do not connect it to a service or claim
all-entry isolation. Review the enrollment/anchor authority before implementing a
production activation decision; a plain file guard does not satisfy that review.

**M2, requires a disposable privileged test environment and approved image/runtime:**
implement the broker, sandboxed execution adapter and protected credentials/network
layout. Exercise actual kernel UID/mount/user/cgroup/IPC boundaries and negative
attempts: copied listener, stolen/replayed credential, Docker socket, host bind,
missing hook flag, PATH/module injection, symlinks, cgroup escape, manual/systemd/
agent/update restart, boot/PID reuse, concurrent supervisors, forged response
files and a pre-seal runtime request that completes after empty observations.
Do not install
runtimes, alter security settings or test a live runner to satisfy this gate.

**M3, requires actual artifact and service qualification:** pin the deployed source/
binary/agent/runtime; qualify the actual GitHub deployment and classic/broker/
scale-set path, one-job registration and consumption/control semantics. Test normal,
failed and owner-cancelled work through native post-job cleanup without retirement
inducing cancellation. Source mocks cannot satisfy this service gate.

**M4, separate retirement work:** external old-writer/accepted-backend frontier and
physical history/finalizer. M0–M3 do not release a charge or authorize deletion.

Until M1 authority, M2 isolation and M3 service/artifact gates are actually
established, there is no safe positive supervisor implementation/enrollment in
this repository. Existing launches may continue only under their existing scope;
they do not silently become eligible for automated retirement.
