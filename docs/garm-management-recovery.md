# Operator recovery for an ambiguous Garm settings write

**Design and disposable tests, not an enabled recovery operation.** The current
journal intentionally has no clear, retry, rollback or unblock method. Stock Garm
v0.2.1 cannot supply the authoritative fence required to add one safely. No live
service, credential, firewall, database or running job was changed to test this.

## What is feasible

Recovery is possible with installation-specific authority over **all old writers,
request ingress and the authoritative database**. It is not safely automatable
using only the existing Garm API. A future adapter must prove and hold the fence
from before the observation until after the resolution audit commits. An operator
checkbox, an evidence filename, elapsed time, a successful GET or "I restarted it"
is not such an adapter.

The maintenance actions below can affect availability, callbacks and worker
behavior. They need a separately reviewed operational window and authorization.
This document is not permission or a script to stop an installed service.

## Why common shortcuts fail

| Proposed evidence | Why it cannot unblock the journal |
| --- | --- |
| Caller timed out, canceled or closed its socket | An accepted handler can still commit. Pinned Garm's pool store uses a database transaction without binding it to the request context. |
| Old settings are still visible | The old PUT can commit after that GET. "Not applied" is not established. |
| Desired settings are visible, even repeatedly | A delayed request or another writer can still change them later. This proves an observation, not request completion or causal attribution. |
| Token revoked, expired or user generation changed | Garm validates authorization before entering the handler; already admitted work is not canceled by that change. |
| Admission paused, pool disabled or controller stopped | These do not fence native Garm UI/CLI, another controller, a queued request, existing runners or a direct database writer. |
| HTTP Shutdown requested or its log line appeared | Garm allows a 60-second shutdown wait and logs errors. A request/log is not proof that every old handler and transaction completed. |
| One old process exited | Other replicas, supervisors, retrying proxies and direct database sessions may remain. A remote database may still be executing an accepted transaction. |
| New healthy process, new token or different journal path | Health is not a writer epoch; credentials are not a completion fence; a fresh journal erases the stop sign rather than resolving it. |

Pinned source: `auth/jwt.go` performs the generation check before the handler;
`database/sql/pools.go:UpdateEntityPool` uses `s.conn.Transaction` without
`WithContext`; `cmd/garm/main.go` logs HTTP shutdown failures. The additional
pinned Runner/SQLite test proves a canceled request context can still change pool
priority. These are actual v0.2.1 semantics, not a claim that cancellation always
fails or that every database backend behaves identically.

## Required recovery protocol

Every stage must remain bound to the same endpoint identity, journal identity,
original request UUID/hash and target. Missing authority at any stage means stop
and leave the operation blocked.

1. **Preserve the incident.** Capture the original immutable intent, typed
   before/desired settings, complete audit history and pending state using a
   consistent journal snapshot. Never copy only an open WAL main file, delete a
   row or restore a historical journal to escape the block. Keep the old request
   permanently non-replayable.
2. **Fence controller generations and ingress.** Prove that every old controller
   copy, automatic retry source, proxy queue and alternative native settings
   endpoint is stopped or prevented from dispatching. Include direct Garm access
   and database access, not just the new management UI. Prevent supervisors from
   silently restarting old writers. Credential revocation may help prevent new
   requests, but cannot replace completion evidence for admitted requests.
3. **Prove remote completion and database quiescence.** A qualified supervisor
   must identify exact old process/container epochs, not merely reusable PIDs,
   and prove all relevant processes and accepted work have completed or been
   terminated. Separately prove the database cannot later commit an old accepted
   transaction. For a strictly local SQLite deployment, exclusive ownership of
   the storage plus a successfully acquired writer transaction after all old
   writer processes exit can form part of this proof. That lock must remain held
   while reading/resolving, and all bypass writers must remain fenced. For a
   networked database, session/transaction draining and a backend-specific writer
   barrier are additional requirements. Merely killing the Garm client process
   is insufficient. No generic SQLite/MySQL or supervisor adapter is supplied.
