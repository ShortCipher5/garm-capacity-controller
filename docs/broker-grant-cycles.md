# Enrolled grant cycles (not activation)

`brokerinput.GrantCycle` is an in-process capacity-bookkeeping writer:

1. Poll the concrete scoped stock GARM client.
2. Require the transport-minted source identity to match every persisted pool profile.
3. Read policy, configuration, historical charges, current host bindings, execution identities and enrolled profiles in one SQLite snapshot.
4. Require the current stock ownership/resource fingerprint and host-binding sequence to match enrollment. Retire at most 64 already-expired unused grants through the canonical ledger transition, then reread all state before planning.
5. Plan with existing idle, unused grants and unresolved creating/unknown supply counted once.
6. Issue a durable grant batch using the same ledger sequence and policy revision CAS.

The existing grant batch is the cycle intent; there is no second reservation ledger or capacity journal. Its deterministic identity includes the source and exact ledger/policy revision. Complete canonical payload equality is required for exact replay. A competing cycle against the same revision either replays the exact batch or conflicts. After an uncertain response or process restart, the next cycle reads fresh ledger charges and does not blindly submit an old plan. CAS conflicts are reported, not automatically applied to a newer revision.

`RunGrantCycles` serializes cycles, applies a 90-second per-cycle deadline and waits a configured 1–60 minutes between attempts. Failure reports no stale plan/counts. Grant TTL is explicitly 1–60 minutes. Wall-clock expiry alone does not free charged capacity. The bounded maintenance pass invokes canonical UNUSED→EXPIRED transactions; concurrently consumed grants cause an error, not a release. Partial completion survives restart, and subsequent cycles reread the ledger. Backlogs larger than 64 remain charged until later passes. Consumed, creating and unknown reservations are never retired by this loop.

## Boundaries

These functions are not wired into production startup. They do not modify stock pool limits, activate native execution, install an agent, release physical reservations or prove exclusive host authority. A grant remains capacity bookkeeping, not a physical execution permit. The source identity identifies the configured API destination; stock GARM does not independently authenticate a controller ID. Metadata enrollment and the native execution boundary still require their separate checks.

Tests cover actual TLS observation provenance, profile/host drift rejection, charged-grant reuse after a simulated fresh read, stable cycle identities, and the real SQLite issuance boundary under concurrent retries and reopened-ledger replay. A public GrantCycle integration test obtains actual pinned-mTLS native identity and scoped TLS stock ownership, registers the immutable mapping in SQLite, issues a grant through the real demand client, reopens the ledger and verifies the next cycle does not issue duplicate capacity. These tests do not execute a native create or qualify a deployed service.
