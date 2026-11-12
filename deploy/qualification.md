# Disposable image/runtime qualification plan

Status: **plan, not executed container qualification**. A dedicated
GitHub-hosted [disposable workflow](../.github/workflows/packaging.yml) now runs
`deploy/runtime/test.sh` on pull requests. Its published JSON contains public
image references and booleans only, never ephemeral credentials or fixtures.
A green job means its synthetic checks passed, not installed-stack qualification.
The exact public Garm image probe records provider execution/libc and default UID
blockers without changing that runtime; `installed_stack_qualified` stays false.
 Source tests and native
CGO builds do not qualify an image, Docker engine, private Compose stack or host
filesystem. Never use the live runner stack, credentials, provider volume, ledger,
job workspace, host network or Docker/Incus socket as test inputs.

## 1. Isolated build host and immutable artifacts

Use a separate disposable Linux Docker/BuildKit host with the supported Compose
plugin, a reviewed architecture and bounded build resources. Do not add daemon
sockets, nested-container privileges or Incus nesting to a job merely to run this
plan. If the available CI worker cannot supply isolated builds, keep this gate
blocked rather than weakening its security boundary.

1. Record the source commit, OS/architecture, Docker/BuildKit/Compose versions and
   reviewed digest locks for **every** Dockerfile base. Resolve and pin upstream
   images before the build; tags in the generic Dockerfile are not deployment locks.
2. Run all repository Go race/vet/format checks and web unit/build checks from the
   exact source revision. Retain failure status, not just successful logs.
3. Build the `gateway`, `ledger-tool` and `provider-artifact` targets from the
   allowlisted context. Run `go mod verify` and lockfile-driven npm installation
   as defined by the Dockerfile. Record resulting image/artifact digests.
4. Inspect final image configuration, history and layers locally. Assert that no
   private config, token, key, test ledger, Git history, node_modules or deployment
   fixture is included. Check that the gateway contains its built web assets but
   not the recovery executable; the recovery target contains the CLI, not gateway
   code/assets or credentials. The base shell is not a security boundary.
5. In **disposable copies** of the exact intended Garm runtime, inspect and execute
   provider interface discovery only. Verify ELF architecture, dynamic loader and
   libc requirements. A successful Debian-host build is not evidence that the
   artifact runs on Alpine/musl or older glibc. Do not install into an active Garm
   service, replace the working provider or enable its admission hook.

## 2. Network-disabled recovery-tool smoke tests

Use the reviewed `ledger-tool` image with all of these runtime restrictions:
`--network none --read-only --cap-drop ALL --security-opt no-new-privileges
--user 65532:65532 --pids-limit 128 --cpus 1 --memory 512m`, no restart policy and
no mounts except those expressly listed below. Do not run the gateway entrypoint
or apply its live-stack overlay for these checks.

- The default command and `--help` exit successfully without mounts or network.
- Invalid usage exits 2. Restore without `--old-writers-fenced` exits 2 **before**
  touching any path, even when supplied paths are inaccessible. No path/secret is
  echoed in errors. Verify behavior in the container, not just host unit tests.
- Explicitly initialize NEW synthetic admission-ledger fixtures and a separate
  independently protected identity file with the actual packaged CLI/SQLite
  implementation in a private local test directory. Include charged resources,
  cancellation/dispatch evidence and a committed transaction retained in WAL.
  Use no copied production ledger or real credential. Normal gateway startup must
  reject missing, mismatched and archive/restored state without creating sidecars
  or running initialization. Never derive the expected identity from a mounted DB. A fixture is not a qualified
  physical-host inventory. Build any fixture helper only in the disposable test
  workspace, not in the production image.
- Mount the **whole source directory** read-only at `/source` and a different
  precreated private directory writable at `/destination`. Run `backup` with a
  new destination. Verify source bytes/accounting remain unchanged, committed WAL
  contents are included, output is a private standalone database, and readiness
  is blocked. Confirm read-only-source behavior with existing WAL/SHM under the
  exact engine/filesystem. Refusal is a failed qualification, not permission to
  make the source writable or copy only its main file.
- Restore the synthetic snapshot to another new path only after simulating and
  documenting stopped/fenced test writers. Verify readiness/fences remain cleared,
  charges and supported schema evidence survive, and the source is untouched.
  Both supported backups and restores are permanently runtime-disabled; verify
  even matching independent identity cannot start the gateway. No recovery
  activation is shipped or implied by completing offline reconciliation.
- Exercise wrong owner/mode, symlink paths, missing/existing destinations, sidecar
  collisions, read-only destination, low disk space and concurrent exporters.
  For interruption/kill tests, inspect outcomes offline and retain uncertain
  destinations; never retry over them. Exit 3 may mean publication already occurred.
