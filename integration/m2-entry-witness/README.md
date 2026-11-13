# Fixed post-entry witness: real executable, not a launcher

This is a tiny freestanding static Linux/x86_64 test executable. It observes its
OWN already-entered process, reports on inherited FD3, waits on inherited FD4,
then exits. No runner, workflow, native launcher, enrollment, assignment control,
service endpoint or release route is connected. No held audit/checkpoint or
resolver implementation is included. This is test instrumentation only.

## Artifact and bounded behavior

`build.sh NEW_ABSOLUTE_PRIVATE_OUTPUT_DIR` builds with the existing
`/usr/bin/gcc` and binutils. It downloads/installs nothing and NEVER executes the
artifact. Existing files/directories/symlinks/dangling links at the output are
refused before side effects; mkdir without `-p` refuses races. The caller must
protect the parent directory/ancestry. A failed new build is left private for
inspection, never silently reused or cleaned over another directory.

The output is a small ET_EXEC ELF64/AMD64 binary, with no libc/CRT, PT_INTERP,
dynamic segment or undefined runtime symbols, and a non-executable stack/no W+X
load segment. The assembly entry ignores argv/environment/auxv. Source/build/ELF
checks record the actual compiler version and artifact/source SHA-256 hashes.
These are RECORDED hashes, not preapproved security measurements or a guarantee
of reproducibility across compilers. A private file/mode/hash does not solve
same-UID replacement or pathname-exec TOCTOU. No installed artifact is qualified.

Before I/O the witness verifies that FD3/FD4 are distinct pipes with correct
write/read directions using descriptor-relative statx, then makes ONLY those
pipe endpoints nonblocking. It does not create/open a file, path or device.
Syscalls are confined to those inherited pipe descriptors, clock/poll/exit and
read-only self getpid/getresuid/getresgid/getgroups/capget/PR_GET_NO_NEW_PRIVS.
There is no clone, exec, signal, socket, namespace, mount, set-ID, capability-set,
no_new_privs-set, seccomp or cgroup mutation syscall. Pipe I/O flag changes are
not global permissions/security-policy changes. Pipe compatibility is NOT proof
of who provisioned the descriptors or who else holds them.

The report is at most 1024 bytes, uses a fixed format, and includes real,
effective and saved IDs, supplementary-group COUNT, no_new_privs and capability
masks. IDs/capabilities are namespace-relative, not host identity or isolation
proof. No raw environment, proc/cgroup path, credential, hostname or host address
is collected. It always reports `birth_qualified=false` and
`execution_authority=none`. Child text could be forged by another executable;
parent-side artifact/native-handle verification remains mandatory.

Only byte `0xa5` on the gate permits this fixed helper to exit zero. EOF, wrong
byte, I/O failure or a five-second soft observation deadline exits nonzero. A
closed report pipe can also naturally terminate this disposable helper through
kernel SIGPIPE; the fixture sends no signals. This gate is NOT a runner stop or
job-drain protocol and must NEVER be used on real work. Deadline/scheduler/kernel
delay or a failed helper yields no absence, retry, completion or release proof.

## Actual unprivileged evaluation and its strict limits

Build/source/ELF/hygiene tests run without executing the witness. After separate
source review and explicit approval of this bounded owned fixture only:

```sh
GCC_M2_ENTRY_WITNESS_TESTS=1 go test -race -count=1 -timeout=60s \
  -run '^TestM2Entry' -v ./qualification/m2entry
```

The opt-in tests use fresh private artifacts, owned pipes and a direct test child
under the caller's unchanged credentials. They change no host privileges or
native policy. They compare real/effective IDs, saved IDs expected after ordinary non-set-ID
exec, unchanged supplementary-group count and the parent read-only GET_NNP
observation, and validate capability-mask format (not a privileged exec capability
transition). They also test hostile
unused loader/environment strings, held parent gate, EOF/wrong byte/timeout,
regular-file/wrong-direction/same-pipe refusal and sentinel preservation. The
owned helper naturally exits; no kill, alternative command or fallback launcher
is used for cleanup. The outer test has bounded waits and reports failure, not
native absence, if natural completion is unavailable. A kernel/scheduler hang is
not given a false hard-deadline guarantee.

The helper has ALREADY executed the entry shim and C/runtime-free setup before
reporting. Neither a report nor these tests proves atomic cgroup birth, trusted
file-descriptor exec, full sandboxing, no other accepted work or runner/job
safety. A successful zero exit is only this fixed-helper test outcome. Required
hosted steps may execute this same ordinary unprivileged fixture, not a VM or
privileged kernel harness; they must not be described as M2 qualification.

## Minimal next disposable-VM test, with exact missing prerequisites

The next atomic-birth experiment needs NO Docker/OCI runtime: a separately
reviewed test-only native parent can use Linux clone3 INTO_CGROUP + PIDFD and
execveat/fd-bound verified ELF for THIS witness. That parent is NOT implemented
here. A positive broker/native launcher must not be inferred from this artifact.

Before any such native parent is run, provide an explicitly approved, throwaway
x86_64 Linux VM with: a supported security-patched vendor kernel with clone3,
CLONE_INTO_CGROUP and pidfd support (minimum 5.7 for these primitives; 6.12-LTS or
newer maintained/vendor-patched baseline recommended), unified cgroup-v2 domain
hierarchy and authorized test subtree/limits, trusted procfs and namespace views,
and an approved root/test-control identity confined to that guest. The full
sender-transport combination additionally requires SO_PASSPIDFD (6.5+); this
witness alone does not test that transport. Record exact vendor image provenance
and SHA-256, kernel package/version, native-parent source/build hash and witness
hash BEFORE evaluation. Those image/runtime records are NOT available here.

The guest must contain no live jobs/runner registration/production secrets and
have bounded CPU/RAM/disk, no host sockets/shared bind mounts/devices/passthrough,
and no production network credentials/endpoints. VM provisioning, software
installation, cgroup delegation or privilege/security changes are NOT authorized
or performed by this source/ordinary fixture task. A container root shell or a
host metadata survey is not a substitute for this explicit disposable environment.

Within that separately approved guest, a qualified native parent must hold the
cgroup and pidfd, independently verify the child/artifact/membership BEFORE gate
release and distinguish pre-exec setup from the first intended payload. Negative
cases must cover closed/wrong-type/undelegated/denied cgroup handles and denied
clone3/exec; no fork-then-move, pathname, wrapper or second-launch fallback. A
failed native start may already have created/setup a child, so the attempt stays
ambiguous/consumed and cannot be retried or released just because no sentinel
arrived. The external rollback anchor, every-entry isolation, queued-runtime
frontier, service assignment/consumption and physical retirement gates all remain
separate and unimplemented.
