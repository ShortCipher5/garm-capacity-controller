# Conservative native-domain overhead accounting

Schema 26 adds an immutable, audited charge for actual enforced whole-capsule CPU, memory and writable-storage ceilings. Input is an opaque fresh pinned-mTLS enforcement observation matched to the enrolled owner/native identity and host binding in the same transaction. It is not an operator-provided resource assertion.

CPU uses the finite milli-CPU ceiling. Memory and disk round upward to MiB. Runner count is zero. Each domain keeps its dimension-wise maximum across revisions; smaller limits, stale evidence, restart and backup/restore do not remove its historical charge. An incompatible birth or binding is rejected. No measured-usage subtraction is performed.

Global and host allocations include this charge, while pool allocations do not. Therefore the accounting invariant is `sum(pool allocations) + domain overhead == global allocations`, and `sum(host allocations) == global allocations`. Planners must subtract the verified overhead from global/host budgets without assigning it to a synthetic pool or runner. Active reservations and unknown operations remain charged separately, so this conservative policy can double-count live guests and leave no spare capacity.

History remains permanent. Validation scans history in bounded batches of 128, with at most 16 KiB per record and 1,000 live domains. Memory is bounded; total scan cost is not constant. Insertions that would exceed these bounds or overflow canonical resource arithmetic roll back atomically. Genuine over-budget debt remains visible and blocks new admission. Old admission/dispatch SQL accounting markers are rejected.

This component does not authorize physical release. The capsule ceiling does not by itself bound immutable images, controller files or other host-side artifacts outside that capsule, prove per-guest ownership of shared storage, or establish complete containment. No absence, deletion, completion or release capability is inferred. Supported backup/restore preserves every overhead and guest charge and closes readiness; arbitrary raw rollback and competing unfenced controllers remain unsupported.
