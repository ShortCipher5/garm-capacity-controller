# Stock GARM shared-capacity broker

## Target and current implementation boundary

The production target is an independent controller using **unmodified GARM** and
supported provider/API interfaces. Do not ship a fork, patched build, direct GARM
database mutation, or fake provider success as a shortcut.

`internal/capacitybroker.Build` is now an implemented **advisory allocation planner**.
It does not perform remote writes, persist grants, accept provider calls, release
capacity, or make the current patched gateway work with stock GARM. It has no
production consumer. A plan cannot be passed as a permit to bypass admission.
The existing patched integration remains experimental and is not silently replaced.

The planner:

- Shares available CPU, RAM, disk and runner capacity across repositories using
  weighted dominant-resource shares. Weight zero means the default equal weight.
- Enforces explicit repository and pool runner caps and host eligibility. Multiple
  pools belonging to one repository share that repository's cap and weight.
- Prefers actual unmet queue demand over optional warm-idle targets. Existing idle
  runners remain charged and count toward the warm target.
- Charges unknown, deleting, orphan and outstanding granted capacity exactly like
  active resources; reducing a desired maximum cannot release a charge.
- Supports explicit non-pool host charges for unrelated workloads.
- Rejects incomplete/stale/future-dated snapshots, duplicate charge identities,
  unknown hosts/pools and arithmetic overflow. Zero budgets mean no capacity.
- Produces deterministic bounded output without mutating its input. The output
  carries the input revision/time and reports remaining queue demand.

The input adapter must supply complete, deduplicated ledger-backed charges plus
outstanding grants, not sum linked reservation/inventory rows twice. It must
attribute each queued job only once across matching pools and subtract existing
idle/creating capacity from queued demand. Snapshot completeness is only an adapter
assertion, not proof that other writers are fenced. Historical resource sizes on
charges must not be replaced by a newly edited profile.

This first allocator is greedy and non-preemptive. It does not promise bin-packing
optimality, a starvation bound for large jobs, or exact GitHub job ordering. Those
properties require a durable queue/grant policy, not fabricated guarantees from
this stateless planner. Repeated calls on the same snapshot must never be applied
as additional grants.

## Why stock pool limits are preferable to sleeping in Create

Evidence below is pinned to upstream GARM commit
`154638445c3949c1958b01812f69d9a1e4d82684`; inspect the exact deployed release before
binding a runtime adapter to it:

- [`addRunnerToPool`](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/pool/pool.go#L1326-L1344)
  checks `Enabled` and the instance count against `MaxRunners` before `AddRunner`.
- [`consumeQueuedJobs`](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/pool/pool.go#L2050-L2205)
  revisits recorded queued jobs and unlocks a job when no runner was created.
- [`UpdatePoolByID` and `ListAllJobs`](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/pools.go#L69-L128)
  provide stock settings and job-list entry points.
- [`retryFailedInstancesForOnePool`](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/pool/pool.go#L1400-L1480)
  cleans up failed instances before bounded retries. A provider rejection is not
  equivalent to a harmless pre-create pool-limit refusal.

Therefore the desired normal path is dynamic pool capacity grants, with the atomic
provider gate as a **safety backstop**, not routine saturation rejection. GARM owns
the actual job queue and runner lifecycle; the broker owns allocation policy.

Do not blindly use `enabled=false` for a zero grant: this version also skips idle
scale-down on disabled pools. Pool creation rejects `max_runners=0`; update code
has different validation. A zero target in this planner is not an assertion that
any selected stock release accepts a zero maximum through all API paths.

## Remaining runtime implementation

1. Supported stock job/runner/pool adapters with bounded reads, source identity,
   missed-webhook repair and cross-pool demand deduplication.
2. Durable revision-bound grants and fair queue history. Retain revoked/in-flight
   grants until safe to reuse; lower maxima alone cannot fence delayed creates.
3. A single-owner stock pool-settings writer, with persisted original intent and
   ambiguous-write reconciliation. Stock GET/fingerprint/PUT is not remote CAS.
4. A stock-compatible provider admission path tied to those grants and immutable
   request identity. It must not fall back to the patched preparation contract.
5. Integration with qualified safe retirement, host authority and restart/restore
   recovery before permitting released capacity to be granted again.
6. Operator UI that distinguishes planned, granted, actually allocated, unknown and
   blocked capacity, rather than calling this advisory output usable capacity.

The previously denied dispatch-envelope/binding scope is not changed here. No
live GARM, Incus or GitHub runner infrastructure is contacted by this package.
