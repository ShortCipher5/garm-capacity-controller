# Opt-in read-only advisory sizing

`allocator_advisory` is separate from `allocator_loop`. Configure repository IDs
and `interval_seconds` (15–60) with the existing pinned managed GARM read transport
and admin read endpoint. It never calls the grant lifecycle, issues grants,
changes pool settings, retires resources, or grants execution authority.

The polling cache uses real stock repo/label demand, immutable enrolled resource
profiles and coherent canonical ledger charges through the existing planner.
Each poll has a 12-second deadline; errors clear the old cache and back off up to
60 seconds. Expired results are unavailable. Shutdown cancels and joins the poll
before closing the source. GET `/admin/api/v1/allocator-plan` uses the existing
admin read token. The separate grant-loop status remains unconfigured/blocked;
configuration of this reader never implies the grant loop is running.
`--check-config` validates the same local transport/configuration without polling.

Monitoring is a veto only: any recent `guest-state-v2` CPU/RAM observation
at or above 90% of a configured budget suppresses new proposed assignments
on that host. Native guest disk totals are display-only because shared volumes
can be counted more than once. An observed `guest-state-v2` value that does
not veto does not refresh or satisfy the prior pressure prerequisite for stock
pool increases. Legacy `guest-state` rows retain the previous CPU/RAM/disk
comparison within the same two-minute window. The advisory planner can instead choose another
already-eligible host using the same canonical budgets and charges. This short high-water window avoids immediately undoing
a veto after one lower sample. These are partial guest measurements, not physical
host totals. Missing, stale, foreign-source or low measurements do not establish
headroom. The baseline planner still requires its independent fresh host and
ledger evidence. Monitoring never frees existing occupancy or changes idle,
busy, creating, unknown, outstanding-grant or in-flight accounting.

At most 64 hosts and 1,000 recent samples per host are examined. A full sample
window suppresses new proposals rather than silently treating truncated history
as low pressure. More than 64 participating hosts returns unavailable. Resource
threshold arithmetic is overflow-safe. Queued items that still cannot fit remain in the displayed unallocated count;
warm suppression does not invent queued jobs. The per-pool suppression count
is the net decrease in additional proposals versus the same snapshot without
the veto; same-pool placements on an eligible alternate host are not suppressed.
This is advisory placement selection, not physical runner relocation. No
capacity increase is inferred. The existing allocation comparison
shows monitoring diagnostics and suppression separately from observed supply.

This is advisory sizing, not automatic cross-repository scaling or a claim of
production host enforcement. Existing positive-write/runtime guards are intact.

`allocator_advisory.allocator_proposal` additionally attaches the observe-only
cross-repository allocation proposal to each cached read served at
`/admin/api/v1/allocator-plan`. It reuses the same bounded tick, plan and
cache: awards carry pool, repository, exact host destination, the complete
budgeted runner profile, per-pool decisions with fixed reason codes, retained
held capacity and outstanding durable commitments, plus input version
(source, ledger sequence, policy revision, demand digest and their combined
revision) and risks. Per-repository weights are read by repository UUID from
the revisioned SQLite `broker_policy` document; a repository without an entry
defaults to weight one. Destinations are marked established only when an
independently verified stock provider route produced the plan; the eligible
host list alone never establishes placement, and an unestablished award is
flagged as a risk rather than as physical enforcement. The section never
enables the `allocator_loop` writer path, issues grants, changes pool
settings, retires resources or releases charges.
