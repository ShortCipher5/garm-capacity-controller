# Read-only CI Health timing

This dashboard view is optional and disabled by default. It reports bounded
GitHub Actions run, attempt, job and step timing observations. It does not
change workflows, schedule jobs, control GARM, or grant capacity.

The existing owner-wide admin read credential can view enrolled CI repositories
when this feature is enabled. It is not a per-user repository-sharing mechanism.

## Deployment requirements

- An existing independently authorized read-only demand observer configuration,
  pointed at the intended GitHub server and repository names. The CI timing
  reader uses its existing GitHub TLS trust and credentials, restricted further
  to the explicit repository mapping below. It never reads the GARM credential
  for this feature. No extra GitHub scopes or credential are created.
- An existing private loopback admin listener and its existing read role.
- The numeric GitHub repository identity for each explicitly selected existing
  GARM repository locator. Record it from an independently authorized source;
  a matching repository name is not sufficient.

A private gateway configuration can specify:

```json
{
  "ci_health": {
    "repositories": [
      { "garm_id": "already-enrolled-example", "github_id": 12345 }
    ]
  }
}
```

The section additionally requires explicit `--allow-ci-health-timing`. It
accepts at most four existing enrollments. Neither omission nor a malformed
mapping enables the feature. No new public listener is created. Use the
existing owner session to open the section once the gateway advertises it.

## Interpretation and limits

The worker reads a bounded recent window and separately searches currently
queued/running statuses, including reruns of older workflows. It runs about
four minutes after the prior successful cycle completes, with a longer backoff
after failure. With one gateway, the per-cycle cap of 160 logical GitHub reads
bounds timing traffic even if the data set is busy. Other consumers of the
same credential must still have enough independent rate budget. Confirm that
budget before enabling on a shared credential.

The dedicated timing reader also honors GitHub's applicable quota deadlines.
It does not resolve credentials or send another timing request while its
local backoff is active. The worker's existing schedule is unchanged: it
may check whether the local gate has opened, but it does not immediately
retry a throttled request. The browser never triggers an upstream refresh.

There is no shared or restart-persistent quota coordination. An explicit
deadline that cannot be interpreted safely closes the affected timing
reader for its lifetime; do not restart it merely to clear this safeguard.
The owner view may remain unavailable. Investigate the reader and its
configured release before re-enabling it. This safeguard does not limit
other consumers of the same credential and does not guarantee that a
proposed observation cadence fits their traffic. See
[GitHub's rate-limit guidance](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api#exceeding-the-rate-limit).

A snapshot is only shown while its two-minute display lease is valid. It may
be unavailable between collections, after withdrawals, or after a read failure.
A page ending does not prove complete or atomic history. When local bounds are
reached the display explicitly labels the selected observations partial. The
module never treats unavailable queue fields or skipped-step durations as zero.
A positive queue sighting is a lower bound, not an exact queue arrival.
Run creation is not the start time of a rerun. In-flight values are measured at
the source observation, not at dashboard render time.

The active job summary groups jobs in the validated snapshot by their own
recorded status. It shows up to twelve queued and twelve running jobs in
identity order, not by wait or runtime. Additional observed jobs remain in
the attempt details. The current GitHub reader cannot establish a queue
arrival or prior queue sighting, so queued wait is unknown.

CI Health presents source-confirmed results separately from timing for each
observed run attempt, job, and step. A completed row without a result is
unknown; a duration alone does not imply success. The view does not expose
raw source result strings or individual test case outcomes.

This release provides current timing and result visibility only. Durable
history, comparative trends, per-test results, and verified per-job CPU, RAM,
and disk attribution are not enabled. Looking at this view never changes runner
capacity or approves a proposed size.

The optional history snapshot reader accepts older rows with no result field.
An older binary does not read new snapshots containing results. If history
publication is enabled in a future release, preserve its data and requalify any
rollback against the older reader before deploying it; do not erase or
reinitialize history to get around an incompatible snapshot.
