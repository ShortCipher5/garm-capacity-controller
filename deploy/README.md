# Experimental deployment packaging

This directory supplies an **opt-in packaging candidate**, not a live deployment,
provider migration or production-ready controller. It does not change any stack,
install credentials, create a policy, turn on admission, or retire resources.
The Compose overlay starts with provisioning and ledger management OFF. The admin
UI is read-only in this preview; the source supports a separately credentialed,
explicitly opted-in policy editor, but this overlay does not enable it.
Do not replace a working Garm provider: physical retirement and job-safe drain
are still missing. Packaging cannot make those missing lifecycle features safe.

## Deployment topology and choice

The gateway accepts only explicit loopback listener addresses. Do not patch that
check away, add host networking, or expose an unauthenticated forwarding proxy.
For an existing Compose Garm stack, the candidate overlay shares the **Garm
service's network namespace only** (`network_mode: service:garm`). Garm's external
provider can then call `https://127.0.0.1:9443` without any published gateway port.
Provider RPC still requires TLS 1.3, a trusted server certificate, a verified
client certificate and the exact configured client-leaf fingerprint.

Network sharing is not credential sharing. The gateway runs as UID/GID 65532;
Garm must run under a different, dedicated **non-root** UID, with all capabilities
dropped and no Docker/Incus socket, host PID namespace, privileged mode, or access
to gateway credentials/state. A root or privileged Garm container and untrusted
jobs colocated with control-plane credentials do not meet this isolation model.
Do not mount any gateway private/state directory into Garm or runner guests.
The read-only provider configuration and client keys belong only to the Garm UID.
Separate OS users but one shared secret volume is not isolation.

The exact stack service name, network routes to trusted Garm/Incus APIs, DNS,
UID mapping (including rootless/user-namespace remapping), filesystem backing,
and certificate names must be checked before this overlay can be applied.
`service:garm` is a template reference, not discovery. Do not create a dummy Garm
service to satisfy Compose validation. Recreating Garm changes its network
namespace; stop/recreate the gateway with it during an approved maintenance window.
Do not assume an orphaned sidecar still shares the new Garm listener namespace.
The overlay has no `ports`, host network, proxy, socket, or automatic restart.
It caps the candidate gateway at one CPU and 512 MiB; account for that controller
overhead separately from runner budgets and qualify the limits on the actual
ledger/inventory size. An OOM/limit failure does not prove resources disappeared.
The 45-second stop grace exceeds the two sequential 15-second listener shutdown
budgets plus overhead; forced termination still requires conservative recovery.

If sharing a namespace conflicts with the stack's security boundary, run the
services as separate native Linux OS identities on the same machine, or use a
reviewed end-to-end authenticated private tunnel. A normal Compose bridge cannot
reach a listener bound to another container's loopback. Choosing a new topology,
listener exposure, or proxy is a separate security review, not an overlay tweak.

## Offline build and artifact handling

Build in a clean checkout after the normal repository checks. Linux + a C compiler
and CGO are required for the gateway. The Dockerfile builds the actual SQLite
implementation with CGO enabled, and the React production assets from the pinned
npm lockfile. It does not contain a demo backend or fixture data. The provider-only
artifact also requires CGO because of transitive package dependencies; it contains
no deployment configuration or credentials. All three Go binaries are dynamically linked
against the builder's Debian libc. Never set CGO to zero to bypass SQLite.

```sh
# Local-only image build, not a publish or deploy command.
docker build --target gateway -t capacity-gateway:review .
# Separate local-only recovery tool image; it does not start a gateway.
docker build --target ledger-tool -t capacity-ledger:review .
# BuildKit exports the provider binary, not an image that can run Garm.
docker build --target provider-artifact --output type=local,dest=./provider-export .
```

`.dockerignore` is an allowlist: private configuration, keys, tokens, ledgers,
Git history, existing node_modules and generated assets do not enter the context.
Never pass credentials as build arguments, ENV, build context, or image layers.
The image includes public application code and assets only. The tagged upstream
base images are **not immutable locks**: resolve, review, and pin their digests
and record the source commit, platform, dependency checks and final image digest
in the private deployment change before publishing or using the image. The local
review tag above is not a deployment reference. The overlay requires an explicit
image value; use an immutable digest, never `latest` or a moving review tag.

Review the provider binary and exact Garm compatibility patch separately. Export
and install it only in a reviewed Garm image matching the qualified OS/architecture **and libc/loader** (Debian bookworm in
this candidate). An Alpine/musl or older-glibc Garm image cannot be assumed
compatible. Qualify the exact artifact inside the intended Garm runtime before
installation; do not replace that runtime merely to satisfy this packaging.
Do not replace the current provider or enable its admission hook as part of a
read-only preview. The overlay does not change the Garm service or supply its
private configuration. See [the pinned qualification](../docs/protocol-qualification.md).

