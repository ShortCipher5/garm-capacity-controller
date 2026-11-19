# Managed capacity evidence join

`LoadedStockBinding.ObservePoolCapacity` joins a real controlled stock startup
binding with the pinned, authenticated coherent ledger snapshot and opaque fresh
native-enforcement observations. It requires every enrolled pool host/domain,
exact owner certificate roles, policy and current host-binding sequence. The
canonical global, pool and host charges must fit their saved budgets; the full
saved host CPU/RAM/disk budgets must fit the actual finite native limits. Byte
limits are divided into MiB, rounding available capacity down without overflow.

The resulting private observation is short-lived. `Check` repeats the actual
loaded-stock check and authenticated ledger read, rejects any revision/profile/
accounting-digest change and revalidates the native observations. A stale or
serialized observation cannot be reused as permission. No caller-provided
maximum or resource budget is accepted.

This remains evidence, not an increase permit. The existing stock supervisor
continues to refuse positive maximum writes. Actual positive-write integration
must retain the existing durable settings journal, bind one pool/target to fresh
evidence at transport dispatch, keep every possibly-sent outcome UNKNOWN, and
require the real release-safe/fixed-overhead authority. The current finite
parent cap and a sampled whole-domain `memory.current` value do not prove that
released reservation bytes are physically reusable. An isolated fixed-overhead
boundary and correct protected storage accounting are still missing; no flag or
schema-presence check substitutes for them.

Tests mint native observations through a real local pinned mTLS endpoint and
exercise exact enrollment, role, host-binding, finite-budget, unknown-overbudget
and freshness rejection. No actual controlled stock restart or qualified host
execution has been performed in this development environment.

Schema 26 integration additionally requires a canonical retained overhead record
for each exact enrolled domain, matching the observed owner tuple and immutable
root-image/device/egress commitments. Retained CPU/RAM/disk must cover current
finite ceilings (bytes round up); a higher observed ceiling without a matching
ledger update is refused. This is still read-only evidence, not permission to
increase a stock pool. Whole-capsule overhead is deliberately conservative and
may consume all host budget; it cannot be treated as an independently enforced
small daemon overhead or subtracted from ordinary runner reservations.

This isolated port retains current main's canonical shared-artifact accounting:
the authenticated snapshot digest and allocated global/host totals include
artifact overhead. It does not replace snapshot validation, subtract overhead,
or connect this observation to a positive writer or transport dispatch.
