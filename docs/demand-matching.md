# Read-only cross-pool demand matching

`capacitybroker.MatchDemand` matches each deduplicated job at most once and each
caller-verified idle runner slot at most once. Augmenting paths prevent a flexible
job from unnecessarily consuming the sole pool available to another job. Stable
job-ID ordering makes results deterministic, not FIFO or historical fairness.
Input size and matching work are bounded; exhausted bounds return no partial result.

Unmet jobs retain their candidate sets. They must not be copied into every pool's
`Waiting` field. The result is advisory, not an actual runner/job association or
permit. Busy, creating, unknown and unresolved warm grants are not idle slots.
Nothing here releases resource charges or proves observation completeness.

## Remaining reconciliation integration

The stock demand reader's repository/provider scope is not a complete routing
graph. Before runtime use, the orchestrator must map GARM repository UUIDs to the
correct GitHub host/owner/repository identity, enumerate every eligible competing
pool, enforce observation freshness, and conservatively merge queue changes with
in-flight durable grants. This function does not make those assertions itself.

Missed-webhook repair is not implemented. It requires an authorized GitHub reader
with explicit repository scope, bounded complete pagination of relevant workflow
runs/jobs, API rate-limit handling and checkpointed polling. GARM's own stored
queue cannot reveal an event it never received. Absence from a response, elapsed
time, or a terminal GitHub job must never release an unknown physical runner.

A future repair adapter should retain source identity and stable job IDs, add
positively observed queued jobs idempotently, preserve disagreements as unresolved,
and only advance coverage checkpoints after complete successful enumeration.
Incomplete/rate-limited reads must not erase demand. Writing synthetic webhooks to
GARM or using provider rejection as ordinary queueing is not part of this design.

This PR adds only the pure matching primitive; no API permissions, live reads,
provider writes, settings writes, dispatch binding or production activation.
