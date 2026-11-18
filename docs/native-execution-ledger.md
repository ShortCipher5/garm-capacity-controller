# Native execution ledger (schema 19)

This code is an offline building block, not production activation or proof that an
installed host has an exclusive native-write boundary. The stock runtime refusal
remains unchanged. No daemon is deployed, no workload is retired, and no capacity
is released by these APIs.

## Enrollment

A trusted controller configures distinct exact DER leaf fingerprints for owner
client and owner server roles, a frozen policy digest and controller/host identity.
`nativeidentity.Observe` reads `/v1/native/domain` over a fresh, verified, pinned
TLS 1.3 mutual-TLS connection. The result is opaque; request JSON, an installation
receipt or an operator boolean cannot construct it. Redirects, duplicate/unknown
fields, different pins, reused controller/owner credentials and unexpected native
version or domain-derived origin/scope are rejected.

The descriptor must come from the actual private native daemon. The independently
bound host's native identity and binding sequence must match that live observation;
enrolling a new domain against an old unrelated daemon is refused. A controller
may use `ObservedNativeHostBinding` during its existing reconciliation fence,
then enroll the exact result. Domain, host, policy and owner roles are immutable.
Certificate/domain replacement is not implemented by overwriting enrollment.

## Canonical downstream claim

`BindNativeExecution` runs inside the canonical stock dispatch callback before
remote delivery. It binds a charged stock-origin intent to the enrolled domain,
exact execution plan and resources, permanent native name and actual request-body
hash. The native name can never be rebound in that domain. This is not a second
allocation ledger or permission to reconstruct the original prepared path.

The owner validates the received body against its closed policy and the immutable
execution envelope, then calls the controller with its actual verified owner-role
TLS identity. `ConsumeNativeExecution` commits one permanent claim and returns a
fresh receipt only once. Exact replay returns `ErrNativeConsumed`, not the prior
receipt. The owner must act only on its own fresh successful claim exchange,
never a detached serialized receipt. Losing the response leaves the dispatch
charged and uncertain, without permission to retry the native mutation.

The envelope pins the reconciliation epoch. Supported backup/restore artifacts
clear readiness; the mandatory new reconciliation advances the epoch and rejects
a pre-backup envelope, even if the original consumed after the snapshot. This does
not promise anti-rollback against an owner restoring arbitrary raw filesystem
images while bypassing the supported restore/fence procedure. Nor does this
SQLite-local epoch fence a stale controller still writing against its original
active database. Independent host authority must fence old writers before a
restored controller can operate; production activation remains closed until that
external boundary is implemented and qualified.

Unknown/submitted/confirmed intents cannot mint a new claim. Cancellation,
capacity accounting, current host membership/binding/health and budgets are
checked under the same SQLite transaction. An envelope-bound dispatch cannot
record submitted/confirmed before consuming the claim. Migration closes readiness
and never invents enrollment or native authority. Startup and backup validate both
forward records and reverse audit linkage.

## Still required outside this ledger

The actual Domain-backed descriptor and closed native request compiler; dedicated
owner and controller HTTP transports enforcing the fresh-response rule; protected
host installation/exclusivity qualification; runtime wiring and full lifecycle
reconciliation remain separate integration work. This claim does not establish
job-safe deletion or physical-completion evidence. Timeouts, list absence,
restarts, lost acknowledgements and claim consumption do not free capacity.
