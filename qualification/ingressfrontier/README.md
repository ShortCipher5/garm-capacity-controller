# Actual HTTP ingress frontier experiment, not a native fence

**Test-only.** These files compile only as Go tests. They are not an installed
broker, a resource limiter, a production journal format, an Incus extension, a
release permit, or an API for enabling physical retirement. They introduce no
application imports, endpoint, policy setting or ledger schema change.

## Implemented and exercised

A disposable mTLS HTTP ingress grants one generated leaf per fixture. It records
a validated CREATE intent in real SQLite WAL/FULL with `BEGIN IMMEDIATE` before
forwarding. Stop-new atomically seals admission and captures the last accepted
sequence. The epoch is checked per request, including an already-authenticated
pooled connection. This is actual HTTP and SQL, not a mocked mutex-only commit.

Accepted work is marked attempted durably before network I/O. A lost response,
process death or non-200 result keeps the request and charge. A response is stored
as `response_observed`, never terminal/absent/released. Retrying the same intent
cannot perform another POST. There is no resume, release, delete or fence-complete
operation. The experiment intentionally cannot turn its history into authority.

The fixture demonstrates why stop-new is not fence-complete: a request accepted
before sealing can later POST; a native handler already accepted before a reported
operation can finish after two empty instance/operation reads. Both remain charged.
Three subprocess exits exercise durable acceptance, durable-attempt/pre-network,
and returned-native-response/pre-journal-update boundaries. Process tests use only
a parent-owned loopback fixture, with a bounded environment and timeout, not native
services. mTLS authentication is tested separately on the ingress/network cases.

Counterexamples remain explicit:

- Two independently initialized journals still double a shared physical allowance.
- A consistent SQLite snapshot predating a seal can accept an old epoch after the
  original is sealed. A local epoch is **not** an external rollback-resistant anchor.
- An unmediated native endpoint still accepts writes after the ingress seals.
- Successful responses retain physical allocations; they are not absence evidence.
- The prototype has no native resource incarnation, hardware mapping, full project
  visibility, accepted-runtime frontier, credential revocation or job-safety proof.

The schema and helper methods are deliberately fixture-only; they are not designed
for production filesystem hardening, migration, recovery or arbitrary operators.
An authenticated caller can supply fixture allocation facts; these are not signed
native measurements. Do not lift this package into the service as a trust adapter.

## Pinned Incus source premises

Source reviewed: official Incus 6.0.4,
[`a99413afd0c742da3bba10eb4f75d639f9dbbd32`](https://github.com/lxc/incus/tree/a99413afd0c742da3bba10eb4f75d639f9dbbd32).

- `cmd/incusd/instances_post.go`: `instancesPost` begins at line 894, body decode
  at 910 and DB transaction at 964. Helper-specific `OperationCreate` occurs later
  in request execution. An operation list does not enumerate every already
  accepted HTTP handler.
- `internal/server/operations/operations.go`: `OperationCreate` checks the global
  daemon shutdown context, not a per-controller epoch. Terminal entries are removed
  after five seconds. This is not durable accepted-request outcome history.
- `cmd/incusd/instance_delete.go`: line 77 loads by project and name and the
  operation calls `inst.Delete(false)`. This is not a caller-supplied immutable
  incarnation conditional-delete contract.

Exact file hashes are enforced by the optional `TestPinnedIncusSourceIdentity`.
It is a **source identity/shape check**, not execution or compilation of Incus.
The native HTTP fixture represents the documented pre-operation uncertainty;
no passing fixture result qualifies Incus or the installed server.

```sh
# Actual HTTP/SQLite/subprocess experiments, no live infrastructure:
go test -race -count=3 ./qualification/ingressfrontier

# Additionally verify the reviewed three files at the exact source pin:
GCC_INCUS_FRONTIER_SOURCE=/path/to/disposable/exact-source \
  go test -race -count=3 ./qualification/ingressfrontier
```

Without the source path the source-only check skips explicitly; the network/SQL
experiments still run. No toolchain, native daemon or package installation occurs.

## Narrow implementable next boundary and hard prerequisites

This experiment can inform an actual **host-local sole credential-owning ingress**
only after the installation prevents all direct native writes. One initial profile
could be a dedicated container-only runner partition and a single broker with an
independently enrolled physical domain/native parent. Every CLI, UNIX socket,
network, cluster/internal call, restore, autostart and privileged administrator path
must be denied or explicitly covered under the trust model. A proxy by itself is
not exclusive ingress.

Its durable frontier must include every accepted request before forwarding,
including all queued/retrying connections. A successor cannot activate while an
old accepted request is unresolved. Native accepted work must have a durable,
authoritative outcome spanning pre-operation handlers, async operations and actual
resource birth; stock operation lists do not provide that. Unknown outcomes cannot
be cleared by retries, elapsed time, missing rows, controller restart or a boolean.

Physical release additionally requires immutable incarnation-conditional mutation,
complete domain coverage, no pending recreation, and the independent job-safe
supervisor/completion boundary. Root/controller privilege, a copied certificate,
project identity or a cgroup path do not prove those properties. The future host
journal needs independently anchored history against rollback; restarting it from
a snapshot is not safe epoch recovery.

Until these native/installation contracts exist, the honest useful result is a
bounded stop-new gate with **retained unknown charges**, not an automatic reclaim
path. Native caps may separately contain resource usage but cannot make a delayed
mutation or a live job safe to delete.
