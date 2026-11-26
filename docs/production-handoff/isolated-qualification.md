# Isolated qualification and rollback for stock GARM

Production trackers [#53](https://github.com/ShortCipher5/garm-capacity-controller/issues/53) and [#268](https://github.com/ShortCipher5/garm-capacity-controller/issues/268) own the work. Use this checklist on a separately authorized isolated installed stack. Release review requires affirmative evidence for each capability on the exact installed artifact and bounded scope. Keep controller writes, automatic release, and attributed collection disabled wherever evidence is missing or contradictory.

## Candidate and scope

- Record main, reviewed head and merge SHA, checkout SHA from each job, and the model CI, disposable packaging, read-only admin UI and pinned GARM compatibility results. Repeat affected review and all four workflows on a new integrated candidate when the base or tested tree changes.
- Record the installed controller, unmodified GARM, provider, runner and backend artifacts with digests and source revisions. Identify the permitted repository pools, provider routes, backend projects and qualified resource profiles. Document the other create, restart and settings paths and how each is independently excluded or held to the same boundary. Keep synthetic fixtures, public source findings and installed-stack evidence labeled in the report.
- Capture the initial held CPU, RAM, runner and applicable storage commitments, with pending and unknown operations separately counted. Use only sanitized references and resource totals in the report. Keep runner and job records private, and keep GitHub assignment and GARM lifecycle in place.

## Shared admission under competing demand

- Stage two or more isolated ordinary repository pools with a common enforced resource budget and one remaining slot. Exercise simultaneous demand plus an already accepted create. Repeat at exact CPU, RAM and applicable disk boundaries where a runner slot alone would otherwise fit. Exercise a repository with a nonzero idle floor and a higher-weight competitor alongside an already charged grant. Verify that pending supply is counted once and unknown observations do not contribute capacity. Capture the durable decision and effective footprint before backend effects, and reconcile provider operations to those decisions.
- Inventory ordinary repository, organization and enterprise pools, accepted pending retries, every named provider, the separate scale-set worker, and independent backend create, start, restore and migration writers. Record each route's shared-boundary trace or the reason its footprint is isolated. Run one grant through its exact demand or host freshness deadline and verify that it remains charged without permitting a new create.
- **Pass evidence.** At most one eligible allocation commits the final slot. Current scope, profile, readiness and actual vector are validated at admission. Other create paths either hit the same authority and budget or are independently blocked. Failed, pending, or ambiguous allocations remain charged after restart. Stale, incomplete or wrong-scope observations refuse new growth.
- **Stop condition.** A native provider, alternate route, queued request or privileged writer can exceed the tested limit, or a changed pool setting is the only apparent enforcement. Leave enforcement disabled. A whole-host claim requires independent accounting or exclusion of all other consumers. [Plan #394](https://github.com/ShortCipher5/garm-capacity-controller/pull/394)

## Operator pause and interrupted settings updates

- In the approved isolated scenario, observe local pause with work attempted before and after its durable acknowledgment. Observe drain through the protected read-only service separately. Exercise response loss after acceptance, delayed completion, process restart and a competing writer with the supported upstream contract.
- **Pass evidence.** Subsequent local work is denied after pause commits. A drain result describes the protected session and carries its own evidence. The exact interrupted operation has an independently verifiable outcome under a continuing exclusive writer and accepted-work fence. Lifting a local pause leaves unresolved work blocked and charged. Apply and rollback require fresh scope and intended-field readback under that fence.
- **Stop condition.** Exact outcome, continuing exclusion, or scope changes are unclear. Keep the writer disabled and preserve the unresolved work. Report matching settings, listener changes and historical restart receipts as diagnostics only. [Plan #393](https://github.com/ShortCipher5/garm-capacity-controller/pull/393)

## Natural retirement and slot reuse

- Capture the exact original completed one-job runner, its registration and immutable backend allocation, and the full charged resource vector. Obtain independent positive job settlement, post-job completion, assignment and reconnect closure, original-incarnation teardown, and disposition of processes and charged storage. Hold the independent late-work fence. Wait for the new durable reconciliation barrier to return before collecting bounded inventory or observing the continuing fence. Collect new evidence for each barrier.
- **Pass evidence.** The current reconciliation proves the relevant scope, and the continuing fence has fresh post-barrier evidence. The guarded transaction credits exactly the original vector once. Retry and restart show the same tombstone and zero additional credit. Wrong generation, a conflicting view of the same incarnation on another host, retained storage, stale fence, disputed outcome and reappearing inventory preserve or restore the applicable charge or hold without implying reusable capacity.
- **Stop condition.** Any witness relies on a name, a successful provider acknowledgment, a missing row, or incomplete visibility. Preserve the charge. [Plan #392](https://github.com/ShortCipher5/garm-capacity-controller/pull/392)

## Queued job recovery after retirement

- Predeclare the isolated fixture, its shared-budget scope, and the observation window (1–300 seconds starting at independently verified release). First exhaust eligible headroom in that scope. While still at the ceiling, record the exact GitHub repository, run, attempt and numeric job as queued, and independently record its blocked admission. Then prove the original allocation retired and restored enough eligible headroom for that waiting job in the same scope. Independently bind the replacement to its numeric GitHub runner registration. Observe whether the original waiting job starts on that registration strictly after verified release and no later than the end of the predeclared window.
- **Pass evidence.** The attempt-scoped GitHub job record shows the original waiting job started on the independently identified replacement registration within the window. This proves start, not completion. A free ledger slot, a ready runner, a successful request or a different job starting is not recovery. Record budget enforcement, capacity restoration and queue recovery as separate results. See [the stock scaling release boundary](../stock-scaling-release-boundary.md).
- **Where to run it.** Public contributor CI can exercise deterministic queue events and a local disposable backend without credentials. Real GitHub registration and assignment belong in a separate trusted workflow on a reviewed revision against dedicated fixture repositories, with no credentials exposed to fork code and no access to production runners. Keep installed-environment checks separate.

## Per-job usage and retention

- Join the exact enrolled source, repository, workflow, run, attempt and job to the assigned numeric runner registration. Prove the original GARM row and provider allocation lead to the immutable runtime incarnation, including process epoch. Bracket the independently observed job window and compare each supported measure with the installed source's documented units and sampling behavior.
- **Pass evidence.** CPU is derived from valid cumulative intervals, memory mean and sampled peak retain distinct semantics, and each supported disk counter has independent coverage. Gauge timestamps are in the observed window. Missing metrics stay unavailable. Foreign scope, wrong generation, overlapping jobs, reset and teardown prevent attribution across those boundaries. Verify bounded runtime/API impact and isolation from admission. Test retention revocation, reopen, duplicate/conflicting observations and physical expiry with no new writes, including restart.
- **Stop condition.** A link has only a name, mutable provider ID, apparent timing match, or an unproven original allocation. Keep live usage and retention disabled and verify GitHub timing and navigation still work. Roll out the [#399](https://github.com/ShortCipher5/garm-capacity-controller/pull/399) dashboard before any measured-v2 producer. [Plan #395](https://github.com/ShortCipher5/garm-capacity-controller/pull/395)

## Server, repository and pool lifecycle

- Discover an isolated server under the approved read-only connection. Check that
  lost transport, a changed peer, and incomplete inventory leave health or placement
  unavailable. Enroll it disabled. Edit the full CPU, RAM, runner and disk defaults,
  inspect the before/after revision and audit record, and verify that held capacity
  does not move. Race a stale review against a committed edit through another handle.
- For a previously reviewed target, close new claims, record a drain request, and
  independently show what happens to already admitted work and idle job eligibility.
  Attempt removal while a runner, an uncertain operation, a binding, or retained
  storage remains. Removing an unused discovery enrollment is a local operation.
  Check that it preserves the consumed review and alias reservation across restart.
- On the stock repository and pool path, preserve the exact repository owner, pool,
  provider, image, runner size and resolved host scope. Start with a disabled
  ordinary repository pool and confirm the returned UUID, settings and empty
  child inventory. Repeat after a lost creation response and require one confirmation
  for the exact original operation. Any unresolved result blocks further changes.
- **Pass evidence.** Current reconciled host health and placement policy agree under
  the admission barrier. A verified stock route uses an eligible server and the
  exact reviewed size, with all resources charged there. A real drain includes a
  continuing job-assignment fence. Removal of a placed server, pool or repository
  requires independent positive closure of every affected runner and operation,
  with no surviving pooled route or charge. A stock GET or a matching name alone
  cannot establish any of these results.
- **Stop condition.** The observed scope is partial, the installed write contract
  lacks exact recovery evidence, or there is no job-assignment fence. Keep affected
  remote writes and deletion blocked. A local draft or a recorded drain request
  supplies no authorization for remote cleanup.

## Rollback and evidence handoff

1. Close new controller admission and any settings writer, and record whether local pause, session drain and settlement each have evidence. Keep unresolved operations and their full charges. Preserve the original provider routes for work that already exists.
2. Disable live metric production and projection before reverting the dashboard. Check that timing remains usable and existing private retention remains governed by its expiry contract. Verify that no retained data supplies scaling or runner-control authority.
3. Preserve the durable ledger, reconciliation history, release tombstones and private recovery state. For SQLite use a consistent backup or a closed checkpointed database. Check schema and read compatibility before selecting a rollback build. Keep write paths disabled if state compatibility is uncertain.
4. Treat any inverse settings change as a separately reviewed operation under the current scope and fence. An interrupted or uncertain rollback stays blocked and charged. Verify final held vectors and absence of new controller commits during the pause.
5. Hand off the signed-off artifact and scope manifest, per-case PASS or BLOCKED with sanitized evidence, custody and rollback observations, and the exact four CI links. Record any unsupported capability as blocked. Obtain separate authorization for activation and for each expanded scope or fault scenario.
