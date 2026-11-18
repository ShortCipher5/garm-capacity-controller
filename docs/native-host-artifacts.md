# Fresh host-owned artifact quota

`nativezfs.CreateHostArtifactRoot` creates one fresh host-only ZFS dataset with a
finite quota, mounts it at the fixed short `/run/garm-ctl`, copies the reviewed
runtime image by exact length and SHA256, and creates shared ids/state/zfs/egress/
devices registries. It never delegates this dataset to a user namespace. The
root-local zoned=off property, root-only paths, actual held mount identity and
pool/root GUIDs are rechecked. The observation is local evidence, not a caller
host label or admission capability; an authenticated enrolled owner must join it.

The fixed host-global `/var/lib/garm-capacity/host-artifact-birth` directory must
already be root-owned0700. Its exclusive fsynced attempt record is permanent and
precedes dataset/mount effects. A second setup is refused, even after partial
failure. No reset, import, migration, adoption, retry, unmount or destroy is
provided. The outside latch and external deployment source image, executable
inputs, TLS/configuration files and bpffs kernel objects remain explicit host
system reserve, not falsely covered by this disk ceiling.

All copies of the actual root capability share a retained mount descriptor and
failure latch. A failed observation or Close permanently invalidates them; Close
only drops the descriptor. The returned paths are derived from the checked root,
not arbitrary directory configuration. Runtime image data is copied rather than
reflinked; any sharing performed internally by ZFS still remains under the full
permanently reserved quota. The image is root0444; its opened-copy SHA256 and
subsequent inode/size/time identity checks assume trusted host root does not
maliciously rewrite it. Guest namespaces receive only the read-only image.

The shared global UID registry must remain global under this root. Per-domain
registry replacement would break anti-reuse history. Consumers must retain this
actual capability and use all derived paths before claiming artifact containment.
Pool load identity changes are refused; automatic restart reconstruction is not
implemented. Production mount/nspawn qualification is not claimed. Tests exercise
fixed grammar, bounds, exact copied bytes, cancellation, fabricated capabilities,
shared closure and Unix socket path length without mounting or contacting hosts.
