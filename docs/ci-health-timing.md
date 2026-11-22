# Read-only CI Health timing

This dashboard view is optional and disabled by default. It reports bounded
GitHub Actions run, attempt, job and step timing observations. It does not
change workflows, schedule jobs, control GARM, or grant capacity.
See the [fixture-backed evidence matrix](ci-health-evidence.md) for what
current sources can prove and what remains unavailable.

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

An old rerun disappears from the active listing when it finishes, and it
may be outside the recent window, which uses the original run's creation
time. After a successful clean publication, the next cycle can recheck one
exact previously observed active attempt per repository if ordinary reads
leave enough of that repository's existing budget. This never displaces
ordinary reads or increases the hard limits. It does not guess rerun
numbers, retry a failed collection, look back after restart, or discover
attempts that were never observed active. The one-use locator lasts less
than six minutes in memory, not as a display or data lease. Only new exact
attempt and job reads can produce a result or update opted-in history.

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

For running or completed jobs, **Job creation to start** is shown separately
when GitHub reports valid creation and start timestamps for the same exact
job. It is elapsed time between those reported timestamps, not queue entry
or evidence of runner saturation, provisioning delay, or any particular cause.
Missing or invalid creation evidence leaves this field unknown without changing
execution or queue measurements. The value is only in the current owner
snapshot and is not persisted, backfilled, or used in retained trends.

The longest-steps summary ranks up to twelve individual completed step
observations from this snapshot, with exact workflow, run, attempt, job,
and step identifiers. It counts measured versus observed step rows.
Running, skipped, and untimed steps are not ranked. The order is by measured
execution duration, not by typical runtime or recent change for a step.
Use **View step** to open that exact attempt and job's step details and
focus the numbered row in the same snapshot, without another request.

For exact observed jobs on a supported GitHub.com enrollment, **Open exact
job on GitHub** rechecks the original observation and local reader at click
time through the same owner-authenticated CI Health route. The snapshot
contains only temporary in-memory handles, not job destinations. No GitHub
request is made by this lookup; opening a destination still requires the
browser user's own GitHub access. Reload the timing snapshot if a handle
has expired, been replaced, or lost its original reader. A destination
already handed to the browser cannot be recalled.

CI Health presents source-confirmed results separately from timing for each
observed run attempt, job, and step. A completed row without a result is
unknown; a duration alone does not imply success. The view does not expose
raw source result strings or individual test case outcomes.

Ordinary gateway configuration still provides current timing and result
visibility only. Retained summaries also require both the separate
`--allow-ci-health-retained-history` flag and an independently reviewed
`ci_health.history` block in the private gateway configuration. Keep
`--allow-ci-health-timing` and the existing loopback admin configuration.
No new token, route, collector, or GitHub scope is needed. Serve the matching
dashboard assets when opting in.

The review block requires `directory` (the absolute canonical path),
`directory_device` and `directory_inode` (the exact positive Linux stat
identities), `snapshot_sha256` (lowercase SHA-256 of the exact held snapshot
bytes), `source_id` (the exact configured CI reader ID), and `workflows`
(a nonempty list of exact `repository_id`, `workflow_id`, `start_revision`
and `start_checkpoint` values). Repository IDs must already be enrolled for
timing; zero revision and checkpoint must be explicitly reviewed for an
approved workflow with no stored checkpoint. Do not copy review values from
the gateway's current guesses or infer approval from a successful static
configuration check. That check verifies syntax only.

The operator must already control a dedicated directory owned by the service
UID with mode 0700, not shared with any other component or exposed as assets.
Every ancestor must be root- or service-UID-owned, not writable by group or
others, and not a symlink. Hold that UID and storage exclusively; qualify
local locking, rename and file/directory fsync behavior separately. The
gateway checks the held identity and reopens only an existing clean directory
with its one fixed canonical snapshot. It will not create, reinitialize,
repair, transplant, guess a path, or adopt a snapshot from another workflow.
An open writer, unexpected file or unresolved stage/intent is refused.

After a committed change, the previous digest and cursor review will no
longer authorize the opted-in startup. The gateway rejects that invocation
before opening the admission database or starting services. Quiesce the
writer, independently review unchanged custody and the new exact bytes and
cursor for every approved workflow, then reissue the private review. This
does not support unattended history restarts. The timings-only path remains
available without this flag and block; do not erase history to get past a
refusal.

### First installation only

For a genuinely new installation, build the separate Linux operator utility
from this source with `go build ./cmd/capacity-ci-history-init`. Run it as the
existing service UID, against an already provisioned, independently reviewed,
empty directory:

