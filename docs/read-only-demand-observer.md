# Opt-in read-only demand observer

`capacity-demand-observer` connects the stock GARM demand reader, bounded GitHub
repair reader and durable observation checkpoint to a runnable polling process.
It is not the capacity controller's allocation loop. It has no pool-settings
writer, grant API, provider admission capability or physical lifecycle action.
Nothing installs, launches or deploys this process automatically.

On Linux, build `go build ./cmd/capacity-demand-observer`. Supply an owner-only configuration
file, owner-only token and CA files, and an existing owner-only 0700 checkpoint
directory. Use `-config <file> -once` for one observation (nonzero exit on failure),
or omit `-once` for periodic polling. SIGINT/SIGTERM cancels active network reads and retry
waits. Output writes rely on the process stdout consumer remaining responsive. Configure the process runner to restart it if stdout fails and enforce a bounded
stop timeout followed by a hard kill: a blocked stdout pipe is not cancellable
through the Go context. Do not use this stdout stream as a lifecycle authority.

Example configuration (replace placeholders with explicitly authorized endpoints,
repository mapping and file paths):

```json
{
  "enabled": true,
  "garm_endpoint": "https://garm.example",
  "garm_token_file": "/private/observer/garm-token",
  "garm_ca_file": "/private/observer/garm-ca.pem",
  "github_endpoint": "https://api.github.com",
  "github_authority": "github.com",
  "github_token_file": "/private/observer/github-token",
  "github_ca_file": "/private/observer/github-ca.pem",
  "providers": ["capacity"],
  "repositories": [{
    "GarmID": "11111111-1111-4111-8111-111111111111",
    "Host": "github.com",
    "Owner": "example-owner",
    "Name": "example-repository"
  }],
  "checkpoint_path": "/private/observer/history/checkpoint.json",
  "initial_from": "2026-11-01T00:00:00Z",
  "interval_seconds": 60,
  "max_backoff_seconds": 900
}
```

Use existing minimally scoped read credentials; this command neither creates
credentials nor changes account permissions. Certificate chains must validate
against the supplied CA roots. Redirects and proxies are disabled by the underlying
clients. Tokens reload from private regular files rather than appearing on the
command line or in emitted events. Configuration changes require a restart.

A poll has an overall 90-second cancellation deadline and the existing narrower
transport/read limits. No overlapping polls run in this process. Failures use
exponential delay from the configured interval to the configured maximum;
success resets the delay. Intervals and maximum delays must be between 60 and
86,400 seconds. Exact provider Retry-After handling and distributed scheduling
are not implemented. The checkpoint file lock rejects concurrent writers; it is
not a distributed controller ownership/fencing mechanism.

The checkpoint binds GitHub endpoint/repository mappings and also the configured
GARM endpoint/provider scope through the reader reference. Changing that scope
fails closed rather than mixing historical observations from a different source.
Token rotation without a scope change does not discard observations.

## Output and truthfulness

Each stdout line is a JSON summary with `mode: observation_only`, status, start
and finish timestamps, last-success timestamp, next-poll delay, and counts only
on success. Failure events contain no old counts. Upstream responses, endpoints,
repository names, labels, credentials and local paths are never emitted.

Success means a complete bounded observation and durable checkpoint, not an
atomic fleet snapshot, complete webhook coverage or usable capacity grant.
`atomic` and `missed_webhook_coverage` remain false. Supply counts are observations,
not proof of available host resources. `conflicted` and `missing_from_reader`
counts expose unresolved demand rather than silently treating it as resolved.

The checkpoint can remain intact even if subsequent stdout delivery fails. The
next invocation must respect its minimum poll interval; a failed process does not
imply the previous checkpoint was absent or safe to overwrite.

## Remaining runtime work

This supplies the real observation runtime only. Still required separately:
- Allocation integration with saved policy, revision-bound durable grants and a
  single-owner pool-settings writer.
- Atomic provider admission and safe runner retirement/recovery.
- Operator UI/service supervision and fully supported credential onboarding.
- Targeted reconciliation of terminal historical jobs outside the reader window.

No live infrastructure was used for verification. Tests use disposable TLS HTTP
servers and temporary private files plus injected scheduling clocks/waits.

Historical queued observations missing from a fresh run enumeration are checked
against the exact repository-scoped GitHub job endpoint in a rotating batch of at
most 32 lookups per poll, within a 10-second budget. A matching positive
completed response can retire an old observation only when the stock queue does
not still claim the job. Missing, inaccessible, malformed or conflicting results
remain unknown; no physical capacity is released and this does not prove
complete webhook coverage. Lookup failures preserve the prior observation.
