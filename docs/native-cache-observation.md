# Kernel cache diagnostics, not a memory cap

The authenticated execution-owner `GET /v1/native/cache` reads the kernel-visible
OpenZFS 2.2.6 ARC statistics and configured maximum through fixed procfs/sysfs
paths. No input paths or setting changes are accepted. Each component is opened
without following symlinks, the final actual filesystem type and root ownership
are checked, reads are bounded, and boot identity/module version/configuration
are sampled before and after. Original sampling time expires after five seconds.
Missing, malformed, changed or unsupported inputs return unavailable, not zero.
An existing qualified owner boundary is mandatory before and after the read.

The response always says `arc_target_not_enforced_memory_limit`. It includes the
current size, target, minimum/maximum target and configured tunable (zero means
automatic tuning). Size above the target is valid diagnostic data. It does not
identify an individual guest, attribute memory to a cgroup, include all kernel
allocations or prove that cache pages have been reclaimed. Version text alone
is not binary provenance. The statistics are not an atomic all-memory snapshot.

Pinned [OpenZFS 2.2.6 ARC source](https://github.com/openzfs/zfs/blob/zfs-2.2.6/module/zfs/arc.c)
uses overflow allowances and advisory eviction before throttling. Its tunable
is a target rather than an enforced total-memory ceiling. Therefore these
observations cannot become schema overhead ceilings or authorize reservation
release. A separately configured conservative host-system reserve is an
operational policy, not proof of a hard bound. Strict activation/release must
remain blocked unless independent enforceable memory containment is established.

Tests use deterministic kernel-file fixtures and negative boundary cases. No
live host inspection, cache eviction, sysctl write or ZFS operation is performed.
