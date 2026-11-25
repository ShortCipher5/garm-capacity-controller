# Controlled stock restart binding (development component)

`managedgarm.ControlledRestart` is an explicit, disruptive control-plane operation.
It is not wired into discovery, startup, the allocator, or a command. Nothing is
restarted by installing this code. It does not authorize capacity increases.

The concrete implementation requires an already healthy managed bundle, a real
root supervisor session, authenticated enrolled-profile snapshot, approved
executable hashes, and fs-verity-enabled configuration files. It measures the
actual kernel-protected inode contents; an ordinary SHA-256 string or unsealed
file is refused. Unsupported filesystems fail closed. The component does not
enable fs-verity or mutate configuration files.

Before any Docker effect it records an exclusive, durable one-shot intent with
the profile snapshot and measured file identities. It stops ingress, controller,
then GARM. Only after all three stops are confirmed does it start GARM, controller,
then ingress, ensuring peers enter the new GARM network namespace. Each operation
has a durable pending record before dispatch and confirmation only after actual
root-peer-authenticated Docker response and readback. Already-started responses
are not accepted as evidence of this operation. An ambiguous outcome is not
retried or compensated; records are retained for explicit recovery.

After restart the implementation opens a fresh stock session and rechecks actual
process start identity, topology, providers, profile hash/lineage and sequence.
It compares fs-verity measurements and device/inode identities from both the
protected host configuration directory and the process's actual mounted directory.
The returned in-process object revalidates on each Check and closes on drift.
It cannot be reconstructed merely by supplying a JSON receipt.

## Limitations

- Only an existing managed bundle with unchanged approved container definitions
  is supported. It does not recreate containers, convert an arbitrary GARM
  deployment, or configure new provider credentials.
- It is deliberately one-shot per deployment intent. There is no automatic
  resume after a lost response, failed final revalidation or supervisor crash.
- Root and the Docker daemon remain trusted. fs-verity protects inode contents,
  not names; protected ancestry, exact mount inspection and before/after inode
  comparisons are required together. This is not defense against malicious root.
- Exclusive native authority and safe runner retirement remain separate.
  Neither a successful restart nor its receipt
  is a live allocation capability.
- Tests currently cover the bounded transition journal rules, actual HTTP result
  classification and refusal of unsealed files. Real root/Docker/fs-verity bundle
  qualification is not yet performed. Do not describe this as production ready.

The provider observer now additionally requires a fresh source descriptor from
fixed managed loopback `https://127.0.0.1:9445`. A separate inspector mTLS role,
actual writer server key pin, nonce and verified source/controller response mint
the identity; every enrolled profile must match it. The namespace dialer cannot
replace TLS validation. There is no configured expected-source-hash shortcut.

The root writer now owns a concrete Service lifecycle. Suspension closes its
listener and waits for actual active handlers before revoking the old session;
timeouts refuse the restart. Resumption opens a fresh actual session/listener in
the new namespace while retaining the same durable settings journal. The command
uses this lifecycle, and the restart coordinator journals suspension/resumption.
The lifecycle action hook is not proof: final independently authenticated source,
process and mounted-file observations are still mandatory. No restart HTTP route
or automatic trigger is exposed. Real namespace restart qualification remains
outstanding; do not claim a production-qualified automated deployment lifecycle.

### Inspect historical records

The installation operator can run
`capacity-managed-garm inspect-restart-history <supervisor-spec.json>` locally
as root using the existing protected supervisor specification. It reads only
the protected restart journal and non-secret bootstrap outcome records. It
does not contact GARM, Docker, a runner, or the restart participant.

The output distinguishes a recorded intent, a returned resume hook with no
completion receipt observed, and an internally consistent historical
revalidation receipt. A torn or inconsistent journal is unavailable. A
receipt is not proof of current readiness or of storage durability; it is
never permission to replay, resume, or enable writes.
