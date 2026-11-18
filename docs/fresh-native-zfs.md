# Fresh host-owned ZFS births

`nativezfs.CreateFresh` is a root-side construction step, not an activation gate.
It requires the non-reconstructible `capsulepeer.Peer` from the actual paused
helper's mapped-UID Unix connection, held pidfd and kernel namespace checks.
Neither JSON identity, a caller PID/path nor a dataset listing can mint that
handle. The package does not launch the capsule or resume it, and the peer alone
does not prove complete nspawn confinement.

Deployment supplies a root-controlled registry directory (0700), exact existing
trusted root dataset GUID, positive aggregate quota and root-owned executable
and `capacity-native-zfs-exec` helper with explicit SHA-256 pins. Every executable/registry ancestor must be root-owned,
non-writable by other users and nonsymlinked. Set-ID files and file capabilities
are rejected. The supported tool/kernel report is exactly `zfs-2.2.6` and
`zfs-kmod-2.2.6`; other packaging/version output fails closed pending review.
The trusted executable and dynamic libraries must not be modified by root during
execution. The pinned child helper closes every descriptor above FD4 at exec, preventing
ambient supervisor sockets from reaching ZFS. There is no shell, arbitrary
browser command or PATH-selected binary.

A fsynced exclusive directory keyed by capsule birth consumes the attempt before
creating `ROOT/domain-BIRTH`, then `runtime` and `pool` children. The aggregate
parent is created with finite quota, `zoned=off`, `mountpoint=none`, `canmount=off`;
children are fresh legacy/noauto datasets. Before delegation, only the runtime
child is temporarily mounted beneath its exclusive consumed registry directory
with nodev/nosuid/noexec. Actual kernel mount ID, exact source, fresh GUID and
empty filesystem root are checked; the held root descriptor is chowned to the
authenticated mapped UID/GID, chmod0700 and fsynced. Normal exact unmount and
restoration of the original placeholder mount/inode are verified before
setting the parent `zoned=on` and delegating. No force/lazy or failure cleanup
unmount exists. The helper is revalidated as paused before and after construction. Each GUID/type/quota/property readback is
strict and bounded. No `-p`, adoption, recursive delete or cleanup is performed.
The parent is delegated with `zfs zone /proc/self/fd/4 DATASET`, where FD4 is a
duplicate held actual user-namespace descriptor, not a reopened PID path.
Successful exit is followed by quota/GUID/root readback and live-peer checks,
then a durable completion record. This is birth provenance, not a capacity permit
or proof of usable storage inside a running Incus instance.

The OpenZFS 2.2.6
[zone contract](https://github.com/openzfs/zfs/blob/zfs-2.2.6/man/man8/zfs-zone.8)
keeps the quota outside namespace modification. The separate disposable `pool`
child is the only intended Incus storage source: Incus may delete its source
while rolling back creation. Never pass the aggregate parent or `runtime` child.

Any failure after the attempt record, lost command response, process death,
quota drift or failed completion fsync returns unknown. The registry and any
created datasets remain permanently consumed. There is no automatic retry,
restart adoption, unzone or deletion API. Closing the peer does not kill it.
Operator reconciliation and recovery are not implemented by this package.

Tests use a fake command runner and temporary journal files only. No host ZFS,
namespace delegation or actual kernel/Incus installation was exercised. Real
nspawn mount/device/cgroup construction, dataset mounting, Incus bootstrap and
controlled egress must still be integrated and independently qualified; existing
native control activation refusal remains unchanged.
