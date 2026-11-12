# M1: non-executing local activation-claim history

`internal/supervisoraudit` implements real SQLite append-only facts for the
[one-use supervisor plan](one-use-supervisor-threat-model.md). It does not implement
a supervisor, consume an authoritative activation slot or issue a launch token.
Nothing imports it from admission, gateways, services or runner launch paths.
There is no enrollment, job execution, network, deletion, drain or capacity release.

## What this advances

Cooperating local writers serialize caller-claimed scope/attempt digest pairs in
`BEGIN IMMEDIATE`. Scope and attempt each have a unique index. Exact replay returns
the immutable original sequence; cross-scope or changed-attempt reuse refuses.
Fact and audit rows commit together under WAL/FULL synchronous settings.
Connection-local FULL synchronous and foreign-key enforcement are set in the DSN
for every connection, including replacements. An unknown
commit poisons that handle. A new handle can recover historical facts, never a
permission to retry execution. Sequence ordering says nothing about server clocks.

`InitializeNew` exclusively creates a new 0600 file in an existing private parent.
`InitializeNew` refuses every existing associated `-wal`, `-shm` or `-journal`
path before creating the main file, including private regular orphan files; it
never adopts, truncates or cleans them up. A second check before SQLite catches
observed races, but this is not hostile-writer isolation.
`OpenExisting` requires the expected random local history ID. Both paths refuse
symlinks, hardlinked files, wrong ownership, unsafe modes and missing/wrong history. Existing databases are first
checked by raw header and a no-write immutable preflight of the initial,
checkpointed schema/history metadata only, without quick-check or fact/audit
scans. That stale preflight is ONLY accidental wrong-file
protection. Actual facts are validated again through a live SQLite read transaction,
and record/snapshot operations validate within their serialized transaction.
No live fact, uniqueness or absence decision uses immutable reads. Failed
initialization is left in place, never removed or silently repaired.

For this deliberately small non-executing foundation, history is capped at 4096
facts and fully checked inside each transaction. This trades scalability for
strict corruption refusal; total insertion work is quadratic up to that hard cap.
Exhaustion refuses new claims and must NOT be solved by resetting history.

Schema and immutable-trigger definitions, canonical lowercase 64-hex digest
claims, contiguous sequence and exact matching audit rows are validated. Recent
snapshots are limited to 100 facts plus total count; they are explicitly incomplete.
Uniqueness uses the complete indexed history, not that recent list. The fixed
boundary text accompanies observations and snapshots even on errors.

## Hard limits, demonstrated rather than hidden

The caller supplies the digests: they are not measured/enrolled identities.
A fact with `Replay=false` means only “newly appended to this local history”. It
MUST NOT cause launch. `Replay=true` also grants no action. The package supplies no
native handle, runtime frontier, remote assignment/control fence, peer attestation,
credential separation, job sandbox or installed artifact qualification.

The local history ID and path/inode/mode checks detect some accidents, not a
malicious same-UID/root actor, copied credentials, independent writer, old backup
or host rollback. There is no independent anchor. A test restores an older CLOSED,
checkpointed database with the SAME history ID and successfully records the same
claim again. That is a counterexample to no-restart authority, not a feature for
reinitialization. Copying an open WAL main file is NOT a backup procedure.

This package must remain disconnected from activation until a separately reviewed
independent rollback/uniqueness anchor and all-entry-path authority/isolation
boundary exist. Even those would not establish GitHub service consumption,
accepted runtime work resolution or backend physical retirement/release evidence.
All those gates remain absent. SQLite triggers are corruption guards, not protection
from an administrator who can rewrite the database.

## Validation scope

Tests use real SQLite across handles and independent copies of the package's own
Go test binary. Children have a minimal explicit environment and no worker/service
role. Abrupt process exit before/after commit exercises rollback and WAL recovery.
A lost commit response demonstrates poison and historical replay on reopen.
Other tests cover bounded snapshots, conflicts, immutable audit, unrelated-file
preservation, path/corruption refusal and the closed-snapshot rollback limitation.
No tests install software or access live infrastructure. Local fsync semantics
still depend on the filesystem/storage; there is no power-loss qualification here.
