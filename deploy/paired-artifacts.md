# Experimental paired artifacts, not a release or deployment

Status: locally tested code candidate; Docker/container execution is unperformed
until the exact hosted `paired-garm-artifacts` job passes. No installed files,
credential, API endpoint, instance, real job or private network is an input.

## Versioning and supply-chain boundary

A provider's `GetVersion` discovery string is a capability label, NOT provenance.
It intentionally remains experimental. Select a pair by the content-addressed
public manifest, independently pinned to a trusted workflow/source, never by that
string, a moving image tag or the presence of a compatible ELF loader.

The job records one clean controller commit/tree and fetches only official Garm
`154638445c3949c1958b01812f69d9a1e4d82684`, applying reviewed patch SHA256
`87e0d61751d4b9ffa15322cff09557f0a1094fdd68464fe840a12ffe01bb0660`.
Unknown patch, FROM syntax/base, architecture or linkage fails closed. Node22.23.4,
Go1.26.10-bookworm and Debian-bookworm-slim bases are resolved to repository/digest
locks before building, with a strict per-Dockerfile stage parser. The explicit
Dockerfile BuildKit frontend is separately resolved/pinned by digest for BOTH
Dockerfiles and included in the content identity. The GitHub runner, Docker
daemon/BuildKit backend and build-network registries remain trusted, not fully
hermetic or digest-locked host infrastructure. These resolved
locks are evidence for that run, not a pre-reviewed immutable production lock.

A tracked-only `git archive HEAD` context plus the existing Docker allowlist
exclude untracked files from provenance and build inputs. It builds actual CGO gateway,
provider and separate ledger tool plus tested production React assets. A second
NEW context contains only fetched public patched Garm source/vendor, public probe
source and that same exported provider binary. All builds and public manifest
construction finish BEFORE generating any fixture key, config, token or database.
No credential is a build argument, context, ENV, layer or registry login.

The reduced `paired-garm` image contains only patched Garm and the capacity
provider on Debian. It does not inherit stock direct Incus or other provider
executables. It defaults to non-root65531 and config-free version output, not an
auto-initializing installer. Garm's linker version records the patch prefix;
manifest source/patch/binary hashes, not that human label, establish the exact
candidate. The qualification probe is exported separately and mounted read-only
only during testing. It is NOT included in the paired image or its entrypoint.
Unmodified Garm still auto-creates/migrates supplied state: this candidate has
NO production existing-only launcher or restore authority. The earlier migration
lab guard is a test fixture, not silently installed here. Its system CA bundle,
Garm CLI/UI helpers and other installed runtime assets also need a reviewed
release profile before real external service use.
This reduced candidate is not an installed-stack replacement or assurance legacy
providers/jobs/assets migrate. No image is pushed or retained after runner cleanup.

Manifest `experimental-garm-pair/v1` binds the controller/Garm source, full patch,
module/lock/vendor hashes, digest-locked Dockerfiles/bases, actual Garm/provider/
gateway/ledger/probe hashes, platform, observed compiler/libc and all three image
IDs. The provider hash in its final Garm image must equal the actual exported
controller provider. Every binary's dynamic dependencies must load in its intended
runtime. `experimental-pair-<content hash>` covers inputs AND all image IDs;
changing any binary or image without changing that identity fails. Validators
reject unknown/private fields, source/patch/platform drift and production claims.

This is unsigned build evidence, **not SLSA, a signed release, a vulnerability
scan, byte-for-byte reproducibility across builders or independent attestation**.
An attacker replacing an entire manifest can recompute its ID. Before production,
independently pin the full manifest digest, review the final immutable artifacts,
resolve a release/signing/registry trust process and qualify the exact target.
The GitHub workflow uses read-only repository permission, pinned actions and no
persisted checkout credentials. No public artifact contains fixture material.

## Bounded disposable runtime proof

- Actual patched Garm runs as65531 with all capability sets zero, NNP, immutable
  root, private PID and `networknone`; actual gateway uses65532. Synthetic upstream
  API/control fixture uses a third UID65530. Only their network namespace is shared.
  No host socket, privilege, host PID, published port or external route is added.
- Explicit NEW-only setup creates Garm main/blob state and controller identity,
  no account/repository/pool/instance. A separate NEW admission ledger and external
  identity are initialized with the packaged ledger tool. No automatic init is
  added to runtime entrypoints, and no mutation journal is initialized or mounted.
- Garm config contains exactly one actual external capacity-provider stanza:
  v0.1.1, admission_enabled and explicit gateway-owned-default-cloudinit/v1,
  empty forwarded environment list. Its controller ID and gateway config match
  the generated Garm controller. No credentials enter ENV/arguments.
- Garm gets only its own TLS/state and ordinary+source provider leaves. Gateway
  Incus/upstream-client/server keys and ledger identity remain inaccessible to it.
  The fixture retains neither Garm provider/source keys nor gateway private keys.
  Key disjointness and positive-owner/cross-UID-negative reads are asserted.
- Actual Garm TLS/API startup and PID1 identity are checked. A qualification-only
  executable compiled from this exact patched source loads its real configured
  provider facade and invokes ListInstances. That facade executes the actual CGO
  provider packaged in the Garm image, reaching the actual gateway over strict
  loopback mTLS and exact ordinary leaf authorization. This is not a Python RPC
  stand-in. Positive list is repeated around source-role/wrong-CA negatives.
- The distinct source leaf must complete real mTLS but receive403 for ordinary
  list. An otherwise identical wrapper config with an unrelated server CA must
  fail. These do not qualify a real source issuer or issuance workflow.
- Gateway provisioning/management remain OFF. The upstream inventory/Incus server
  is synthetic and rejects every POST; there is no real GitHub/Incus API. Actual
  admin assets and loopback listeners are checked. No pool/JIT/job is allocated.

The facade probe is invoked by the harness in the Garm container, NOT by a real
Garm demand/poll worker. Gateway inventory is a synthetic read fixture, not the
uninitialized Garm daemon API. The existing pinned compiled-source/preparation
wire gate separately tests the control flow. Neither result alone or combined is
real-issuer, live restart, physical enforcement, source-proof trust, availability,
job-safe drain or recovery activation qualification.

## Public result and remaining gates

Result directory creation is exclusive: any preexisting directory/file/symlink
fails before builds, without deleting/adopting it. Final JSON uses exclusive file
creation. Summary/result upload runs only on success, never after an early failure
that could leave stale/preexisting data. Only two explicit JSON files are uploaded
after every runtime assertion passes:
public build manifest and result booleans tied to its digest. No raw config, key,
JWT, token, DB/WAL, controller identity, request body or container log is uploaded.
Failure diagnostics are status/exit/OOM only. Every result keeps signed_release,
source_issuer_qualified, jit_or_job_lifecycle_qualified, installed_stack_qualified
and deployment_authorized false. Runner-local image tags are not publication.

Remaining gates include a deployable image and release trust process, exact target
mapping/mount/permissions and role migration, complete physical/alternate-writer
scope, effective old-writer fencing, real issuer/lease authority, job-safe lifecycle,
maintenance downtime and authoritative post-write recovery. Admission and mutation
journals have independent protected lineages; archives remain runtime-disabled.
No live command, registry publication, provider replacement, security-setting
change or service restart is authorized by this job or its manifest.
