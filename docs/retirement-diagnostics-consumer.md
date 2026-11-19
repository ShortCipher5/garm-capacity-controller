# Read-only post-delete diagnostics

After a canonical successful DELETE, the retirement service separately requests a fresh pinned-owner retained-observation report. The consumer requires the exact immutable DELETE receipt and enrolled native host identity, and checks that the canonical host binding did not change during the bounded read.

`RetainedDiagnosticsAvailable` and `RetainedDiagnosticsUnavailable` are last-cycle counters, separate from `OperationsCompleted`. Missing, expired, mismatched or unreachable evidence does not reverse a completed native operation, authorize another mutation, establish physical release, or reduce charged capacity. Unknown native outcomes are not eligible for this read. Reports are not persisted as lifecycle authority and are re-observed on later cycles/restart.

Production runtime refusal remains unchanged. TLS/SQLite protocol-double tests exercise correct and wrong-incarnation reports, restart and retained charges. They do not qualify actual kernel/ZFS reclamation or activate production.
