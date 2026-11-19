# Durable root network closure receipt (schema 24)

The protected root PID1 waits for every descendant, disables the fixed guest's
sole external interface through the kernel, verifies interface identity and down
state, and only then publishes its durable root journal. It never kills active
jobs. Unknown network topology, failed mutation/readback or failed journal fsync
leaves the prior unknown record and does not authorize a retry of the runner.

`RecordNativeRootFence` accepts only the actual pinned owner observation, joins
it inside one transaction to the exact schema23 terminal GitHub job and native
incarnation, verifies current reconciliation epoch and configured protected image,
and atomically appends an immutable receipt and audit record. Repeated identical
observations retain the first observation time; changed records conflict. Reads,
startup and backup validate historical lineage and reverse audit membership.
Migration from schema23 closes readiness without releasing charges.

The recorded qualification is **local_network_closed_containment_unqualified**.
This is not GitHub credential revocation, a claim that credentials never escaped,
or proof that the host prevented other namespace/process paths. It never writes
release state or deletes a resource. Qualified host ownership, immutable image
boot, actual kernel network fixture and physical deletion remain prerequisites
for a later safe retirement path. No running native owner is enabled by this code.
