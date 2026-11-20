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

A snapshot is only shown while its two-minute display lease is valid. It may
be unavailable between collections, after withdrawals, or after a read failure.
A page ending does not prove complete or atomic history. When local bounds are
reached the display explicitly labels the selected observations partial. The
module never treats unavailable queue fields or skipped-step durations as zero.
A positive queue sighting is a lower bound, not an exact queue arrival.
Run creation is not the start time of a rerun. In-flight values are measured at
the source observation, not at dashboard render time.

This release slice provides timing visibility only. Durable trend history,
per-test results, and verified per-job CPU, RAM, and disk attribution are separate
work. No optimization or auto-sizing is enabled by viewing timing.
