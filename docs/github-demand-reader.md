# Bounded read-only GitHub demand transport

`demandrepair.NewGitHub` accepts an explicit web-authority to exact HTTPS API endpoint mapping, repository
mapping, verified CA trust and an injected credential-reference resolver. It has
only read methods. No real credentials, secret storage, app installation,
permission changes, scheduler or runtime activation are included.

`Read` enumerates repository workflow runs in a run-created interval, then job
pages using `filter=all` (all attempts). `ObserveGitHub` feeds those observations
into the conservative adapter; stock demand is not deleted because jobs disappear.

Transport protections: exact constructed paths, strict repository allowlist,
verified TLS, no redirects or environment proxies, bounded response bodies,
request deadlines, run/job/page limits, duplicate-ID rejection, expected run and
repository checks, and stable total-count checks across pagination. API errors,
rate-limit responses, Retry-After or exhausted rate quota fail without partial
results. Backoff/retry belongs to the outer durable reconciler, not a busy loop.

## Supported API semantics

- [List repository workflow runs](https://docs.github.com/en/rest/actions/workflow-runs#list-workflow-runs-for-a-repository)
  requires Actions read permission for fine-grained tokens and caps filtered
  searches at 1,000 runs. The transport rejects totals at or above that ceiling.
- [List jobs for a workflow run](https://docs.github.com/en/rest/actions/workflow-jobs#list-jobs-for-a-workflow-run)
  supports `filter=all` and paginated job results. Requests pin REST API version
  `2026-03-10`. Only queued/in_progress/completed jobs are currently understood;
  other statuses fail closed rather than being silently discarded.

## Not completeness or production readiness

This does not discover old queued runs created before the requested interval.
Initial backlog enumeration and a durable overlap/checkpoint strategy remain to
be implemented. Pagination is not an atomic snapshot: totals/duplicates detect
some drift, not every replacement or state transition. EnumerationComplete is
still only successful bounded traversal, not complete missed-webhook coverage.

A configured enterprise API must support the pinned version/response contract;
this code does not auto-downgrade. Every repository Host must exactly match the single configured web authority.
That authority is explicitly bound by operator configuration to the one API
endpoint; mixed or mismatched authorities are rejected before credential lookup. No host is inferred
from an untrusted job. Credentials are resolved per request and never returned in
errors; resolver failures and HTTP response bodies are not surfaced to callers.

Remaining: operational credential configuration, authoritative repository mapping,
initial backlog, durable checkpoints/rate-limit scheduling, conflict resolution,
grant reconciliation and activation wiring. Tests contact only local synthetic
TLS fixtures. No live GitHub/GARM/Incus infrastructure was accessed by tests.
