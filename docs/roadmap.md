# Focused delivery roadmap

## Implemented experimental foundation

- Durable SQLite WAL/FULL admission with one atomic global/host/pool budget transaction, conservative charged states, one-shot dispatch intents, durable received submission correlation before operation wait and bounded multi-process contention tests. The JSON simulator remains separate from live enforcement.
- Verified read-only Garm inventory and standalone-only Incus HTTPS/mTLS transport, with barrier-first bounded three-source reconciliation. Partial, unknown or unbounded inventory blocks new claims.
- Maintained opt-in Garm v0.2.1 pre-JIT compatibility patch, compiled gateway-only provider and credential-isolated gateway. Disposable pinned-source/executable fixtures cover admission deferral, conservative create replay and preparation-failure cancellation with permanent tombstones. Provisioning is off by default.
- Separate authenticated read-only TLS admin API and React view of actual ledger allocations, recorded health and conservative drain blockers. There is no synthetic fallback or policy editor.
- Trusted ledger-only host enable/disable/draining primitives, consistent SQLite backup/restore APIs and a local operator recovery CLI. These do not fence external writers, stop jobs, prove physical retirement or qualify live recovery.

## Remaining delivery and qualification

1. **Create/restart recovery:** build evidence-gated recovery on the [durable pre-wait submission journal](submitted-correlation.md), including lost responses before receipt persistence, terminal evidence and immutable resource generation. Submitted/unknown records remain charged and cannot authorize blind create retry, confirmed success or timeout-based release.
2. **Job-safe lifecycle:** establish a version-qualified assignment fence that does not cancel accepted/running work, completion and immutable-resource evidence, journaled physical deletion, and authoritative retirement before releasing charges. Existing idle runners can still accept jobs when admission is paused.
3. **Scheduling and pool policy:** add a transactional multi-host chooser and explicit repository/pool labels, priority and idle controls. Current placement is fixed in trusted gateway configuration. Priority metadata alone is not fair scheduling; queue ownership, starvation bounds and retry semantics need separate tests.
4. **Authenticated management:** separately authorized, attributable and conditional policy changes, accurate UI controls, security-event retention and credential rotation. Never present pause-new-claims as completed drain or allocated budgets as measured utilization.
5. **Operator packaging and isolated CI:** validate build/Compose configuration and real disposable UID, credential, filesystem, network and resource boundaries. Keep provider switching and experimental provisioning off until separately authorized and qualified. Hosted fixture CI is not installed-stack qualification.
6. **Operational acceptance:** qualify exact installed versions, complete physical-host/project coverage, native Incus isolation, alternate-path and old-writer fencing, failure drills, live recovery, measured historical usage and an independently reviewed production-readiness gate.

Operational backup/restore acceptance is tracked in [operational-acceptance.md](operational-acceptance.md). Offline primitives and the local CLI are implemented; a live recovery service with verified external fencing and new authoritative post-restore inventory is not qualified.

The integrated pair is documented in [gateway-service.md](gateway-service.md). Passing local, browser or pinned-source fixture tests does not make its incomplete destructive lifecycle a production replacement for the installed provider. No deployment is claimed or authorized by this roadmap.
