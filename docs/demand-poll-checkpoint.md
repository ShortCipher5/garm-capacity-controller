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

All previous queued observations remain conservatively retained. Confirmed
terminal/obsolete demand retirement and conflict resolution are not implemented;
long-running accumulation can reach the bounded history limit and stop polling.
The result is not safe to feed directly to allocation without that reconciliation.

Polling is non-atomic across GitHub/GARM. Status transitions can escape one set of
queries; overlap and future active scans improve observation, not absolute event
coverage. There is no daemon, configuration/credential UI or automatic activation.
The caller must supply fresh scoped stock queue data and schedule retries/backoff.
