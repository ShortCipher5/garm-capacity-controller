# Named Incus runner health observation

The library can read health for an independently reviewed **existing container
incarnation**, on a previously pinned standalone Incus 7.0.2 daemon and exact
configured project. Pinning, enrollment, scheduling, credentials and UI are not
part of this method. An unreviewed name, mutable runner tags or a cluster
endpoint do not qualify it.

The method performs two named instance metadata GETs and two named runtime
state GETs within fresh pinned root checks. It verifies the expected UUID,
generation, creation time, and direct and effective stock controller/pool
tags. It rejects detected process changes and decreasing CPU counters or
inconsistent reported memory peaks. It
does not use recursive full-instance reads, snapshot listings or backups.
It retains only positive reported cumulative CPU nanoseconds, current memory
bytes, optional cgroup peak memory bytes, and the start time of the final state
request, a conservative bound on
sample age. No PID, raw metadata/network data or other counters are retained.
Counters reported as zero, missing or unavailable are not displayed as zero.

These are independent, non-atomic observations. Incus's
[instance-state GET](https://github.com/lxc/incus/blob/v7.0.2/cmd/incusd/instance_state.go#L19-L26)
can be expensive and can internally gather unrelated counters for this same
container; we do not retain them. It does not include an instance UUID or generation, and
[Incus renders process and cgroup state sequentially](https://github.com/lxc/incus/blob/v7.0.2/internal/server/instance/drivers/driver_lxc.go#L3954-L3986).
Matching bracketing metadata and reported process observations detects visible
drift, not all intervening changes. CPU is cumulative container cgroup usage,
memory is current container cgroup usage, and both can include non-CI work.
The optional memory peak is Incus's
[`memory.usage_peak` high-water mark](https://github.com/lxc/incus/blob/v7.0.2/internal/server/instance/drivers/driver_lxc.go#L8028-L8037)
over the reported cgroup lifetime. It is not current memory, the peak in
the sampling interval, or a job-window peak. Unavailable peaks are omitted,
not filled from earlier observations or current memory. A cgroup reset or
replacement can defeat continuity; detected inconsistency rejects the sample.
This is not a counter-epoch identity, live placement proof, host budget, GitHub
job attribution, resource sizing input or permission to control anything.
Disk capacity and I/O are not provided.

`SummarizeRunnerRuntimeHealth` compares at most 120 actual in-process named
observations from one explicit window of at most 24 hours. It refuses mixed
daemon/project, container identity or reported process, duplicate/unordered
reads and detectable counter regression. It returns the latest observation
and the highest *sampled current* memory, never backfills a missing latest
value, and does not derive CPU deltas or rates. The opaque sample match does
not prove that the effective profile, cgroup namespace, or counter epoch
remained unchanged. In-place profile edits can affect what contributes to
the sampled maximum, and a reset can go undetected. A sampled memory maximum
is neither the cgroup-lifetime peak nor proof of a high point between reads.
Do not treat the samples as comparable intervals or use them for resource
sizing. Pool-wide disk gauges and root-volume metadata cannot substitute for
per-instance disk counters. No fetch, history, authorization or cross-job
attribution is added by this reducer.