## Private configuration and durable state

Use an operator-controlled private deployment directory outside this public
repository. Do not publish an actual stack, endpoint list, host inventory,
budget, certificate, token, or output of a rendered private Compose config.
The overlay accepts only directory locations and an image reference as variables,
never secret values in environment variables or arguments.

Precreate two separate real directories on a trusted **local** filesystem:

| Container path | Owner/mode | Purpose |
| --- | --- | --- |
| `/run/capacity-private` | gateway UID 65532, `0700`; files `0600` | read-only gateway config, independent ledger identity, TLS and API-token files |
| `/var/lib/capacity-gateway` | gateway UID 65532, `0700`; files `0600` | persistent ledger **and** its WAL/SHM sidecars |

Account for the Docker engine's UID mapping when setting ownership on the host.
Ancestors must be trusted and not writable by jobs/unrelated users. No symlinks,
shared group permissions, world-readable Docker secrets, automatic host-path
creation, NFS/SMB, temporary state volume, or shared independent writers. The
container runs non-root with a read-only root filesystem; only the state bind is
writable. Do not loosen ownership checks or run the gateway as root to repair a
mount. Do not mount the main database file alone: SQLite must create WAL/SHM in
its parent directory. Backups and host filesystem snapshots need a separately
qualified consistency procedure.

Construct the private `gateway.local.json` using
[the full gateway configuration contract](../docs/gateway-service.md). Set:

- `listen`: `127.0.0.1:9443`.
- `database`: `/var/lib/capacity-gateway/ledger.db`, an existing explicitly initialized runtime ledger.
- `identity_file`: `/run/capacity-private/ledger.identity`, owner-only and independently protected outside the state bind. Never derive or regenerate it from a mounted database.
- Every gateway TLS/token path: a private regular file under `/run/capacity-private`.
- `controller`, trusted hosts/projects, fixed pool placement and read-only Garm
  inventory credentials: actual reviewed values. Persisted initialized policy is
  authoritative; `initial_policy` cannot initialize or replace runtime state.
- `provider_fingerprints`: only exact authorized DER client-leaf SHA-256 values.
- Optional `admin.listen`: `127.0.0.1:9444`, separate server TLS/read-token files,
  and `admin.assets`: `/usr/local/share/capacity-admin`. Omit `admin` to disable it.

The JSON shape is intentionally not filled with guessed infrastructure or budgets.
The runtime validates it and fails closed. All configuration, CA, certificate,
key and token files must belong to the running UID, even public CA files.
Do not use default Compose file-backed `secrets` permissions without verifying
ownership/mode: common root-owned `0444` mounts are deliberately rejected.

The provider's **separate** private config/key directory belongs to the Garm UID.
It contains only controller identity, `https://127.0.0.1:9443` and provider mTLS
files, not Incus credentials, Garm inventory token, admin read token or ledger.
Both server certificates must cover the configured IP/hostname. Do not disable
TLS verification. Plan approved certificate/read-token rotation and log retention;
rotation currently requires private file replacement plus a controlled restart.

## Preview acceptance and publication boundary

Run `go test -race -count=1 ./...`, `go vet ./...`, `gofmt -l .`, and the web tests
and build. The packaging contract tests run as part of the Go suite. Container
builds and disposable Compose runtime checks are **additional** gates, not covered
by those static tests. Validate the merged private stack with the supported Docker
Compose plugin offline before any start; do not share its rendered output publicly.
The overlay requires `CAPACITY_GATEWAY_IMAGE`, `CAPACITY_PRIVATE_DIR`, and
`CAPACITY_STATE_DIR`, and is inactive unless profile `capacity-preview` is selected.

A reviewed, authorized first run must leave the supplied command unchanged:
there is no `-allow-experimental-provisioning` or `-allow-experimental-management`.
Do not supply `admin.management_token_file` in the preview configuration. The
source policy editor is a separate reviewed change, not a packaging default.
There is deliberately no automatic health check: a listening process or ledger-read
success does not establish complete physical scope, fresh authoritative reconciliation
or admission qualification.
Do not report the stack production-ready from an HTTP 200 or container "running".
The preview performs authenticated external inventory **reads** and writes local
reconciliation/audit state. Runtime startup is existing-only and never initializes,
adopts or migrates missing/legacy/foreign state. Prepare a NEW private test ledger
and independent identity only through the explicitly authorized `capacity-ledger
initialize` operation described in [runtime lineage](../docs/runtime-ledger-lineage.md).
Never derive the expected identity from the mounted database or add initialization
to an entrypoint, restart, install or upgrade hook. Supported backup/restore
artifacts are permanently runtime-disabled, even with a matching identity; do not
point the gateway at one for a compatibility check or claim a recovery activation.

