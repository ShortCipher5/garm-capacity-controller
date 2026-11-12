# Durable daemon bindings (schema four)

This guards known alias/remapping mistakes at the experimental gateway boundary.
It is **not hardware attestation, whole-host capacity proof, external writer
fencing, retirement authority or production qualification**.

After beginning its durable reconciliation barrier, the real gateway obtains a
fresh opaque identity for EVERY configured Incus alias through authenticated TLS.
The reported daemon certificate must match the actual TLS peer key. The complete
alias set is checked and stored in ONE SQLite transaction before project inventory
collection or readiness can complete:

- TLS public-key fingerprints are unique across aliases, even for different
  origins, reported names, versions or projects.
- Canonical HTTPS origins are unique, even with different projects. Case, default
  ports, trailing DNS dots and IP text normalize before hashing.
- Reported names are conservatively unique; origin/project scope and reported
  version are bound permanently to that alias.
- Existing bindings cannot be edited, deleted or silently changed after restart.
  Missing, malformed, stale, incomplete or conflicting observations fail closed.

Multiple aliases/projects on one daemon are not separate host budgets. Supported
collection currently uses one configured project per host alias; a future
multi-project collector must aggregate under ONE physical-host budget rather than
creating another allowance.

## Dispatch and transport

The gateway verifies the current observation against the durable binding and
pins an immutable, fresh TLS connection pool. Existing unpinned pooled sockets
are not reused. Every later handshake checks the expected key BEFORE an HTTP body
is sent; root checks also enforce reported name/version/project scope. Key/name/
version changes block rather than silently requalify. No in-use TLS config is
mutated and no redirect/proxy relaxation is introduced.

`ExecuteDispatchBound`/`ClaimDispatchBound` compare the exact persisted binding
inside the dispatch-intent transaction. Each bound dispatch records the immutable
`HostBindingSequence`, referencing its binding audit record. Legacy and trusted
simulation-library callbacks have zero/unqualified provenance, not an inferred
physical binding. Provider replay cannot promote them to qualified history. The
simulation helpers remain useful tests; the service never selects that path.

The binding audit sequence establishes transactional provenance without relying
on wall-clock order. It does not fence a different database, another credential,
or an external operation already submitted.

## Upgrade and restoration

Owned schemas 1/2/3 migrate to four without releasing reservations, dispatches,
orphans or cancellation tombstones. Upgrade closes readiness and invalidates the
old collector nonce. Any historically charged host is placed in an immutable
`host_binding_history_exclusions` set. **It cannot be automatically blessed by
newly observing a daemon**, even if its old creating reservation is later safely
cancelled. A separately proven scope-import workflow is required and is NOT
implemented. There is no reset/clear/force-bind route. Do not delete tables or
start a blank ledger to make an upgrade run.

Read snapshots expose `HostBindings` and `LegacyUnboundHosts` for operational
status. A UI must distinguish bound observation, unbound and legacy scope-import
required, never label a bound observation as production-qualified physical scope.
The separate read-only admin listener remains useful when reconciliation is
blocked; it cannot create a binding or bypass legacy provenance.

Consistent library/CLI backups preserve bindings, binding-sequenced dispatches,
legacy exclusions, cancellation history and audit references. Artifacts remain
admission-blocked, with old collector fences unusable. Opening a copy does not
revoke the old gateway's credentials. Older binaries refuse schema four; do not
roll an image back against a migrated file or hand-edit the schema number.

## Still open before live qualification

Two separate daemons can occupy the same physical host. A copied key or misleading
reported name is not a trusted physical asset registry. A project inventory also
cannot see hidden projects/foreign writers. Require qualified external unique
host registration plus exclusive coverage or enforced hard native parent budgets.
The actual image/profile must be tested for effective CPU/RAM/PID/disk/swap/socket
isolation and parent health. Names/labels alone do not prove instance incarnation.

A missing/wrong admission-ledger runtime mount can still be mistaken for an initial
offline-store creation until the separate admission runtime initialization/expected-
lineage slice lands. The GARM mutation journal has a separate identity and does not
protect this admission ledger.
Same-UUID historical clones need external history/fencing, not just a UUID check.
A real disposable two-gateway/separate-ledger credential-revocation/handler-drain
restore drill remains required. Operator acknowledgement alone is not that proof.
No live scheduling, automatic host import, destructive lifecycle or job-safe drain
is authorized by this document.