```text
capacity-ci-history-init --initialize-empty-store DIRECTORY DIRECTORY_DEVICE DIRECTORY_INODE
```

The positive device and inode values are decimal Linux stat values for that
exact directory. The tool applies the same trusted-ancestor and private
directory checks as startup. It refuses a nonempty directory or competing
writer while holding the directory lock throughout publication. It does not
create directories, read configuration or credentials, use the network, run
the gateway, or initialize twice. A failed or unacknowledged initialization
requires inspecting the existing slot; the tool does not repair or clean up an
unresolved attempt.

Review the resulting private snapshot separately after the initializer exits.
Initialization records no observed jobs or approved repositories. The operator
must still independently confirm the service custody, exact snapshot SHA-256
and exact source/workflow scope with explicit zero starting cursors before
supplying the gateway review block. Do not copy output into startup authority;
the utility produces no review metadata, and neither action grants new
privileges. The gateway remains reopen-only. A separate, optional matching image is
outlined in the [packaging guide](../deploy/README.md#offline-build-and-artifact-handling).

With that explicit opt-in, the existing owner read can show two rolling
seven-day buckets, based on last observed time. It considers the newest
whole retained attempts per workflow, at most 64 attempts and at most 12,000
attempt, job and step records combined. If the next eligible retained attempt
would exceed either limit, the read stops before it and marks the selection
limited. It never splits an attempt or skips past it to fit smaller rows.
Hitting a selection limit does not change the persisted facts, revisions, or
future writes. The marker is about the bounded local read, not proof that a
particular seven-day bucket lost observations. The view separately counts
initial attempts and retry attempts and their distinct run IDs. It also
counts initial attempt failures with an observed successful higher-numbered
attempt for the same exact run, at most once per run. The count is shown in
the initial failure's last-observed window; the success can be observed in
either selected window. An unobserved retry or out-of-window observation
does not establish the eventual run outcome. The reader does not backfill
missed attempts for this count. Failure share uses recorded successes and
failures only. Mean and p95 use actual completed durations with both
endpoints; job and step observations are pooled, not compared by guessed
definition. Workflow wall duration remains unavailable without an exact
attempt end. The retained subset is not complete upstream history. No verified
test reports, job resource measurements or sizing recommendations are
provided by this path.

Each included workflow needs a fresh positive exact attempt observation.
The aggregate expires by the earliest selected observation lease and
withdraws server-side when its local owner or source closes. A browser
already displaying the result hides it on expiry or when the owner session
ends; it does not poll for remote revocation.

GitHub can remove runs before this reader sees them. An attempt that is
missing from a listing or returns HTTP 404 does not establish why the source
is unavailable. Do not count it as passed, failed, skipped, or covered by
historical backfill.

Before enabling any future durable history view, qualify the case where an
already observed exact attempt becomes unavailable: previously observed
facts may remain in the store within approved retention, but unobserved
coverage stays unknown. Verify separately that withdrawing repository scope
stops history visibility. A stored observation is not current authorization.

The optional history snapshot reader accepts older rows with no result field.
An older binary does not read new snapshots containing results. If enabling
retained history for a deployment, preserve its data and requalify any
rollback against the older reader; do not erase or reinitialize history to get
around an incompatible snapshot.

### Offline test-report format qualification

Use an operator-trusted isolated checkout with an already independently
reviewed, lockfile-integrity-verified Playwright installation and a trusted Node
executable. Supply the canonical absolute Node path explicitly:

```sh
CI_HEALTH_FIXTURE_NODE=/absolute/canonical/path/to/node \
  go test -tags cihealth_playwright_fixture ./internal/cihealthreport -run TestPinnedPlaywrightJUnitReporter -count=1
```

The test compares installed version labels to lockfile labels for consistency;
those labels do not authenticate package bytes or the selected Node executable.
The fixture runs one pass, one deliberate failure and one skip without a
browser or server. The bounded parser receives the exact reporter XML in a
local fixture ZIP; the test verifies normalized outcomes and discarded
diagnostics. The fixture requests no artifact downloads, but is not a network
sandbox for the local executables and dependencies. This qualifies report
format only, not the identity of a live workflow, job, archive, or test case.

In the hosted read-only admin workflow, the same fixture checks the pinned
reporter without starting a browser or publishing XML from PR code. On a push
to `main`, it uploads only this synthetic report for one day. Its job summary
records the raw XML digest separately from the uploaded archive digest and
artifact ID. This verifies the hosted synthetic export and upload path, not
downloaded bytes, production report ingestion or exact producer identity.
