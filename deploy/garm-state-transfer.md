# Disposable patched-Garm state-transfer qualification

**Status: code candidate, not a completed Docker proof or installation change.**
Native generated-state/helper checks pass against pristine pinned source plus the
reviewed source patch. Docker is unavailable in the authoring workspace; the new
GitHub-hosted `patched-garm-state-transfer-lab` must actually pass before any
container result is claimed. No real state, credential, account, job, host or
installed service is an input. The test image is runner-local and never published.

This extends the [stock-image empty-state identity lab](garm-least-privilege.md).
That earlier hosted proof did not qualify a patched artifact or existing root-owned
state. Neither lab qualifies the installed stack, availability or deployment.

## Exact public artifact and isolated fixture

The job fetches only official Garm commit
`154638445c3949c1958b01812f69d9a1e4d82684`, checks and applies the exact public
`integration/garm-v0.2.1/admission-preflight.patch` SHA256
`87e0d61751d4b9ffa15322cff09557f0a1094fdd68464fe840a12ffe01bb0660`.
A patch change fails until explicitly reviewed and updated. The source patch has
trusted admission/bootstrap/JIT semantics absent from stock Garm. This lab does
not configure a provider or exercise those semantics; their compiled-source/wire
gate remains separate.

A generated build context contains this public patched source, its vendored
modules, a test-only storage helper and Dockerfile. It is built **before** fixture
creation. No private file can enter the context/cache/layer. Go1.26.10-bookworm
and Debian-bookworm-slim bases are resolved to digests with strict FROM allowlists.
The image builds actual CGO Garm and the lab helper with vendored modules. There
is no upstream moving-provider fetch, registry login, secret, production web UI
build, or installed-image replacement. This is a different **TEST image**, not
an assertion that a stock/rebuilt/installed image is interchangeable.

The fixture generates owner-only TLS, JWT and database-encryption material, a
nonfunctional PAT-shaped payload and non-admin user, plus an actual file object
through Garm's local store APIs. It creates no admin account, repository, pool,
instance, provider or remote job. The API must still return `init_required` under
strict loopback TLS. Fake provider/source TLS leaves are distinct and owned by
the Garm role; separate generated gateway keys never enter its runtime mounts.
The two source/provider leaves do not isolate a compromised Garm from itself.
There are no real issuer claims or credential-validity checks.

## Why both databases and the passphrase matter

Pinned Garm `config.SQLite.BlobDBFile` derives `blob-<main filename>` beside the
main DB. `database/sql.NewSQLDatabase` opens two separate connections, uses WAL
and migrates both; objects are stored in the second DB. Main-only backup or two
independent online backups while writes continue are not a qualified application
snapshot. A native supplemental proof reproduced silent object loss after a
main-only copy: startup created a new empty blob schema, and SQLite integrity
could not detect the omitted logical object. It also reproduced failure to decrypt
an actual synthetic stored credential after changing the encryption passphrase.

This container candidate copies **only after every generated daemon/helper
writer has terminated**, with no restart policy or external writer. The full
source tree, including each DB/WAL/SHM, is copied to disposable staging. SQLite
backup then creates both new destination DBs from staging. The untouched original
is never opened by SQLite during backup. Even a SQLite `mode=ro` reader can update
SHM bookkeeping; that is why recovery/backup operates on staging, not the original.
The destination is new, with exclusive file creation, checked integrity and no
silent overwrite/adoption. No claim is made for a live-copy or cross-DB online
snapshot. The hosted fixture may cleanly checkpoint its WAL; explicit committed
WAL-only inclusion was established separately by the native subproof, not assumed
from this job's nonempty state or successful startup.

The entire private config/JWT/passphrase/TLS/provider/source material remains
byte-identical. Only a new generated destination gets ownership65531; the preserved
root-owned original is not chmodded/chowned. Numeric ownership portability to an
actual Docker user namespace or filesystem is not inferred.

## Executable hosted slice

`deploy/runtime/garm-migration/test.sh` refuses non-GitHub-hosted execution. It
accepts no configuration, state, source, image or endpoint override. It performs:

1. Build the exact public-source **test** image before generating private fixtures.
2. Explicit NEW-only store seeding under root, then run the actual patched daemon
   on that generated populated state as UID/GID0. Even this lab root baseline has
   all caps dropped, NNP, read-only root and networknone. It is not a reproduction
   of the installed root service's effective permissions.
3. Strict TLS/API and actual PID1/UID/all-capability/NNP/mount checks; clean stop,
   verify exit0 and no running daemon, then a terminating read-only local guard.
