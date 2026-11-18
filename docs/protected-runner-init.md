# Protected runner init: unfinished image integration

`capacity-runner-init` is an actual Linux guest PID1 executable, not a physical
completion verifier. It is not wired into provisioning, any runtime flag, native
release, or a GARM template. Do not deploy it as a replacement runner image yet.

The private JSON configuration binds domain/incarnation claims, an exact SHA256
of a root-owned ELF launcher, non-root UID/GID, private runtime directory and new
private journal directory. Claims must later be authenticated against the native
owner's immutable instance UUID and accepted create/start history. They are not
independently verified by this executable.

It refuses non-PID1/non-root startup, mutable or symlinked artifact paths, file
capabilities and privileged executable bits. It records and fsyncs launch_unknown
before starting. An existing journal refuses every replay, including a crash
before exec. The child clears groups and ambient capabilities, verifies empty
capability sets, sets no-new-privileges, closes inherited descriptors above its
pinned executable, and execs only that descriptor with a fixed environment and
no inherited stdin/stdout/stderr authority. The launcher must use separately
protected bootstrap credentials; the config contains no JIT token or shell code.

PID1 reaps all children and adopted descendants. It never forwards stop signals,
kills jobs, restarts a launcher, or attempts rollback. After every descendant is
reaped it records only process_tree_exited_unqualified, never job success or
release permission, and stays alive. A nonzero/signal exit is retained. A missing
process, timeout, stopped guest, or missing journal cannot substitute for proof.
This is PID ancestry, NOT independently verified cgroup containment. Trusted root
outside the guest remains in the threat model. Failures remain unknown.

Image builder prerequisites still to implement and qualify:

- Root-owned immutable runner assets and a pinned executable bootstrap launcher,
  with separate writable runtime/work paths. Stock runner bootstrap requires
  compatibility work; this component does not claim the default template works.
- No job sudo/root path, no writable trusted launcher/library paths, no sibling
  job namespaces or delegated cgroups, and a qualified dedicated PID namespace.
- Native owner binds protected image, namespace, exact instance UUID and journal
  to the canonical create/start history. Add authenticated host observation.
- A real runner acceptance fence and terminal job identity/result protocol. A
  listener exiting is not sufficient evidence of job completion.
- Durable deletion acceptance/terminal lineage and physical resource release
  integration, with independent restored-controller/old-owner fencing.

Local tests exercise durable publication/crash residue and real disposable
subprocess orphan reaping. They do not qualify Incus, PID1, cgroups, runner jobs,
SSH/hosts, or production lifecycle. No live host actions are part of this change.

## Root preparation implementation (not yet connected to execution)

`PrepareRunnerLayout` now implements actual dedicated-guest PID1 mount/file
preparation. It verifies the complete hashed bin/externals file inventory and
root-owned immutable paths, rejects nested mounts/symlinks/capability-bearing
files, consumes a fresh destination, marks guest mounts private, and establishes
read-only/nosuid/nodev bind mounts for those executable trees. It verifies mount
flags before preparing UID-owned `_diag`, `_work` and configuration files. The
runner owns its runtime root so normal credential creation/deletion is possible,
but cannot replace a mountpoint or unmount without privileges. A private opened
listener descriptor is retained before changing runtime-root ownership.

The JIT reader accepts exactly `.runner`, `.credentials`, and
`.credentials_rsaparams`, with strict base64 and duplicate-key rejection; runner
settings must match the expected name and require ephemeral, disabled updates,
`_work`, and no custom monitor socket. Unknown upstream file layouts fail closed.
Neither credentials nor JIT JSON are logged. Failed preparation is not rolled
back automatically; the existing destination permanently refuses another attempt.

This layout follows source inspection of
[Runner.cs v2.321.0](https://github.com/actions/runner/blob/v2.321.0/src/Runner.Listener/Runner.cs),
[HostContext.cs](https://github.com/actions/runner/blob/v2.321.0/src/Runner.Common/HostContext.cs)
and [ConfigurationStore.cs](https://github.com/actions/runner/blob/v2.321.0/src/Runner.Common/ConfigurationStore.cs).
That is a research source pin, NOT a recommendation to deploy that old release.
Runner derives its root from the entry assembly's bin parent, not cwd, and writes
JIT files and diagnostics there; therefore writable cwd alone is insufficient.
A currently supported runner release must be selected, integrity-pinned and
qualified with this contract before activation.

Preparation remains unwired: `Run` does not call it, no image build/download or
mount is performed by tests, and no runner has been launched. Apphost fd-exec and
assembly path resolution through the prepared mount require actual verification.
The layout is not yet bound to owner-authenticated native incarnation, accepted
operation history, or job terminal state. These remain real coding/integration
and qualification gaps, not optional checks to bypass.
