# Offline completed-image integrity inspection

`capacity-native-image --inspect-completed=/protected/build-output
--expected-manifest-sha256=<approved digest> --expected-image-sha256=<approved digest>`
reads the existing image compiler output without running or mounting an artifact.
It checks the canonical manifest and completion record, requires the exact image
size, and hashes both runtime.raw and reproducibility.raw from held protected
regular-file descriptors. Supply digests from the independently reviewed build;
copying them from an untrusted receipt does not establish provenance. Interrupted
or incomplete builds stay incomplete. Inspection never repairs or resumes them.

A successful result retains `BootQualified:false`. It proves matching local bytes,
not that the filesystem implements the inventory, is bootable, or safely contains
Incus. Trusted root can still mutate files after inspection; this is not a durable
capability or protection against privileged concurrent mutation.

The current SSH preparation bundle does **not** contain or transfer runtime.raw.
Its configured artifact image path must already hold the separately supplied
matching image on the target. Pinned host nspawn, ZFS/zpool, ip and nft tools are
also preexisting dependencies, not installed by configuration registration or
preparation. The owner checker validates configuration, not availability or live
qualification of those dependencies. Neither this inspection nor preparation
opens the unconditional owner/runtime activation guards. Image transfer requires
a separately reviewed bounded protocol; do not add it to the existing 512 MiB
flat preparation stream or treat a prepared unit as deployable.
