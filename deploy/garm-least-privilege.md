# Garm least-privilege qualification, not an installation change

The gateway packaging requires a dedicated **non-root Garm identity**, separate
from the gateway UID, with all capabilities dropped and no access to gateway
credentials or state. A stock image's default UID is not an exemption. Neither a
running container, a working provider binary, nor this lab is proof that an
existing installation meets that boundary.

## What is tested independently

The GitHub-hosted `upstream-garm-nonroot-lab` job uses the exact public image
`ghcr.io/cloudbase/garm:v0.2.1@sha256:5ccdc29c31084ed4f30ec85e09a57b5105cf3dcc5fa8c1aba9e68de461e7e635`.
It deliberately uses **new generated empty state**, one-run TLS/JWT/encryption
material and no configured provider, GitHub credential, repository, pool or job.
No installed configuration, database, volume, certificate or host address is an
input. Public v0.2.1 source resolves to commit
`154638445c3949c1958b01812f69d9a1e4d82684`; the executable test uses the immutable
image, not an assumption that rebuilding that source produces the same artifact.

The disposable runtime uses:

- UID/GID `65531:65531`, distinct from the gateway's example UID 65532.
- All capability sets dropped, no-new-privileges, a read-only root filesystem,
  private PID namespace, no Docker/Incus socket and no privileged mode.
- `network=none`, no published ports, and explicit `127.0.0.1:9998` TLS binding.
  A separate bounded test client shares only this disposable network namespace.
- A read-only private config/TLS bind and a different writable private state bind.
  The only extra writable path is a bounded private noexec/nosuid/nodev `/tmp`
  tmpfs. It is not a persistence or credential mount.
- Explicit `umask 077` in a lab-only shell launcher which **execs** `/bin/garm`,
  not a root entrypoint that chowns/adopts existing data. Garm's own SQLite files
  must be owner-only. The stock image is not modified to achieve these settings.
- Bounded CPU, memory and PIDs, no restart policy and controlled termination.

Checks inspect the actual Garm PID 1 UID/GID and every capability set, not just
Compose metadata or a helper's `id`. The real TLS API must return its expected
`init_required` response while certificate validation remains strict. No admin
account or remote authority is created. The bundled Incus provider executes only
harmless version discovery with network disabled and no provider configuration.

A separate permissions probe gives each UID a read-only mount of the other role's
new private canary/config directory and verifies that reading is denied without
printing its contents. This deliberately adversarial test mount is **not** part
of the Garm runtime. Normal Garm receives no gateway path at all. Garm's own DB
must remain private, empty of users/entities/pools/instances, intact after a clean
stop and identical in controller identity/inode after container recreation.

Artifacts contain only public source/image references, platform and result
booleans. They do not contain generated keys, JWT/encryption material, config,
database contents, controller IDs, container logs, environment or installed facts.
A lab failure stays a failed gate; the harness never adds root/capabilities,
loosens file modes, disables TLS, mounts a socket or modifies the image to pass.

## What this cannot establish

A pass establishes only that this public stock image can run with these
restrictions and fresh empty state in this hosted Docker environment. It does not
qualify the installed container's effective UID/capabilities, engine user mapping,
existing volumes, live configuration, provider migration, patched Garm admission
artifact, credential roles, job lifecycle or private networking.

The stock upstream Garm server can initialize a missing database. That behavior
is intentionally used only for the lab's explicitly NEW state. It is **not** an
existing-only production guard or a safe response to an empty/wrong live mount.
Recreation checks preserve an existing supplied lab database; they do not turn
upstream initialization into wrong-mount protection. A deployment needs a
separately reviewed fail-closed installation/state identity procedure. Never
start an empty replacement controller to repair an ownership or missing-volume
failure, and never copy an open SQLite main file alone.

The example UID is not automatically portable across rootless engines, userns
remapping, Docker Desktop or an existing host's identities. Host owner/mode,
parent trust, mount mapping, filesystem durability and backup preservation must be
qualified on the actual target separately. Host root/the Docker administrator and
the OS/kernel remain trusted; UID separation is not hardware attestation or a
boundary against that administrator.

## Separate operator review before any live change

This checklist is a plan, not authorization or a startup/deployment command.

1. **Read-only baseline:** identify the exact installed image/artifact, configured
   and effective runtime UID, capabilities, NNP, PID/network namespace, mounts and
   their actual ownership/modes. Record only non-secret evidence privately. A
   missing `cap_drop` is not a measurement of the actual process capability mask.
   Do not export ENV, private Compose rendering or credentials into a public PR.
2. **Independent identities:** map Garm, gateway and trusted installer roles.
   Garm gets only its own private configuration/state and provider-only gateway
   client credentials. Gateway alone owns Incus keys, inventory/admin credentials,
   admission ledger and independent identity. The optional Garm-mutation journal
   has separate persistent storage and independently pinned lineage. Do not share
   secret groups/volumes or infer lineage from whichever DB is mounted.
3. **Exact artifacts:** repeat the non-root proof against the actual reviewed
   patched Garm/provider pair, not merely the stock image. Review every init/helper
   process and its writes; non-root runtime does not excuse a privileged helper
   leaving readable keys or unrestricted provider paths behind.
4. **Storage and interruption:** qualify private ancestor directories, effective
   UID mapping, per-file modes and the full SQLite/WAL/SHM directories. Preserve
   Garm's existing encryption/JWT material and all controller/runner history.
   Take separately supported consistent backups without claiming supported
   runtime recovery activation for the admission or mutation-journal archives.
   Those archives remain runtime-disabled/inspection-only under their own gates.
5. **Maintenance/fencing:** plan the controller/provider restart and shared network
   namespace recreation with stopped or effectively fenced old execution paths,
   queued requests and ambiguous creates. Disabling new claims is not job-safe
   drain. Never solve a permissions failure by root, broad chmod/chown, a new DB,
   a changed controller identity, direct Incus access or a bypass provider.
6. **Explicit live authorization:** only after the exact change, rollback limits,
   credentials, timing and affected services are reviewed should a separate live
   maintenance action be authorized. This PR or a green lab does not authorize
   changing UID/capabilities, file ownership, credentials, networking or services.
7. **Post-change evidence:** verify effective daemon identity/capabilities,
   namespace attachment, allowed writes, non-disclosure to the other role,
   durable state identity/history and conservative fresh reconciliation. Stop on
   uncertainty. External writer fencing, complete physical scope, monotonic
   recovery authority and job-safe retirement/drain remain independent gates.
