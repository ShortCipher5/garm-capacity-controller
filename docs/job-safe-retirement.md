# Job-safe retirement: evidence and implementation boundary

**Not implemented or qualified for physical retirement.** The current gateway
continues to reject DeleteInstance, RemoveAllInstances, StopInstance and
StartInstance. Nothing here authorizes applying a patch, removing a GitHub
runner, stopping a guest, or deleting an Incus resource. No live infrastructure
was used for this investigation.

## What the pinned source actually does

This analysis is pinned to the official [Garm v0.2.1 source, commit
154638445c3949c1958b01812f69d9a1e4d82684](https://github.com/cloudbase/garm/tree/154638445c3949c1958b01812f69d9a1e4d82684),
not an assumption about a newer version or the installed controller.

- [`Runner.DeleteRunner`](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/runner.go#L1048)
  checks an administrative caller and allowed Garm instance states, removes the
  GitHub runner by AgentID if nonzero, then sets pending_delete. It has no
  independent atomic check-and-fence of new job assignment. AgentID zero skips
  GitHub removal entirely. Its bypassGithubUnauthorized option can also continue
  after a GitHub removal error. None is proof that execution has finished.
- [`basePoolManager.DeleteRunner`](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/pool/pool.go#L1923)
  similarly deregisters before marking pending_delete, with a separate
  authorization-bypass path. Normal-pool pending deletion later calls the
  provider. With pending_force_delete it can remove the database row even when
  the provider returns an error. Therefore missing Garm rows do not prove
  physical absence, and pending_delete does not prove job-safe deletion.
- [`ScaleSetClient.RemoveRunner`](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/util/github/scalesets/runners.go#L167)
  sends a DELETE to the Actions service. A scale-set recovery comment about
  GitHub refusing busy runners is not a documented service contract or a
  qualification of every scheduling race.
- Garm's own [pool documentation](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/doc/pools-and-scaling.md#delete-a-runner)
  says only idle runners can be deleted. This is useful intended behavior, but
  cannot establish that observing idle and later deleting is an atomic safe
  operation across all the paths above.

## GitHub's published contract does not close the race

The official [REST runner documentation](https://docs.github.com/en/rest/actions/self-hosted-runners#delete-a-self-hosted-runner-from-an-organization)
describes runner DELETE as forcing removal, including for a machine that no
longer exists. The published endpoint does not offer a conditional
delete-if-still-idle precondition or a documented drain token. The Get endpoint's
busy field is a point-in-time observation, not such a token. This investigation
does **not** assert that GitHub always allows removal of an active runner; it
states that the available published contract is insufficient to certify the
required atomic job-safety guarantee.

GitHub's [removal guide](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/remove-runners)
says stopping the runner application prevents future assignments while leaving
it offline, and that a JIT runner can execute only one job. Neither statement
proves that an arbitrary stop is safe for an already accepted/running job or
that post-job cleanup has finished. A zero busy flag, job-completed webhook,
offline status, successful deregistration, elapsed timeout or absent list entry
alone must not become permission to stop/delete a guest.

## Implemented bounded slice: non-destructive drain assessment

`internal/retirement.Assess` consumes one consistent SQLite snapshot without any
I/O or writes. It reports:

- whether the ledger presently blocks NEW claims, distinctly from job dispatch;
- host observation freshness and last-observed allocation-state counts;
- reservations absent from inventory, still counted and charged;
- linked reservations and inventory counted once, rejecting ambiguous identity;
- explicit outstanding job-assignment-fence and confirmed-retirement gates,
  even for an empty host.

It never produces a deletion permit, changes a reservation, frees budget,
declares a host drained, or relabels last-observed idle as safe-to-delete. An
already committed physical dispatch can still complete after host mode changes.
An existing idle runner can accept a job without a new provisioning claim.

## Concrete boundary needed before enabling retirement

1. Persist a retirement intent bound to controller, host/project, pool, name,
   immutable provider identity and reservation. Serialize this against dispatch
   and re-registration, and fence all relevant writers. Keep capacity charged.
2. Establish a version-qualified mechanism that prevents future assignments
   **without cancelling accepted/running work**. It must cover idle-to-assigned
   races and both normal pools and scale sets, or explicitly refuse an
   unsupported mode. A validated guest/listener handshake may be necessary;
   no such trusted guest control channel is currently implemented.
3. Obtain authoritative job-and-post-job completion after the assignment fence.
   Busy, unknown identity, failed observations, missing AgentID, revoked
   credentials, or restart must hold, not delete. Do not use the force/bypass
   Garm options as fallback.
4. Journal exact physical delete intent before dispatch, reject replacement
   identities, correlate the Incus asynchronous operation, and recover uncertain
   outcomes without blindly repeating an operation against a reused name.
5. Only after authoritative exact-resource absence, no unresolved creating or
   deleting operation, no viable runner registration and a fresh complete
   reconciliation may a separately audited transaction retire the charge.
   DELETE HTTP success or operation timeout alone is not absence proof.
6. Exercise disposable fixtures and the exact installed versions: job assignment
   racing retirement, running/post-job cleanup, missing AgentID, busy/403/404,
   force/bypass attempts, cancellation, restart at each journal boundary,
   delayed operations, name reuse and malformed/partial inventories. Installed
   stack qualification and live rollout require separate authorization.

Until these gates are met, the correct behavior is an explicit blocked drain
with visible charged allocations, not an apparently successful destructive
lifecycle.

## Read-only project observations and concrete fence requirements

The [physical retirement proof boundary](physical-retirement-proof-boundary.md)
adds a source-pinned runner-shutdown counterexample, concrete missing supervisor/
assignment/backend-frontier requirements, and an authenticated project-observation
API with explicit uncleared external gates. Its fixtures show why even matching
empty lists can coexist with a late queued write or a permission-hidden target.
It does not change the blocked retirement or charged-allocation behavior above.

The [pinned official runner lifecycle source-contract gate](../integration/actions-runner-v2.323.0/README.md)
executes actual Runner/JobDispatcher methods with mocked service/worker inputs.
It verifies counterexamples and cleanup ordering, not a deployed runner fence or
authority to retire resources. Its hosted compiled result is distinct from local
source-hash checks; absent SDKs must not be installed as an unapproved fallback.
