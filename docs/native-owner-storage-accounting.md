# Actual owner storage accounting connection

The owner now requires both the existing pinned ZFS quota reader and a separate
pinned zpool reader. Before its one-use START, it captures the real full-pool
reference baseline and retains the opaque tracker in that exact execution.
Capture failure consumes the start attempt and performs no START. No baseline
is imported, reconstructed after restart, or newly captured after workload use.

After a verified successful DELETE operation, an authenticated controller may
GET `/v1/native/retirement-operations/<claim>/storage-accounting`. This performs
fresh read-only observation against that original tracker. It returns HTTP409
when evidence is absent, references remain, quiescence/identity fails, or the
sampling window is stale. The successful response is explicitly
`accounting_only_not_release`, with original resource/domain/policy identity,
root quota/used/available and pool allocated/free/size. Full-pool dataset and
reference names are not exported. Pool free bytes are not writable root quota.

The original opaque observation is also retained privately for a future strict
physical join, but any join must recheck Accounting().Fresh() and exact identity.
This endpoint does not sign completion, release reservations, establish shared
extent ownership, or equate DELETE success with physical reclamation. Schema26
persistent whole-capsule overhead accounting remains a separate conservative
charge. Unresolved extents and host-side artifacts remain charged. The execution
boundary remains closed and no host operations were exercised by these tests.
