# Durable retirement coordinator: authority held

This change integrates a durable retirement request into the provider deletion
path, but does not enable physical deletion. Stock GARM remains unmodified.
Exact never-dispatched cancellation keeps its existing behavior. Other requests
must have original authenticated creation evidence and an exact charged ledger
reservation before a held intent is recorded. No lookup of a current name can
replace missing original evidence. The provider still receives unavailable.

## Persistence and replay

Schema 15 follows the combined schema 14 connection lifecycle and recovery
assessment branches. The original intent contains the immutable creation subject
(host key/scope, instance UUID/generation, original operation and reservation).
It is written with audit in one BEGIN IMMEDIATE transaction. Concurrent retries
return that original record; request/subject changes conflict. No allocation or
reservation lifecycle state changes merely because retirement was requested.

A second durable table defines the one-attempt recovery protocol. A future
qualified authority implementation must commit a unique `unknown` attempt
BEFORE a destructive request. A lost response remains unknown, never retried by
name. The returned original operation ID can be recorded idempotently with its
private permit; later operations cannot replace it. Recovery selects observation
of that original operation, not dispatch. An authenticated matching terminal
operation observation can be journaled, but remains fully charged. The records
survive ordinary restart and consistent backup. Existing restore artifacts stay
runtime-disabled.

`ClaimRetirement` ALWAYS denies. There is no production permit constructor,
operator flag, HTTP override, SQL injection route or exported authority factory.
The future-attempt state tests seed internal SQLite fixture data explicitly;
they are not evidence that production host authority or dispatch exists.
The module performs no backend calls. Observation recording accepts the existing
opaque authenticated transport type, not arbitrary JSON, and never treats a
name-only Incus operation as incarnation destruction proof.

## What remains before physical retirement

1. Host-enforced exclusive writer/credential ownership, including stale-controller
   rejection and protection against rename/recreate/restore of an incarnation.
2. Trusted job completion and execution termination evidence. Stock GARM's delete
   callback, `idle` status and GitHub deregistration alone are not an atomic drain.
3. An authority-issued one-shot attempt executor that durably records intent
   before invoking an incarnation-bound host operation and persists its result.
4. A finalizer combining that authority, terminal operation and qualified physical
   absence before releasing capacity. There is no finalizer or release path here.
5. A supported restoration/reconciliation protocol for post-backup unknown work.

Incus can purge terminal operations quickly; an expired original operation is an
unknown outcome, not permission to infer absence or delete another object. A
backup cannot prove that later work did not occur. Missing, ambiguous and failed
observations retain their charges. Force deletion in GARM must never erase this
independent accounting history.

The operation journal does not reconstruct an opaque authenticated instance
observation from serialized history. After a restart, an already-absent guest
may prevent the current transport from obtaining a fresh prior identity; the
coordinator must then retain the charge. Supplying a stored subject as though it
were new live evidence is deliberately not implemented. A future host-owned
operation-recovery transport must resolve that case without weakening identity
or inventing exclusive ownership.
