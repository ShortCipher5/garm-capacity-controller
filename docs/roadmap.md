# Focused delivery roadmap

1. **Admission foundation (current draft):** offline model, durable local reservations, budgets, conservative states, idempotency, contention and restart tests. No external integration.
2. **Read-only web overview:** Go API, simple React dashboard, fixture-only host/pool views, allocated capacity and decision history. Clearly display simulated mode; no pretend live switches.
3. **SQLite store (in a focused draft slice):** transactional schema, WAL/local filesystem constraints, migrations, atomic admission + audit, corruption/restore tests. Match existing model tests and add cross-process fault injection.
4. **Read-only adapters:** pinned Garm v0.2.1 and exact Incus provider version; complete bounded inventory (the pinned Garm collection is unpaginated), TLS/auth, freshness and unknown-resource accounting. No writes until tested in disposable infrastructure.
5. **Provider admission integration (pre-JIT compatibility patch in a focused slice):** reserve-before-create, stable request identity, cross-pool serialization, retry/defer compatibility evidence and idempotent provider outcomes. Prove bypass resistance.
6. **Reconciliation and draining:** import pre-existing resources, provider-operation recovery, confirmed deletion before release, bounded job-assignment races for idle runners; never silently terminate jobs.
7. **Admin and operations:** authentication/authorization, external secret keys, audit retention, telemetry, policy changes, failure drills, backup/restore and an independently reviewed production readiness gate.

Priority in pool policy is not priority scheduling. Fairness, starvation bounds and queued demand need their own tested scheduler. Resource accounting and historical observed usage must remain separate.

Operational backup/restore acceptance is tracked explicitly in [operational-acceptance.md](operational-acceptance.md), including consistent WAL snapshots and mandatory fail-closed post-restore inventory reconciliation. Offline library APIs and a local operator CLI are implemented; there is no qualified live recovery service.

The integrated experimental gateway/provider pair is documented in [gateway-service.md](gateway-service.md). It defaults to authenticated reads, and its incomplete destructive lifecycle prevents calling it a production replacement for the installed provider.
