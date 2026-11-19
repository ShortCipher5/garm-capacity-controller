# Bounded disk preflight for offline native assets

Asset staging retains an upload copy and a final prepared copy. It is not charged
to capsule quotas and neither copy is deleted automatically. The fixed uploader
now checks the destination filesystems before consuming any payload:

- Upload bytes include the staging helper and all six assets. Final-copy bytes
  include the six assets, not a second helper.
- On one filesystem both outstanding copies are summed. On separate filesystems,
  each has its own required budget; both must pass. Both filesystem views are
  checked even when device IDs match.
- Each filesystem retains a fixed **1 GiB headroom**, plus a 1 MiB metadata and
  allocation-rounding allowance per outstanding copy. At least 80 free inodes
  must be reported (64 headroom plus bounded file/directory allowance).
- Root-reserved blocks are excluded. Missing, invalid, read-only, unexpectedly
  large allocation-unit or insufficient filesystem observations refuse staging.
- Upload checks repeat after approximately every 8 MiB and before starting the
  helper. The updated helper checks the whole outstanding final-copy budget
  before creating its destination, before each 1 MiB copy chunk, and before its
  completion receipt.
- Root-owned, no-follow, nonblocking locks serialize cooperating uploads across
  IDs and cooperating helper copies across IDs. Inspection creates no lock and
  requires no writable-space budget.

These are **observations, not storage reservations**. Other processes, changing
quotas, filesystem overhead, delayed allocation, or concurrent direct helper
invocation can still consume capacity between observations. The upload and
helper locks have distinct scopes; they do not exclude unrelated host writers.
No code here proves physical capacity, authorizes launches, or promises an OOM/
ENOSPC-free host. Partial bytes are retained on every uncertain outcome, and the
existing one-use upload history still forbids retrying a consumed ID. Even a
preflight refusal after local acceptance must not be treated as proof that the
remote attempt had no effect.

Deploy the newly built, separately hash-approved helper to obtain its per-chunk
checks; an older pinned helper does not acquire new behavior automatically. The
fixed upload script digest and helper hash are part of exact reviews. Existing
reviews/registrations cannot silently adopt changed bytes; preserve their
history and resolve upgrades explicitly, never delete history or reset an ID to
make a new review pass.

Tests exercise same/separate filesystem accounting, unavailable statistics,
reserved-space and inode limits, concurrent locks, payload refusal, mid-stream
free-space loss, preserved partial output and unchanged read-only inspection.
No real host is contacted by these tests.
