# Advisory repository capacity policy

The repository-policy UI stores desired controller configuration. It does **not**
activate a scheduler, enroll pools, edit stock GARM, reserve capacity, retire a
runner, or release any charge. The API explicitly returns `advisory_only: true`.

- Repository identity is the GARM repository UUID, not a GitHub name.
- Equal fairness defaults to weight 1 in the UI; explicit weights are positive
  uint32 values. Repository caps are aggregate ceilings across its pools.
- Each pool belongs to one repository in the document, with its own ceiling and
  soft warm-idle target. Pool cap cannot exceed repository cap; idle cannot
  exceed pool cap. Caps range from zero to 1,000,000. Zero forbids new capacity;
  it does not revoke existing capacity. Pool caps need not sum to the repository
  cap: pools may compete within an aggregate ceiling.
- Added repository/pool drafts default to zero cap and zero warm idle, preventing
  a guessed budget. Operators explicitly choose their desired limits.
- IDs are syntax-checked, **not authoritative inventory-checked**. Runtime
  consumption must verify repository/pool ownership against trusted discovery
  and an enrolled scope before acting. Merely saving a policy grants no authority.

`GET /admin/api/v1/broker-policy` accepts the existing read credential. `PUT`
requires enabled management, the separate management token and the existing
same-origin check. The body is `{revision, repositories}`. Unknown/duplicate keys,
malformed IDs, duplicate pool/repository identities and invalid limits fail closed.

A monotonic SQLite revision and compare-and-swap replacement prevent concurrent
writers from silently overwriting each other. The document, revision and audit
entry commit in one immediate transaction. A lost response is not retried: the UI
blocks further edits until an authenticated reload. Old revisions cannot replay,
including after values are changed back. Reload deliberately discards draft edits.
This is configuration durability, not a remote-write reconciliation protocol.

Schema v12 adds a table to the existing ledger, covered by its consistent backup
mechanism. Integrate this v12 migration before any later schema bump, including
host discovery review persistence. Older binaries reject the newer schema; use a
qualified migration/restore procedure rather than editing `user_version`.

Remaining runtime work: connect verified inventory and demands to planner input,
map declared size/host policies, consume desired policy through an authorized
atomic grant boundary, distinguish desired/granted/actual values, and implement
safe stock-GARM mutation with competing-writer and unknown-outcome handling.
These are not implemented by this advisory editor.
