# Native isolation construction: unresolved activation boundary

The owner control command refuses activation before launching any native process.
The private cgroup namespace, finite limits, and ZFS readbacks implemented so far
are **not** a complete native privilege boundary. A configured opt-in must not
turn those observations into execution authority.

## Why mount masks are not a fix

`ProtectSystem=strict` does not prevent connecting existing host Unix sockets.
The native daemon currently retains host privileges and a view of host process
namespace handles. Hiding `/run` or mounting another `/proc` in a private mount
namespace is insufficient while host `CAP_SYS_ADMIN` and backing mounts remain.
Do not implement a mask-only "exclusive" mode.

## Source-grounded candidate, not an implemented deployment mode

Pinned sources investigated:

- [Incus 6.0.4 container driver](https://github.com/lxc/incus/blob/v6.0.4/internal/server/instance/drivers/driver_lxc.go)
  has explicit paths for a daemon running inside a user namespace, and delegates
  ZFS access to unprivileged containers when supported. This is not evidence that
  this application's complete nested daemon recipe works.
- [Incus 6.0.4 ZFS driver](https://github.com/lxc/incus/blob/v6.0.4/internal/server/storage/drivers/driver_zfs.go)
  detects delegation support at OpenZFS 2.2 and still requires the ZFS utilities
  and kernel support.
- [OpenZFS 2.2.6 zone interface](https://github.com/openzfs/zfs/blob/zfs-2.2.6/man/man8/zfs-zone.8)
  describes user-namespace dataset delegation and protection of the delegated
  dataset's quota. This is different from merely exposing `/dev/zfs` to host root.
- [OpenZFS 2.2.6 namespace implementation](https://github.com/openzfs/zfs/blob/zfs-2.2.6/module/os/linux/spl/spl-zone.c)
  holds a namespace reference for the attachment. Dataset attachment is not a
  substitute for provisioning a new never-shared domain or handling unknown
  setup outcomes.

The candidate is a daemon in a fresh non-host user namespace, with a fresh
bounded dataset delegated to that exact namespace, and an independently managed
parent retaining the immutable physical-domain identity. No version alone is
accepted as proof that a kernel actually enforces this boundary.

## Required construction code

1. Reserve a fresh UID/GID range and dataset/domain birth together. Never map host
   UID/GID zero or reuse a range or dataset from an unknown prior launch.
2. Create the child in fresh user, PID, mount, IPC, network and cgroup namespaces
   before native code. Hold birth pidfd and namespace descriptors before reaping.
   Use a bounded setup handshake; lost acknowledgement quarantines the birth.
3. Build a detached root from a pinned complete native runtime image, pivot into
   it, detach the old root, and close every host directory/namespace descriptor.
   No backing host `/run`, proc namespace handles, root disk devices, host D-Bus,
   host `/sys` controls, or arbitrary bind paths may survive. Runtime dependencies
   cannot be taken from mutable host search paths.
4. Enforce a parent-owned device policy. Expose only required safe character
   devices and namespace-delegated `/dev/zfs`, never general host block devices.
   Set up native networking inside its own namespace, with explicitly scoped
   parent uplink connectivity instead of host network administration privileges.
5. Provision a fresh ZFS dataset, immutable parent-owned aggregate quota and exact
   GUID; attach it to the held user namespace and read back native visibility.
   No generic ZFS command proxy, host-root ZFS authority, or retry after uncertain
   attachment. Include writable native database/cache/log storage in the bound.
6. Bind the owner Unix socket to the actual mapped peer UID, birth pidfd and all
   held namespace identities. Existing root-UID socket checks are incompatible
   with the proposed mapped daemon and must not be weakened to PID-only checks.
7. Preserve parent cgroup ceilings, mapped guest resource constraints, owner-loss
   quarantine, and canonical one-use operation claims across this construction.
   Native restart/adoption or resource release remain separate capabilities.

## Unsupported until implemented and qualified

The current host-root native daemon, arbitrary installed Incus versions, ZFS
without verified user-namespace delegation, inherited/shared datasets, arbitrary
UID maps, and fallback to host networking are not supported production modes.
A dedicated VM is an alternative boundary, not an automatic fallback: it needs
its own hypervisor-authoritative resource/identity/lifetime integration. Neither
`systemd-detect-virt` nor an operator assertion establishes that authority.

The codec, stopped create/bootstrap/start adapters, and ledger work do not make
this construction complete. No guest start, native restart, daemon kill, domain
cleanup, ZFS delegation, or host configuration was performed to write this note.
