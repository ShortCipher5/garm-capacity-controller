# Collection of usage for a completed CI job

The private collector in `internal/cihealthsource/completed_usage_collector_linux.go`
can reduce a recent completed job's already captured resource window and attach
its measured projection to the enrolled timing history. Stock collection is
disabled. There is no registered provider source, command, startup option or
background job. The public sidecar opener still requires a qualified source
matrix.

## Read budget and identity

One owner can process one exact job at a time. A call has a 20-second deadline,
one exact GitHub run-attempt GET, one indicated jobs-page GET, one existing
managed GARM running-registration capture, one exclusive-window lookup and at
most 120 indexed counter observations. There is no retry or page discovery.
The page number and row UUID are locators. The completed job receipt binds the
numeric source, repository, workflow, run, attempt, job, runner registration
and both execution endpoints. The GARM report checks the owner's independently
approved repository, controller, pool, provider, ownership fingerprint, row and
numeric registration. Both reports must still release the same evidence while
the collector runs. Before every counter read, the collector verifies the
original row, execution and closing observation times again. Contention on
either local store lock ends collection immediately.

The owner opens the window only against the exact retained history instance,
committed workflow cursor and enrolled terminal job. The largest eligible
execution window is 24 hours. A close can follow the actual job end by at most
two minutes and must itself be less than two minutes old. This gives an
absolute lookback of less than four minutes after completion. Expiry uses
source observations. Collection and render times cannot refresh them.

## Missing source contract

The private counter-reader contract requires positive proof of the original
provider allocation for the exact row, immutable daemon/project/instance
incarnation, boot ID and process start ticks, and exclusive assignment for
the entire completed interval. It must supply independently observed opening
and closing brackets and bind every point to the same job and process epoch.
For a separately authorized acceptance run, prime the independent bracket
and counter capture before dispatching the job. After completion, require the
opening observation to identify the selected incarnation at or before the
actual start, within the allowed cadence. A missing opening observation
leaves that job unavailable. Overlapping or ambiguous assignment must
produce an omission. A reader may
only supply metrics qualified for its evidence class. Read bytes, write bytes
and occupied bytes each require their own unit qualification. The stock Incus 7.0.2
named runtime-health reader observes CPU time and memory on its chosen
subject. Its gauges, process ID and timestamps cannot establish the missing
links. The hosted disposable counter fixture does not change this matrix.

[Source capability contract](../internal/cihealth/attribution_capability.go)
and [Incus runtime-health observations](incus-runner-runtime-health.md).

## Retained result

The reducer retains CPU interval averages and peaks, the sample maximum and
covered mean for current RAM, separate byte totals and coverage for disk
reads and writes, and the sampled occupied-space peak where qualified.
A measured zero survives. Missing fields and cadence gaps contribute no
intervals. A counter reset, a changed epoch inside valid brackets or a
sample after completion prevents attachment.

The private sidecar receives only the existing minimized record after checking
the exact terminal endpoints and both store revisions under their locks.
A revocation followed by re-enrollment, timing-history replacement or a
concurrent sidecar publication invalidates the captured window. Cancellation
and receipt expiry are checked before atomic publication. Uncertain files
remain subject to the sidecar's existing fail-closed custody checks. Running
jobs continue to use the timing view. Their jobs-page receipt provides no
positive execution cutoff for sampling.

No live origin/epoch or disk source was qualified for this change. Local tests
exercise sealed GitHub receipts, synthetic exclusive windows and the real
sidecar publication boundary. A deployment would need independent installed
source evidence and a separately approved registration before enabling reads.
