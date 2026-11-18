# Native device boundary (dormant construction)

`nativedevice.Install` is called only before capsule birth in an independently
managed, finite, private parent cgroup containing solely the owner process and
no descendants. It creates a fresh consumed attempt, loads a fixed deny-default
`BPF_CGROUP_DEVICE` program, attaches it to that actual held cgroup FD, and pins
the kernel link in a trusted bpffs directory before returning a capability.

Allowed character devices are null/zero/full/random/urandom, tty/ptmx, the first
Unix98 slave PTY major, and the actual `/dev/zfs` device confirmed against its
sysfs identity. ZFS permits read/write only. Block devices, host console, kvm,
tun, mem and all unspecified devices are denied. The policy is not configurable
by an owner request or runner. Failure on an unsupported required device stays a
failure rather than broadening the allowlist.

Rechecks reopen the pin, compare actual link/program/cgroup IDs and query the
kernel's effective device program list. Failure latches across copied values.
There is no unpin/detach API. Pinning preserves the filter after owner-process
loss; it does not preserve a removed cgroup or survive host reboot. Those cases
must never be treated as adoption/replay authority.

`nativecapsule.Start` now requires this opaque matching boundary before any
launch. `Running.CheckScope` rechecks it before native operations. The active
owner refusal remains. The separate manager service's eventual constructor must
prepare the trusted bpffs pin directory and durable registry and call Install;
no service unit or live BPF state is changed by these source additions.

Linux's cgroup link creation uses cumulative ALLOW_MULTI semantics. Descendant
programs cannot override this ancestor denial. Source contracts:
- https://github.com/torvalds/linux/blob/v6.12/include/uapi/linux/bpf.h
- https://github.com/torvalds/linux/blob/v6.12/kernel/bpf/cgroup.c

The tests execute the generated instruction subset in a private interpreter and
check exact kernel-record parsing. They do not load a BPF program or qualify a
real target kernel. Host root remains trusted and can deliberately remove pins,
change cgroups or device nodes. No device observation grants runner admission,
job completion, deletion, or capacity release.
