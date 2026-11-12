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
checked by a pinned read-only/no-follow descriptor and a bounded disposable-copy
preflight of initial static schema/history metadata only. The changing original
is NEVER opened as immutable: a cooperating WAL checkpoint can make that unsafe
reader report SQLITE_CORRUPT even when the actual history is valid. The private
0700 directory/0600 copy is closed before SQLite opens it with immutable reads.
Raw copying is NOT a coherent original database snapshot, backup, recovery source
or fact/absence view, and no copied facts are used or returned.

Source and copy bytes are capped at 16 MiB (the format caps facts at 4096). Up to
8 fresh-copy observations share a one-second retry budget, checked between I/O
operations; blocking filesystem I/O itself is not given a hard completion promise.
ONLY raw SQLITE_CORRUPT from parsing the potentially torn disposable COPY permits
another fresh guarded copy. Exact lineage/schema mismatch, filesystem/ownership/
inode/link/sidecar changes, I/O/oversize/cleanup errors, other SQLite errors and all
normal live validation/commit failures immediately refuse. Every copy attempt
rechecks the pinned source descriptor, current main and parent identities, private
modes/ownership/links and sidecars. Copy files/directories are removed on all paths.
This remains accident protection, not hostile same-UID/root isolation.

Actual facts are validated again through a proper live SQLite read transaction,
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

## Checkpoint regression

A valid synthetic writer appends paired contiguous fact/audit rows in small
transactions up to the same 4096 cap while a second connection TRUNCATE-checkpoints
and a third repeatedly opens history. The original immutable-on-live-file code
reproduced SQLite Code 11 at its first application_id preflight, while final proper
locked Snapshot still validated 4096 rows. The unchanged growth assertions and
original checkpoint stress must pass with the private-copy fix. Additional tests
prove bounded copy-only retry/exhaustion, cleanup, oversized/persistently corrupt
original refusal, unrelated/wrong-history preservation, no-follow source identity,
and rejection of broken actual facts despite successful copied metadata. No test
relaxes original live integrity or unknown-commit behavior; no action is enabled.
