# Local runner disk telemetry: startup qualification only

The standalone command currently **does not listen or read TLS credentials**.
It can validate part of a reviewed local directory handoff, then refuses because
there is no independently verified Incus daemon-to-mount custody producer. No
existing service, Compose, installation or workflow invokes it.

An independently authorized startup owner would pass the already-open runner
dir-pool directory as inherited FD 3. There is no storage pathname argument,
privilege change, namespace switch, or path-discovery fallback. Only the exact
opt-in argument **serve-reviewed** reaches startup checks. The fixed owner-only
/etc/capacity-runner-disk-telemetry/review.json is canonical one-line JSON with an
optional final newline and fields in this order:

    kind ("reviewed-local-runner-disk/v1")
    scope (object: host, pool, boot, mount_namespace, unique_mount_id, device_major, device_minor)
    directory_inode
    expires_at
    server_key_sha256
    client_key_sha256

Both hashes are distinct lowercase hexadecimal SHA-256 SPKI values, numbers are
JSON integers, and expiry is canonical UTC RFC3339Nano, no more than 30 minutes
ahead. Unknown, duplicate, missing or reordered fields and expired leases
are refused. These are reviewed expectations, not authority conferred by JSON.

The command compares the actual current kernel boot ID and locked-thread mount
namespace to those expectations. The same held descriptor must show the reviewed
inode and device. Its **ordinary** mount ID must resolve to exactly one current
ext4 mountinfo row on that device; its separately measured **unique** mount ID
must match the review. The two ID types are never compared to each other.
Unsupported kernels, malformed mount state, invisible mounts, and mismatches
do not satisfy the preflight. This is a local snapshot, not an attestation
against a privileged actor changing the host.

Before enabling the guarded server composition, a separately reviewed and
enforced supervisor must link the current authorized Incus process lifetime and
mount namespace, the exact loaded provider/project/dir-pool route, and the
identity of the held descriptor to that daemon's visible mount. A pool name,
the caller's namespace, an owner-file assertion, or even both mount ID checks
alone cannot substitute. Replacements and stale evidence must withdraw the
handoff. Ext4 project quotas can affect descriptor counters; the result does
not establish whole-mount headroom, future placement or scaling authority.

The unwired server composition is loopback-only (127.0.0.1:9447) with TLS 1.3,
verified client authentication, the exact separate client SPKI and client-only
role, HTTP/1.1, and bounded sockets and request sizes/deadlines. A future
authorized rollout needs existing dedicated client CA and server credentials,
validated through the owner-only secure file loader, and independently
qualified end-to-end TLS pass-through if used remotely. The source neither
creates a route nor installs credentials. Lease cancellation can withdraw the
listener; closing it would not prove an in-flight kernel read stopped.
