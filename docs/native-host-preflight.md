# Read-only native host preflight

`capacity-native-preflight` accepts no arguments and prints a JSON prerequisite
report. It never installs, starts, stops, mounts, creates namespaces, changes
privileges, calls ZFS, contacts a service or enables the native owner. Exit zero
means the report was produced, **not** that the host is qualified. Its overall
state is always `unqualified_no_activation`.

The report reads actual uname, effective UID and UID mapping, effective process
capabilities, cgroup-v2 filesystem/controllers, OpenZFS module version and
available lockdown status. Missing permissions appear as missing/unsupported,
not as zero resource capacity. Kernel and loaded OpenZFS module versions are
`informational` observations, or `unavailable` when unreadable/empty. A separate
`zfs_module_presence` prerequisite records whether a loaded module exposes a
nonempty kernel version attribute; it is not proof of working storage. No host
minor-version allowlist decides compatibility. The verification candidate is
**Debian 13 / Linux 6.12**, not a version pin or an assertion of qualification.
Matching version text alone does not establish kernel/module provenance or ABI.

Incus is explicitly `not_exercised`: this command does not contact its daemon or
execute a binary merely to discover a version. Its actual running version and
capabilities must be observed in separately authorized verification. The exact
hash/version pins for supplied runtime artifacts remain unchanged, as do all
native backend and activation refusals. A different observed host ZFS version
therefore does not authorize mixing it with an incompatible pinned image.

The fixed host-side LXC library path is inspected without execution. Its actual
bytes must match the shared reviewed vendor SHA256 and size before imported
GLIBC version requirements are reported. A host library match does not prove
that the isolated image contains/maps it or supplies those dependencies. Missing
host-side bytes can also mean the candidate is present only inside a prepared
image; that image still needs independent inspection and boot qualification.

Every apparent prerequisite match is labelled `observed_prerequisite_only`.
Actual namespace construction, quota delegation, nftables, BPF attachment,
Incus/LXC image operation and complete dependency closure are explicitly not
exercised. The existing native activation guard is untouched. ARC remains a soft
target, so a configurable operational reserve cannot become hard-bound evidence.

Unit tests use injected kernel-file fixtures; the command is not run against a
live host as part of these changes. Capability presence does not prove a syscall
will be permitted by LSM, seccomp, lockdown or service-manager policy.
