# Fresh-domain storage subbudgets

`nativezfs.Birth.SealBudgets` performs a one-use host transition after stock
Incus creates its initial datasets, but before any guest is admitted. It is
not an activation capability. Integration must withhold admission until this
opaque result is retained and rechecked alongside the other real boundaries.

The runtime budget covers Incus metadata, logs and TMPDIR together. There is
no independent log limit. Every stock category has an explicit positive budget:
buckets, containers, custom, images, virtual-machines, and their five deleted
counterparts. Their sum plus runtime cannot exceed the original parent quota.
Deleted image storage is not silently charged as free capacity.

The transition pins the existing fresh-domain GUIDs, durably consumes a seal
intent, sets local zoned/quota properties, removes the original broad domain
delegation, then attaches only runtime and the ten category roots using the
original held user namespace FD. Pool and deleted ancestors are never attached.
Quota and zoned properties must both have local sources. A failed Recheck
permanently invalidates all copies of the sealed handle; later readback cannot
revive it. All failures after intent are unknown, permanently consumed, with no automatic
retry, rollback, deletion or adoption. The original broad Birth.Recheck is no
longer a valid post-seal check; integration must retain SealedBudgets.Recheck.

This distinction is essential: a local zoned property alone stops a namespace
from changing that root's quota, but broad parent delegation can still permit
its destruction and recreation. Separate roots leave the parent read-only,
preventing recreation at the same category name. GUID drift fails closed.
A destroyed category can cause denial of service; sealing is not self-repair.

Source basis: OpenZFS 2.2.6 module/os/linux/spl/spl-zone.c implements separate
namespace entries with visible read-only ancestors; zfs_ioctl.c protects quota
changes at the local zoned setpoint. Stock Incus 6.0.4 driver_zfs.go applies
initial properties strictly during Create, but warns on failures for existing
datasets during Mount. Missing categories still fail. driver_zfs_utils.go
initialDatasets includes active and deleted categories. Image retention moves
between images and deleted/images in driver_zfs_volumes.go.

No real kernel/ZFS/Incus execution has been performed. Stock cross-category
rename/clone and restart behavior require supported-host qualification. The
seal does not prove that bytes were reclaimed, protect against trusted host-root
redelegation, enumerate namespace membership from properties, or authorize
canonical reservation release. Early pre-seal daemon work is bounded only by
the original aggregate quota. Exact readback of GUIDs, finite quotas and local
zoned sources follows the successful one-way commands; zoned=on is not itself
namespace-membership evidence.
