# Existing-only runtime ledger lineage

Both default read-only and provisioning `capacity-gateway` startup require an
existing explicitly initialized ledger and an independently protected identity
file. `initial_policy` cannot cause startup to create or adopt state. Missing,
zero-byte, foreign, offline, legacy, wrong-ID and archive/restore databases fail
closed before schema migration, WAL configuration or readiness changes. Rejection
preflight reads checkpointed immutable lineage with SQLite's immutable read-only
mode, which cannot create WAL/SHM sidecars. This mode is never used for budget,
readiness, inventory or dispatch decisions. The normal transactional connection
then checks current identity, including WAL state, before ledger operations.

## Explicit initialization, not recovery

In a private owner-only real directory, with a private admission policy JSON:

```sh
capacity-ledger initialize --destination /private-state/ledger.db \
  --identity-file /independent-private-config/ledger.identity \
  --policy /independent-private-config/initial-policy.json
```

The policy is an `admission.Config` object, not a gateway configuration wrapper.
Configure gateway `database` and `identity_file` with these exact paths. The
identity file is owner-only and contains a random 256-bit identifier. Protect it
outside the database volume and do not derive or regenerate it from whichever
ledger is mounted. The identifier is not a password or external fencing token.
Initialization checkpoints immutable metadata into the database before recording
the independent identity, fsyncs outputs and parent directories, and leaves
admission blocked. Runtime still requires fresh authoritative reconciliation and
all existing identity/provenance gates. Initialization never starts a service.

Initialization refuses any existing database (including empty), identity file,
symlink, or orphaned WAL/SHM/journal sidecar. It does not chmod, adopt, overwrite,
activate legacy state, or provide a force flag. A failed or interrupted
initialization can leave partial outputs. Preserve and inspect them offline;
never delete/retry over them as an automatic recovery strategy. Files and their
direct parents must be private and owned by the effective user. Ancestor
directories and the OS identity remain trusted, as for service credentials.

The offline/test `OpenSQLite` API remains available and migrates owned legacy
ledgers for accounting inspection, tests and exports. Its new ledgers and legacy
migrations receive runtime-disabled `offline` kind, never active lineage.

## Artifacts cannot become runtime state

Supported consistent online backups and restores retain lineage, host bindings,
per-dispatch provenance, legacy exclusions, CREATE evidence, cancellation
history and charges. They carry permanent `archive` or `restored` runtime kind,
separate from readiness and reconciliation nonce. Even matching the independent
ID and completing offline reconciliation cannot activate an artifact. The SQL
interface prevents identity mutation and transitions back to active. No runtime
activation API, operator boolean, or recovery command is provided. Supported
restore remains a conservative offline artifact workflow only.

Runtime operations check that the database path is still the original private
regular file and validate active matching lineage in each transaction. Removing
or replacing the database path while the process is open blocks further ledger
operations instead of continuing under an orphaned/rebound pathname.

## Explicit unsolved limits

A UUID cannot detect rollback to a raw historical **active** copy with the same
ID, including a complete old volume snapshot. It cannot prove that a matching
ledger includes every post-snapshot dispatch. Such raw active clones remain
indistinguishable from the original and are not a supported recovery method.
Do not copy the main file of an open WAL database. A real recovery/activation
workflow needs a separately durable monotonic history authority, preservation
and qualification of newer dispatch/in-flight evidence, effective revocation or
fencing of old writer credentials/handlers, backend transaction fencing, and
fresh complete authoritative inventory. This implementation does not provide
those external authorities or claim they exist.

Owners can edit their database or identity files; SQL triggers are not a defense
against that owner. Concurrent directory/path tampering by the trusted OS owner
is outside this protection. Independent ledgers controlling a shared physical
host still have no common atomic capacity authority. Neither lineage nor local
SQLite barriers fence Garm, Incus, provider processes, queued requests, another
credential, or a different ledger. No live infrastructure qualification or
runtime recovery activation is claimed.
