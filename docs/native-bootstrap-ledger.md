# Schema 22: protected image bootstrap ledger (not deployment approval)

A protected-image v3 policy and canonical `protected-runner-boot/v1` payload are
required. Ordinary v2/cloud-init strings cannot bypass this gate. The payload
contains the exact independently configured image descriptor and ephemeral,
no-update JIT configuration. A codec derives only two root-0600 files at fixed
paths, after the actual successful stopped-CREATE resource UUID is recorded.

`BindNativeBootstrap` recompiles the original native body from immutable policy,
canonical allocation and payload, and requires its digest equal the bound native
execution envelope. This prevents an owner substituting a freeform JIT digest.
The binding stores only exact manifest/JIT hashes and byte counts, image identity,
parent/CREATE lineage and resource UUID. No JIT secret or file contents enter the
ledger or audit. The files include the exact domain, policy and incarnation.

`ClaimNativeBootstrap` returns permission only once after committing an immutable
intent. It rechecks parent ownership, charge, host binding, readiness and epoch.
Lost acknowledgement, partial delivery and owner restart do not permit retry.
An actual pinned controller-origin owner GET supplies the only accepted receipt.
Verified receipts require both files' exact root ownership, 0600 mode, ordinary
file type, bytes and digest. Unknown is permanent and cannot become verified.

START requires a fresh verified bootstrap receipt in addition to CREATE/quota
proof, both through the API and a SQLite insertion trigger. There is no optional
skip. Historical pre-schema22 START records are not rewritten or used to grant
new permission. Migration closes readiness; new starts require the new evidence.
Backup/startup validators retain lineage and reverse audit checks. Supported
artifact restore clears readiness and reconciliation changes the epoch, denying
new claims from pre-restore envelopes. Raw rollback and competing old controllers
still require independently enforced fencing; SQLite is not a global fence.

Readback means bytes were observed, NOT an fsync guarantee, successful guest boot,
network readiness, terminal job completion or physical resource release. Incus
6.0.4 file POST is not an exclusive durable transaction: existing files are not
re-owned/re-moded, partial/lost replies can leave effects, and forkfile helpers
mount stopped storage. The owner must use the pinned clean image's fixed absent
paths, bracket exact stopped incarnation, write once, then verify readback. A
partial/unknown batch cannot start or be automatically retried.

The protected PID1 image/boot path, current runner release qualification, network
bootstrap and end-to-end operator activation remain independent work. No runtime
refusal is removed here, and no GARM/native host has been contacted by these tests.