4. **Collect authoritative post-fence settings while the fence is held.** Read
   from the actual writable authority, not a cache, replica, backup or stale
   pre-fence API response. Obtain the sanitized target settings plus the full
   protected-settings fingerprint/generation used by the client. Verify endpoint,
   target, schema/version and database lineage. A Garm restart can itself run
   initialization/migration code, so it must not be inserted into this sequence
   as though it were a passive read. Do not accept a read before the barrier.
5. **Approve the observed disposition explicitly.** If current settings match
   desired, describe that as "current state matches desired under the verified
   fence," not "we proved the old request executed." Otherwise require an
   explicit decision to adopt the observed state or prepare a separate new
   reviewed change after recovery. Never overwrite the observation with the old
   desired payload or automatically replay the original request UUID. Neither
   disposition changes running jobs, grants a retirement fence or changes ledger
   allocations.
6. **Commit resolution and audit atomically before releasing the fence.** A
   future implementation must compare the exact still-unresolved request and
   journal revision, then durably record authority/epoch/challenge identities,
   evidence digests, endpoint/target/request binding, post-fence observation,
   chosen disposition, management role and operator reason in one transaction.
   Preserve the original uncertainty and permanent no-replay identity; do not
   relabel history as an ordinary successful PUT. A crash before this commit
   stays blocked. A crash after it must recover the same recorded disposition,
   never resend the old PUT. Only after a verified commit may the qualified
   authority transfer control to one current writer and release its barriers.

## Contract for a future authority adapter

An acceptable integration must own the relevant supervisor, ingress and database
controls and return **verified, fresh, narrowly scoped evidence**, rather than
trust caller-supplied booleans. It must:

- Bind a fresh challenge to this exact unresolved intent and endpoint, prevent
  replay across incidents or database restores, and prove ordering without relying
  only on wall-clock timestamps.
- Keep the writer/transaction fence held for the entire authoritative read and
  atomic journal-resolution operation. A fence receipt whose authority has
  already been released is not sufficient.
- Reject expired, mismatched, partial, unverifiable and wrong-authority evidence;
  fail closed if any authority cannot be reached or any lock is lost.
- Prove absence of queued/old writers, including the installed topology's database
  behavior and all alternate API routes. A network probe or process list alone
  does not meet this contract.
- Require a separately authorized operational handoff and explicit disposition.
  Evidence is not permission to perform maintenance or alter settings.

Until that adapter is implemented and qualified for the installed stack, adding
`Resolve(id, true)`, `--force-clear`, a token-rotation shortcut or a generic
"accept current GET" button would make the current safety guarantee false.

## Disposable regression coverage

`TestDelayedPUTNeedsCompletionFenceNotTimeoutOrMatchingRead` accepts an
authenticated TLS PUT, lets the caller cancel, and keeps the handler able to commit.
A GET first observes the old state; the durable journal remains blocked. A
**test-only** ingress gate then prevents new writes but cannot report a completed
fence until the old handler finishes. After that handler commits, another GET
observes desired state. Both observations are explicitly audited, and neither
unblocks the production journal. The fixture authority covers only its own
in-process server, not an installed Garm service or database topology.

```sh
go test -race -count=1 ./internal/garmmanagement
integration/garm-v0.2.1/management-test.sh /tmp/new-disposable-garm-management
```

The pinned upstream check additionally exercises
`TestCapacityManagementCanceledRequestIsNotFence` against actual v0.2.1 Runner
and SQLite code. No live Garm/Incus/GitHub infrastructure or production authority
is involved. Passing these tests validates the conservative refusal and fixture
contract, not a production recovery capability.


## Lineage guard is not historical rollback authorization

Normal service startup now requires an existing schema2 journal and an independently pinned expected lineage. A missing/empty/different journal cannot silently bootstrap a new history. Explicit new-only initialization is first-install setup, never failure recovery. Schema1 and orphaned sidecars are not auto-adopted.

Matching lineage still does not prove that a same-lineage raw historical copy contains the latest accepted requests. No supported operation activates such a copy. Do not replace the journal or expected lineage to bypass a pending/unknown operation; external request-completion and durable-history fences remain mandatory. A later matching settings GET cannot supply that proof.
