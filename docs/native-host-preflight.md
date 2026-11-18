# Read-only native host preflight

`capacity-native-preflight` accepts no arguments and prints a JSON prerequisite
report. It never installs, starts, stops, mounts, creates namespaces, changes
privileges, calls ZFS, contacts a service or enables the native owner. Exit zero
means the report was produced, **not** that the host is qualified. Its overall
state is always `unqualified_no_activation`.

The report reads actual uname, effective UID and UID mapping, effective process
capabilities, cgroup-v2 filesystem/controllers, OpenZFS module version and
available lockdown status. Missing permissions or unsupported versions appear
as missing/unsupported, not as zero resource capacity. The current source profile
is Linux 6.8, OpenZFS 2.2.6 and the separately pinned amd64 Debian LXC candidate.
Matching version text alone does not establish kernel/module provenance or ABI.

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
