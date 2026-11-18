# Native retirement intent and operation ledger (schema 25)

This is a dormant, first-use protocol component, not permission to retire a
production runner. Runtime-lineage-bound stores reject stop/delete claims.
There is no new capacity release path or physical absence authority.

An intent joins the immutable schema 24 root fence, exact schema 23 terminal job,
successful create/start/bootstrap and original measured dataset GUID, with a fresh
pinned owner journal and current reconciliation epoch. It freezes the instance
UUID, policy/domain, request, owned storage identity and root fence digest.

A stop claim is issued once to the exact enrolled owner leaf. The fixed request
is graceful stop, timeout 30, force false, stateful false. Delete is a different
one-use claim, available only after an authenticated successful stop operation.
Each claim binds a canonical request digest. Lost claim ACKs cannot reissue;
transaction failures return no detached claim. Accepted operation IDs cannot be
reused across creation/retirement or distinct retirement steps. Authenticated
terminal observations are immutable; unknown cannot be upgraded to success.
Restart and supported backup/restore preserve these claims and charges, while
the reconciliation epoch forbids stale new effects. Arbitrary owner/filesystem
rollback and competing-writer fencing are not solved by a SQLite counter.

Remaining integration is explicit: real native stop/delete adapters and central
RPC orchestration; qualified owner/cgroup acceptance fencing; immutable owned
storage and held-process absence proof; and a canonical release transaction
accepting that proof. A native successful DELETE or HTTP 404 is **not** proof of
physical completion and does not refund capacity. No deployment or live host
operation is performed by tests. Actual TLS owner/stock/GitHub fixtures and real
SQLite exercise the protocol without simulating production authority.