- Remove and recreate the tool container. Verify only the intentionally mounted
  output persists and no automatic service start, inventory call or admission
  enablement follows. Protect and clean synthetic artifacts without using
  `compose down -v` against an existing stack.

## 3. Full gateway disposable topology

Create a completely separate Compose project containing a disposable namespace
anchor, gateway, authenticated fake Garm/Incus HTTPS APIs and test clients. The
anchor exists only in this explicitly synthetic harness; never create a dummy
Garm service to make a real private stack validate. The harness must use actual
gateway binaries, TLS loaders, SQLite store and web assets. Fake APIs cannot
qualify the real Garm/provider protocol; keep that qualification separate.

- Precreate separate private credential, source-state and backup directories with
  effective UID ownership and 0700/0600 modes. Generate ephemeral test-only TLS
  identities and role-separated tokens. Publish neither fixtures nor rendered
  private Compose output. Do not pass secret values in argv, ENV or build context.
- Validate the merged harness offline with the supported Compose plugin. Confirm
  the profile is opt-in, no published/host-network provider/admin ports, no socket,
  no privileged/PID sharing, no broad mounts, read-only root, dropped capabilities,
  no-new-privileges, separate non-root identities, bounded resources/logs and no
  automatic restart. Test that gateway directories are unreadable by the anchor
  identity and vice versa. Wrong ownership/modes must fail, not be repaired by root.
- Start without either experimental opt-in flag and without a management token
  file. Observe local reconciliation writes, but no create/management mutation on
  fake APIs or the ledger. Inspect every interface: only explicit loopback binds
  may serve the provider/admin listeners. Ordinary bridge peers cannot reach them.
- From the shared namespace, exercise actual mTLS RPC: reject no certificate,
  wrong leaf, untrusted CA and wrong role; accept only the configured identity.
  Read credentials must not authorize management or provider requests. An admin
  200 or running container is not an admission-readiness assertion.
- Load the production UI against the real disposable admin handler. Verify read
  authentication, refresh/disconnect/interruption and no token persistence or
  management capability in this preview. No public proxy or bearer-token URL.
- Recreate gateway and then namespace anchor plus gateway together. Verify
  namespace reattachment, durable SQLite WAL/FULL state, conservative unknown
  accounting, no stale-intent replay and no automatic enablement after restart.
  Stop/recreate ordering is part of the test; it is not a live maintenance command.
- Fail/stale the fake inventory and inject interrupted requests. Readiness must
  close conservatively. Never infer deletion or release from timeout or omission.

## 4. Acceptance record and separate live decision

Record every gate as passed, failed or not run, with exact commit, digests,
platform and bounded logs that contain no credentials. Include rootless/user
mapping, storage durability/power-loss limits, resource-limit failure behavior,
backup schema/rollback and namespace recreation results. Synthetic container
success still does not qualify actual physical coverage, bypass fencing, job-safe
drain/retirement, upstream credential roles or real operator recovery.

The separately implemented Garm-mutation journal needs its own durable storage,
independently configured expected lineage and qualified backup/restore tests;
restoring it must remain write-blocked pending an unimplemented authoritative
activation/fencing workflow. Never initialize/adopt it on normal startup or
derive expected lineage from the mounted journal. The admission-ledger tool must not be applied to that database.

Only after the disposable gates pass can an operator review the **exact** target
stack and separately authorize any deployment. Do not change its UID/capabilities,
credentials, networking, provider or scheduling under this plan. If the live stack
does not meet the documented isolation model, report the blocker and leave it
unchanged. This plan contains no live deployment command or permission to deploy.

## Automated coverage and remaining gates

The workflow digest-locks all production bases for its run, builds the actual
gateway/recovery/provider targets, checks native image identity/capability/rootfs
behavior, explicit new-only ledger/independent-identity initialization, missing/
wrong-mount/archive nonactivation, wrong owner/mode rejection, synthetic Compose namespace/mount/role
isolation, actual gateway mTLS and loopback listeners, static web serving, live
WAL-aware CLI backup and blocked restore, and namespace recreation. The separate
public Garm digest is executed only for UID/interface discovery, with no network
or configuration. Its incompatibility is reported explicitly and never repaired
by modifying the image or installing a loader.

This does not yet automate low-disk/interrupted/concurrent container export,
full browser interaction, image-layer secret canary scanning, power-loss durability,
all-negative ownership/symlink cases, legacy charged-ledger upgrades or any actual
physical-host/job-safe lifecycle. Existing Go/browser tests cover their documented
local contracts separately. Do not convert a missing automated gate into a pass.
The shell harness requires a disposable runner and removes only its generated
Compose project/test directory; do not point it at an existing host or stack.
