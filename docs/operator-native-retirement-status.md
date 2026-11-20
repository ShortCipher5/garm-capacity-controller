# Native retirement process status

The existing Lifecycle section has a read-only status disclosure backed by the actual authenticated `/admin/api/v1/native-retirement-status` route. It reports the last process cycle, separately from the coherent ledger snapshot and physical host observations.

The backend's `processing` label means a completed cycle reported no unresolved outcome; it does not identify currently in-flight work. The UI explicitly says this and shows the last cycle timestamp separately from browser receipt time. Zero time means no completed cycle. Counts are per-cycle process results and reset after restart; `OperationsCompleted` means STOP/DELETE protocol outcomes, never physical release or refunded capacity.

No enable, retry, reset, release or configuration action exists here. Failed reads remove prior results; closing/disconnecting aborts and fences late responses. The UI requires both release-authorized fields to remain false. Production runtime refusal and durable recovery are enforced by the actual backend, not inferred from this display.

The response classifies the completed cycle at fetch time. After the configured interval plus the two-minute cycle deadline, counts are stale and shown only as historical; a future server timestamp is unverified. No result is fresh after restart until a cycle actually finishes. Freshness never cancels an unresolved result or confirms worker liveness, durable readiness, or release. Older responses without a freshness field are treated as historical.

The same authenticated GET may include `capacity_reuse` when a supported cycle
exists. This is an explanation from that exact process-local status, not a new
host or ledger observation. `historical_only` reports a stale/unverified clock;
`last_cycle_diagnostics` distinguishes missing canonical root fences, unresolved
operation outcomes, and unavailable retained diagnostics. Available retained
diagnostics explicitly remain **not release proof**, even after successful
STOP/DELETE receipts. `reuse_authorized` is always false. No cycle means the
field is omitted, not that capacity is free. These reasons never change charges,
permit replay, or relax the existing production runtime refusal.
