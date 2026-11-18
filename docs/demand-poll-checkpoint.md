# Observation-only backlog polling and checkpoint

The GitHub reader now combines recent-history scans with all supported active-run
status scans without a creation-age filter. This recovers queued jobs in old or
retried runs. Overlapping runs are fetched once. Every query and the combined run
set have explicit conservative ceilings; exceeding them yields no partial result.

On Linux, `PollGitHub` connects the actual read transport, caller-supplied current
stock queue, retained prior observations and durable observation checkpoint. It
serializes concurrent callers with a nonblocking advisory file lock, rescans with
a configured overlap, retains unresolved prior queued items, and only advances
its checkpoint after complete successful enumeration and atomic durable storage.
The exact API endpoint, credential reference, repository mapping, initial interval
and overlap are bound to the saved history. Mismatches fail closed.

A private0700 operator-owned directory and0600 regular single-link files are
required; symlink checkpoint/lock files are refused. Writes use a private temporary
file, fsync, atomic rename and directory fsync. A failed or incomplete poll does
not replace prior history. A minimum successful-poll interval prevents immediate
repeat success polling. Failures still require an outer rate-limit/backoff
scheduler; this is not implemented by the minimum interval.

## Authority and scope

The checkpoint records only observations. It does not permit runner creation,
release charges, drain jobs, bind dispatch requests or advance a grant ledger.
Missing checkpoint history starts from configured InitialFrom and repeats active
backlog scans, not from now. Restoring/copying a checkpoint does not grant runtime
authority. The directory and local writer remain operator-trusted, not a security
boundary against a privileged attacker.

Historical queued observations are removed only when this complete successful
reader observation positively reports the same job as `completed` and the current
stock queue no longer claims it. Current stock/reader disagreement remains visible.
In-progress or missing jobs remain retained; old checkpoint `ReaderStatus` never
substitutes for fresh evidence. This prevents confirmed completed history from
accumulating forever, but unresolved or terminal jobs outside the reader's window
can still reach the bounded history limit and stop polling. Conflict resolution
and targeted lookup of those historical jobs remain runtime integration work.
The result is not safe to feed directly to allocation without that reconciliation.
Removing an advisory demand item never releases a runner or any resource charge.

Polling is non-atomic across GitHub/GARM. Status transitions can escape one set of
queries; overlap and future active scans improve observation, not absolute event
coverage. There is no daemon, configuration/credential UI or automatic activation.
The caller must supply fresh scoped stock queue data and schedule retries/backoff.

Current stock input and checkpoint history are distinct provenance. A previously
reader-only job never becomes stock-confirmed merely because a checkpoint was
reloaded. `Source=history` identifies a retained item absent from the current reader;
`PriorSource` carries its earlier origin. Only current stock input may establish
`stock`/`stock+reader`. Consumers must honor Conflict/MissingFromReader flags.