4. Preserve original hashes, snapshot the stopped pair through staging, preserve
   private bytes, reown only the NEW destination to UID/GID65531.
5. Paired owner-positive/cross-role-negative reads without outputting any bytes.
6. Run actual Garm under65531 with zero capability sets, NNP, immutable root,
   networknone/no published port/private PID, no socket, fixed private-read-only
   and state-writeable binds, bounded private noexec/nosuid/nodev tmpfs and CPU/
   memory/PID limits. Check actual daemon PID1, strict TLS and state witnesses.
7. Cleanly stop/recreate destination, recheck controller/user/encrypted payload,
   pre-transfer JWT signature, actual blob contents and hashes of private files.
8. Same guard+exec launcher must reject missing main/blob, wrong owner/mode,
   symlink/hardlink, substituted controller/config, wrong passphrase/JWT/CA/TLS
   key and truncated/interrupted copy. Private-material negatives retain the
   original expected manifest: they prove guard/validation rejection before daemon
   exec, not remote credential revocation, TLS protocol refusal or an issuer check.
   The identical positive/negative launcher maps guard failure to distinct
   exit42, while pinned Garm startup errors exit1. A still-running container or
   later daemon failure is failure, never a guard-rejection or timeout success.
   Missing DBs must stay absent. No retry as root, mode widening or empty fallback.
9. Stop new writer and verify original source bytes untouched before a LAB-only
   restart of that original. No external work occurred, so this is not proof of
   safe live downgrade, historical recovery activation or job-preserving rollback.

The helper checks the expected controller and generated payload identity in the
private read-only bind, independently of whatever DB is in the writable bind. It
never seeds during guarded startup. This is a **test fixture guard**, not a shipped
production existing-only launcher, anti-rollback authority or defense against the
trusted Docker/host administrator changing mounts. Its fixed lab roots and private
parent trust are not a qualification of an actual host's ancestors/mapping.

The Python `state.py selftest COMPILED_HELPER` supplements native verification on
fresh generated temporary roots, not container/UID proof. It rewrites its generated
native-only paths/expected hash to accommodate two host paths; the actual hosted
transfer preserves config bytes and fixed container paths without such rewriting.

Public artifact `garm-state-transfer.json` contains public source/patch/base/image/
binary/dependency references, toolchain/libc/platform and boolean results only.
It explicitly records installed_stack_qualified=false, deployment_authorized=false,
provider_integration_qualified=false and availability_qualified=false. No config,
keys, JWT, encrypted/plaintext credentials, DB/WAL, controller IDs or logs upload.
Failure diagnostics are container status/exit/OOM only. Cleanup removes only named
generated lab containers, its runner-local image and new temporary workspace.

## Downtime, fencing and separate live gates

This is a maintenance transfer, **not HA or zero downtime**. Never run old and new
controllers concurrently to preserve availability. Existing runner processes may
continue, but callback/metadata service, webhook processing, JIT replenishment and
reconciliation can be interrupted. No promise is made that remote events replay,
that callbacks tolerate this window, or that an apparently idle runner is drained.
A bounded lab stop does not establish safe live maintenance or recover ambiguous
creates. Admission disable is not an effective old-writer/job fence.

Before a separately authorized live change still require:
- A reviewed patched deployable Garm/provider pair, immutable image provenance and
  its exact runtime/ABI/initialization/helper/asset behavior. This reduced test image
  is not the installed stack or a production replacement recipe.
- Private read-only measurement of effective installed UID/caps/NNP, engine userns
  mapping, all mounts/parents and owner/modes, both existing DBs and referenced
  assets, exact data version and startup migrations. Preserve existing provider
  identities/jobs; do not retrofit fake routes or credentials from this lab.
- Independent role boundaries for Garm ordinary+source provider keys versus gateway
  Incus/admin/inventory authority and the two independent durable storage lineages.
- Effective old-writer/alternate-provider fencing, complete physical scope, job-safe
  maintenance/drain and recovery/rollback authority. No pre-v8 source history can
  be invented from a copied DB; old charged state stays conservative.
- Qualified real-application consistent backup including both Garm DBs, sidecars,
  private material and referenced files. `capacity-ledger` is NOT a Garm DB backup
  tool. Admission/journal archives stay runtime-disabled/inspection-only; copying
  them does not activate a historical scheduler. Post-write schema/data rollback
  remains unqualified, even if the patch adds no new Garm SQL column.
- Reviewed timing, affected services, permissible downtime/data-loss and separate
  explicit owner authorization for the exact live service/security/storage action.

No live command, volume export, real credential use, deployment or setting change
is authorized by this test, a merged PR or a passing hosted run.
