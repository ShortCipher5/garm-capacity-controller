# Stock demand to shared allocation planning

`brokerinput.Stock` connects the saved `admission.BrokerPolicy`, the existing
`garmapi.DemandObservation`, and a complete caller-supplied capacity snapshot.
It verifies stock pool repository ownership, rejects observed runner supply that
is not covered by supplied charges, retains all historical charges, and applies
saved repository weights, runner caps and warm-idle targets. Disabled stock pools
receive no new assignments. Missing/inconsistent ownership and stale observations
fail closed. Observation freshness starts at the beginning of the stock read.

`capacitybroker.BuildForJobs` keeps each job's eligible pool set until fitting it
against host resources and weighted repository shares. Existing idle slots are
matched once. Each unmet job consumes at most one planned assignment, even when
multiple pools can serve it. A job is not prematurely assigned to a full pool
when another eligible pool has capacity. Remaining jobs are returned individually;
per-pool candidate counts are deliberately not exposed as additive queue totals.
The assignment JobID is correlation for a proposed plan, not proof that GitHub
has assigned that job to a runner.

The caller must supply stock UUID pool IDs, stable ledger revision, complete
historical charges (including unresolved creation and grants), host eligibility
and reviewed resource profiles. This adapter does not invent those inputs from
stock counts, drop unmanaged charges, or mark an incomplete snapshot complete.
A successful planning call is NOT a physical-capacity grant or writer fence.

## Remaining integration

This is a planning bridge, not the live allocator. It has no provider permission,
GARM writes, durable grants, deletion or capacity-release operations. It does not
resolve uncertain writes or provide a stock-compatible provider execution path.
The read-only demand-observer command is unchanged. The runtime still needs to
atomically bind plans to authoritative ledger grants before publishing pool
limits, and bind delayed provider requests to those grants.

Creating runners are charged but are not idle supply. Without authoritative
job-to-creation correlation the bridge cannot deduct a specific queued job for
an in-flight create. It consumes stock-observed demand only, not additional
GitHub-repaired demand. Neither independent stock reads nor this derived revision
prove an atomic fleet snapshot or complete webhook coverage. The prior denied
route/dispatch publication scope is not changed or republished here.

### Bounded placement improvement and remaining limits

New slots use alternating-path matching over existing planned slots: if a flexible
A/B job was correlated to an A slot and an A-only job remains queued, a new B slot
can cover the flexible job and leave the existing A slot for the constrained job.
Only advisory job correlations change. Pool/host resource placements and weighted
repository share accounting never move; no already-covered job is evicted.
The oldest reachable unmet job supplies the age tie-break. A shared one-million
edge-visit budget fails the entire planning call closed on exhaustion.

This repairs the flexible/constrained slot example, not general heterogeneous
resource packing. Host selection and resource placement remain greedy, and initial
idle matches remain fixed. No maximum-utilization guarantee applies across mixed
runner sizes, overlapping host eligibility, or fixed idle correlations. The test
suite exhausts all 512 eligibility graphs of three jobs and three independent unit
slots, in addition to the resource/fairness tests; that is not live qualification.
