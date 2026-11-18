# Offline missed-webhook reconciliation adapter

`internal/demandrepair.Observe` accepts an explicit GARM repository identity to
GitHub host/owner/repository mapping, a time interval, the stock queued baseline,
and an injected read-only `Reader`. There is **no GitHub HTTP implementation or
live credential integration** in this change. Tests use in-memory readers only.

The reader contract must enumerate all relevant workflow runs, job pages and
attempts in the requested interval for exactly the mapped repository. A real
reader must reject permission failures, rate limits, hidden/truncated pagination,
and incomplete nested run/job enumeration. A caller-provided terminal marker is
not proof that GitHub exposed every event. `EnumerationComplete` only reports that
the supplied reader terminated all requested page streams successfully.

The adapter adds positively observed queued jobs, deduplicated by mapped repository
and job ID. Existing stock queued jobs never disappear because of reader absence,
terminal status or changed labels. Missing rows are marked `MissingFromReader`;
status/label disagreement is marked `Conflict`, with stock data retained. Such
items must not be treated as settled runnable demand by a future allocator.

Pages, jobs, labels, cursors, identities and traversal count are bounded. Cycles,
duplicate job IDs, unknown statuses, cancellation and malformed/partial pages
return no result. The reader must honor cancellation and bound transport bodies;
the interface cannot force a malicious or broken implementation to return.

## Remaining integration

- Implement and qualify the actual read-only GitHub transport, exact identity
  resolution and explicit scoped credential configuration.
- Include queued jobs older than a polling interval; overlapping polls alone do
  not establish a complete initial backlog. Handle workflow attempts and all
  nested pagination without truncation.
- Persist reconciliation checkpoints only after complete successful enumeration;
  recover interrupted polls and API rate limits without erasing prior demand.
- Resolve uncertain stock/reader conflicts before consumption, merge repeated
  observations with durable grant state, and connect to complete pool eligibility.

No synthetic webhooks are sent. No durable checkpoint is claimed. No physical
resource charge is removed, runner retired, pool settings changed or live grant
created. This adapter deliberately does not claim full missed-webhook coverage.
