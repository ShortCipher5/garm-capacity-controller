# Cancellation of a never-dispatched reservation

This is a **trusted SQLite transaction** and authenticated experimental provider
command, not runner retirement or provider DeleteInstance. It makes no GitHub or
Incus request. No browser mutation, timeout-based release or live rollout is
enabled. The optional pinned Garm preparation-failure hook is a separate dependent
qualification; this command does not itself automatically clean up JIT failures.

`CancelAdmission` uses the same verified provider mTLS identity and trusted fixed
pool placement as admission. `/v1/cancel-admission` is unavailable in default
read-only mode. Its exact bounded JSON receipt requires exit zero,
`outcome: cancelled`, matching name/pool, `dispatched: false`, durable reservation
key and explicit replay boolean. The caller cannot select arbitrary host/resource
values. An unconfirmed cleanup is not proof of release: its reply may be lost
after the tombstone commits. Retry only the exact identity to inspect that
idempotent result. A refused transaction retains its charge. Cleanup errors must
not replace a JIT or database error with benign defer. The separate admin read credential cannot call
this route.

`CancelUndispatched(ctx, exactOriginalRequest)` uses ONE BEGIN IMMEDIATE
transaction to require current readiness, fresh complete baseline observations
for every host, a healthy target, an exact still-creating reservation, no dispatch
journal entry in ANY state and no inventory resource linked to its key. It writes
an immutable exact-request tombstone, removes only that reservation's charge and
audits the cancellation in the same commit. ClaimDispatch uses the same durable
serialization point. Only one can win, across handles or processes. Audit failure
rolls everything back; ambiguous commit poisons the handle instead of reporting
success. An intent whose callback never ran remains charged.

An exact cancellation retry returns the original record with Replay=true and
never releases capacity again. Admit/ClaimDispatch return ErrCancelled for a
cancelled identity; altered identity-bound parameters return ErrConflict.
SQL triggers prevent tombstone mutation/deletion and reservation reinsertion or
rekeying to a cancelled key. LookupCancellation is physically read-only.

Snapshot/ReadSnapshot expose at most the last 100 inserted tombstones as
RecentCancellations plus atomically maintained CancellationCount. This is bounded
administrative history, NEVER complete authorization or absence proof. Indexed
lookups cover every permanent key, including records omitted from recent history.
Active allocations are checked against the entire tombstone index.

## History and recovery limits

Absence of a dispatch row proves never-dispatched status only in authoritative,
complete ledger history where EVERY provisioning path uses ClaimDispatch.
All preexisting v1/v2 reservation keys are permanently excluded during schema-v3
upgrade: version one lacked the journal, and version two may descend from version
one. Only newly admitted v3 identities can qualify. Legacy cleanup needs a
separately reviewed provenance/retirement workflow, never a schema-version guess.

Tombstones survive reopen and backups/restores containing them. An older backup
cannot preserve later cancellations or dispatches. Before historical restore,
fence every old writer, credential and queued request, recover later history or
quarantine all old identities, then collect NEW complete authoritative inventory
behind a new barrier. Fresh-ready checks block immediate cancellation from a
restored snapshot; they do NOT implement external fencing or certify completeness.
An old backup plus an empty list cannot substitute for missing history.

All existing dispatch states, linked resources and idle/running/failed/unknown/
deleting reservations remain charged. Timeouts, age, errors and missing list
entries never authorize cancellation. Reappearing cancelled correlations reject
reconciliation; an unlinked physical orphan must remain fully charged, not skipped.

## Verification

Only disposable local ledgers are tested. Coverage includes exact/altered replay,
all dispatch and reservation states, linked resources, invalid/missing requests,
audit/context rollback, claim/cancel races across handles and processes, freshness,
drain baselines, historical restore, reopen/read-view/backup preservation, v1/v2
migration exclusions, bounded history and cancelled-identity reappearance.

This source and equivalent safety regressions were reconstructed after the cloud
workspace reset. They receive fresh validation; the lost local commit hash is not
claimed to have been recovered byte-for-byte. No infrastructure was accessed.
