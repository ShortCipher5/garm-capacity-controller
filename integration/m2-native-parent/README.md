# VM-only native parent SOURCE: compile/static checks, not an executable actor

This is the next concrete test-only native parent for the fixed #58 post-entry
witness. **It has NOT been linked or run.** The default recipe emits two mode0400
ET_REL objects with entry address0 and no program headers, not a parent
executable. There is no executable bootstrap, run/link command, daemon, Go/CGO
library integration, arbitrary-payload port, enrollment or authority constructor.
No app imports this test. No cgroup/native/privilege operation runs in its tests.

## Compile-only work that actually runs now

```sh
# NEW private directory; do not execute any object or link a parent here.
integration/m2-native-parent/build-objects.sh NEW_ABSOLUTE_PRIVATE_DIR
go test -race -count=1 ./qualification/m2parent
go vet ./qualification/m2parent
```

Only the existing compiler/binutils and the already-reviewed witness BUILD
recipe run. The witness ELF is embedded as readonly object data and is not
executed by these tests. Tests check ET_REL/non-executable parent objects, no
interpreter/runtime dependencies except the two fixed-blob symbols, exact
embedded witness bytes and source/object hashes, NEW-only output/symlink refusal,
and bounded source syscall/control-flow shape. These are STATIC checks, not
native launch, kernel failure-path or guest-isolation tests. There is no runtime
flag that activates a native parent in this test package or build recipe.

## Native control flow proposed for the separately approved guest

Only a future separately reviewed **single-threaded freestanding** guest
bootstrap may call `native_parent_vm_test`. It must establish the C ABI/cleared
DF/aligned stack and exit on return. Do not link this raw-clone source into a
Go/CGO, multithreaded process, production supervisor or existing runner.

External guest setup must provide controlled stdio plus FD3(write report pipe),
FD4(read gate pipe), FD5(the specifically authorized fresh cgroup-v2 domain LEAF).
The source verifies pipe roles/distinctness, cgroup2 filesystem/type and observed
empty direct membership. These are NOT permissions, whole-tree absence or
exclusive-writer proof. Fresh leaf/no descendants/sole authority is an external
approved-VM prerequisite; no supplied path, claim or `qualified:true` can meet it.
The source never creates/deletes cgroups or writes their controls/settings.

1. Copy ONLY the fixed compiled-in witness bytes into an own executable memfd,
   set mode0500, apply and verify WRITE/GROW/SHRINK/EXEC/SEAL seals and size/type.
   No caller image/path/argv/env can be selected. Denied/unsupported MFD_EXEC or
   seal policy refuses; no sysctl/security change, flag downgrade or path fallback.
2. Create private pipe gates and make exactly ONE clone3 attempt with precisely
   CLONE_INTO_CGROUP | CLONE_PIDFD, targetFD5 and a birth-time pidfd. Even EINTR or
   an ambiguous failure never retries as clone/fork/vfork/fork-then-move.
3. The fixed child closes its cgroup descriptor, maps only witness pipes to FD3/4,
   emits a pre-exec marker and waits. Parent holds target/pidfd, checks child
   still waiting and exact direct membership, and reports a bounded observed
   phase with `birth_qualified=false` / `execution_authority=none`.
4. Only the external guest experimenter's first fixed gate byte releases this
   pre-exec wait. The child marks all other descriptors CLOEXEC and calls
   execveat(AT_EMPTY_PATH) on the sealed known-byte image with fixed argv/env.
   No retry by filename, shell, wrapper, different syscall or payload exists.
5. Parent checks the bounded witness header/PID/false-authority suffix, held
   child/pidfd and direct membership, then forwards the post-entry report. A
   SECOND fixed external byte releases the fixed witness's own natural-exit gate.
   Parent observes normal child exit0 with waitid(P_PIDFD); there is no signal or
   kill operation. A denied exec exits the fixed child; it does not retry.
6. On errors close only private gate writers and boundedly observe this fixed
   child's natural EOF/timeout. Never start another child, kill real work, clear
   a ledger charge, delete a group or report absence/reusability. Soft deadlines
   do not guarantee kernel/scheduler completion. A broken report pipe may cause
   ordinary SIGPIPE termination; no signal syscall is sent. Any partial/lost
   phase, parent crash or unknown result is unresolved, not a success permit.

All of this native control flow is SOURCE-REVIEWED/COMPILED ONLY, not executed
here. A successful future return would mean just the fixed test scenario, not
one-use durability or authority. The source can be invoked again by an external
caller; it has no independent rollback anchor and must not be sold as no-restart
fencing. No existing admission or runner attempt becomes reusable from its result.

## Exact missing evaluation environment and artifacts

This fixture provides NO disposable guest, cgroup delegation or approved native
execution environment. Ordinary build/CI success, a container root shell or a
host metadata survey is not a substitute. No parent was linked/run and no
runtime, device, daemon, guest or security setting was started/changed to provide
such an environment. Platform-specific availability observations are not portable
qualification evidence.

Required separately before native linking/running:

- An expressly approved throwaway x86_64 Linux guest, never a live runner or
  production host; record exact vendor image provenance/digest and maintained
  kernel/package version. This source REQUIRES6.3+ memfd execution/EXEC seals,
  clone3 INTO_CGROUP/PIDFD, close_range(CLOEXEC), execveat, statx and waitid(P_PIDFD).
  Combining #55 sender transport additionally needs6.5+. A maintained6.12-LTS or
  newer vendor-patched baseline is a recommendation, NOT a selected/approved image.
- Unified cgroup-v2 domain hierarchy, specifically authorized writable bounded
  test leaf with no descendants/foreign writer, trusted namespace/proc views,
  fixed approved test-control identity, and policy allowing these exact native
  primitives. Do NOT disable seccomp/LSM, relax memfd policy or alter host security
  when a call is denied. Missing support must remain a refusal, not a fallback.
- Record source/build hashes for the separately reviewed guest bootstrap, parent
  objects and exact embedded witness. Object manifests record hashes, not signed
  measurements, image enrollment or same-UID/path-replacement defense. The future
  parent itself still needs qualified immutable execution/provisioning.
- No live jobs, registrations, credentials, host sockets/bind mounts/devices or
  passthrough; bounded guest CPU/RAM/disk and no production endpoints. VM creation,
  image installation, root/test delegation and native run need separate approval.

Future guest tests must exercise positive birth/FD-exec and negative missing,
closed, wrong-type, threaded/non-domain, nonempty, undelegated/denied target,
MFD_EXEC/seal/exec/clone3 policy failures, lost/wrong/EOF/late gates and abnormal
child/parent exits. Assert no intended-payload sentinel on refusal, but NEVER
infer no native attempt from start failure. Parent/orchestrator must independently
bind artifact, cgroup and pidfd; child text is not identity or membership authority.

This is not arbitrary-work isolation: the child retains the test caller's IDs/
capabilities and runs only a fixed benign witness. There is no UID/capability/
namespace/network/mount/seccomp job sandbox, every-runner-entry guard, service
assignment/consumption or accepted-runtime frontier. External rollback/old-writer
fences, installed artifact qualification, physical history and final release
remain absent. No launch, drain, deletion or capacity-release authority is enabled.