Before an authorized disposable runtime check, verify:

1. All container user/capability/mount boundaries above; private files fail closed
   with wrong owner/mode. The Garm identity cannot read gateway secret/state paths.
2. The gateway uses actual CGO SQLite WAL + FULL durability and preserves state
   across gateway recreation. Refusing a stale/failed inventory leaves admission
   blocked. Never equate a single missing resource with deletion evidence.
3. Unauthenticated, wrong-leaf and wrong-role calls fail; the provider can reach
   the loopback mTLS listener; no other network interface exposes either listener.
4. The optional UI uses the separate read credential and actual ledger; it cannot
   admit/create. For remote access use an approved end-to-end TLS tunnel to this
   namespace, not a new published provider port or public bearer-token URL.
5. Stop/recreate Garm and gateway together in the disposable stack and verify
   network namespace attachment. An interrupted gateway does not redispatch old
   intent, release unknown resources or silently re-enable creation.

No startup/deployment command is given here: applying this overlay can start or
recreate the Garm dependency and needs its own reviewed change and authorization.
Publishing a generic packaging PR is not permission to touch a live stack.

## Recovery, rollback and remaining blockers

Preserve the entire private state directory and stop/fence every old writer before
restoring or changing versions. Never copy only an open `.db`, delete `-wal`/`-shm`,
run `docker compose down -v`, or clear a ledger to fix a failed preview. Use the
[consistent backup/restore API, local `capacity-ledger` CLI and
procedure](../docs/backup-restore.md). The CLI is available in the separate
`ledger-tool` image target, not in the gateway image or as a Compose service. Its
default command only prints help. It is an offline recovery primitive, not a
production backup service or proof that old writers are fenced. Keep credentials
separately. A historical ledger cannot prove later external work vanished.
Normal runtime requires the current initialized schema and matching independent
identity. Supported archives/restores never become runtime state, and no activation
command or boolean exists. Offline inspection/migration does not activate them.
A raw historical active copy with the same identity is not rollback-detectable and
is not a supported recovery route. A different image may refuse the database. Do not downgrade/hand-edit
schema or substitute an older ledger to force startup. Preserve evidence, fence
old credentials and preserve artifacts for offline inspection. Fresh authoritative
reconciliation and external fencing remain necessary but cannot activate a supported
archive or restore. A safe live recovery/activation workflow remains unimplemented.

Still required before integration into an existing runner stack: exact installed
Garm/provider compatibility, full physical-host scope and bypass fencing, upstream
credential/role review, tested container build/runtime and filesystem durability,
operator-approved networking/admin access, owned-schema backup/rollback procedure,
and the missing proof-based physical retirement/job-safe drain lifecycle. This
packaging does not authorize production scheduling or change those gates.


## Separate local recovery-tool image

Use only an immutable reviewed `ledger-tool` image built from the same source
revision/schema as the gateway. Its Debian runtime and CGO build match the
gateway; do not export the binary into an unqualified Alpine or older-glibc host.
It runs as UID/GID 65532 and has no gateway credentials, network client, server,
automatic schedule, service-start or admission-enable capability. Its explicit
NEW-only initialize command creates a ledger and independent identity; normal
gateway startup and Compose never invoke it. The gateway
overlay deliberately does not mount backups or start this tool.

For an independently authorized local backup/restore, run it with network disabled,
a read-only root filesystem, all capabilities dropped, no-new-privileges, bounded
CPU/memory/PIDs and no restart policy. Mount only the existing source directory
read-only and a separate precreated private destination directory writable. Both
must have trusted ownership matching the container's effective UID, including
user-namespace mapping. Keep the complete source WAL/SHM directory visible; never
copy the main open database alone. Do not mount Docker/Incus sockets, gateway
credentials, the entire host filesystem or a shared job workspace. Do not grant
root/capabilities or relax file modes to resolve an ownership failure.

The explicit new-only initialization, backup and restore subcommands and restore-fencing acknowledgement
are documented in [backup/restore](../docs/backup-restore.md). Destinations must be
new. Do not automatically retry uncertain results, restore over an active ledger,
resume scheduling, or interpret the acknowledgement as a fence. This image covers
only the admission ledger. The Garm-mutation journal has a separate persistent
directory and independent configured lineage; its own explicit new-only initializer
is not included in this image. Neither a missing journal nor a lineage mismatch
may be repaired by startup/restart. Its archives remain inspection-only pending a
qualified recovery workflow. Do not use
`capacity-ledger` on an unrelated database.

See the [disposable qualification plan](qualification.md) before treating any image
or merged private stack as usable. No container build or runtime qualification is
claimed from source-level tests alone.
