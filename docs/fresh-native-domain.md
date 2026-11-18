# Fresh native domain ownership (not production activation)

`internal/nativedomain` adds a concrete Linux-only, root-owned, one-shot Incus
process launcher and private Unix transport. It is not wired into the gateway
and does not remove its production activation refusal.

The launcher requires a new directory beneath non-writable root-owned ancestors.
It refuses symlinks and existing directories, even empty ones. It creates a 0700
outer directory and Incus data directory, fsyncs a random domain birth identity
and native/helper executable hashes with launch consumption **before** starting any process, and
executes an already-open root-owned helper descriptor. That fixed helper applies
child-side `close_range(CLOSE_RANGE_CLOEXEC)` before executing the pinned native
descriptor, so ambient non-CLOEXEC listeners cannot leak. It passes a closed
environment and no socket-activation descriptors. It does not use an existing
Incus service, project, socket, network listener or imported database.

Local connections must have root peer credentials and the precise launched PID;
a kernel pidfd captured at child birth, process-start observation taken before
reaping, and child-exit latch reject dead/reused process identities. The pidfd
is held until child reap; unsupported kernels fail closed. Requests cannot select a different host. The transport disables pooled
connections. Close/cancellation/launch failure never kills a daemon, removes the
directory, retries launch or releases resources. The child uses a separate session to avoid controller process-group signals.
Direct library callers still inherit their caller cgroup. Use of the separate
manager-owned command below is required for controller-restart isolation; a
library handle alone is not evidence of that service boundary.
Existing/uncertain domains
cannot currently be reopened or restarted by this package.

## Threat model and limits

Host root, the kernel, the Incus binary and its root-controlled runtime
libraries/helpers are trusted. This is not a defense against root rewriting the
binary or changing permissions/configuration. Incus's fresh database defaults to
no network listener. No write API is exposed to guests by this package. The
containing broker must retain a closed API and deny network-listener changes,
foreign credentials, privileged guests, control-socket mounts, instance import,
restore/migration, arbitrary profiles and alternate writers. The package does
not yet implement that policy or install storage/images/profiles.

The birth manifest is not a second resource ledger: it contains no allocation,
capacity accounting, native operation UUID, reservation or retirement decision.
No successful launch or Unix connection is a native operation or completion
receipt. In particular it cannot justify freeing a reservation.

Remaining integration must consume native mutations in the authoritative ledger
before dispatch, use permanently unique names/tombstones, send each mutation only
once, persist exact terminal outcomes, and quarantine accepted requests whose
operation UUID or completion was lost. A missing instance/operation or expired
request is never proof of absence. Job-safe retirement remains separate.

## Verification

Rootless disposable tests cover durable creation/permissions, no adoption or
reuse, cancellation before effects, peer-incarnation rejection and rejection of
manufactured zero handles. Disposable subprocess tests verify an inherited
non-CLOEXEC listener disappears at native exec and a reaped birth pidfd cannot
authorize another connection. No test launches Incus or accesses a live host.
Actual Incus launch, kernel confinement, packaged helpers, guest isolation,
host reboot and native lifecycle remain **unqualified** here. This executor has
no Incus binary and no mounted `/sys/fs/cgroup`. A subprocess fixture cannot prove
those properties. This code must not be described as a complete native authority
or used to switch the stock-provider runtime on.

Sources: [Incus environment](https://linuxcontainers.org/incus/docs/main/environment/),
[security](https://linuxcontainers.org/incus/docs/main/explanation/security/),
and [packaging/socket activation](https://linuxcontainers.org/incus/docs/main/packaging/).

## Separate manager-owned service (still no provisioning)

`cmd/capacity-native-domain` and the static unit in `deploy/native-domain/` keep
this fresh owner outside the controller service. systemd creates the owner's
service cgroup before its executable starts; the native child inherits that
independent cgroup from birth. This arrangement does **not** require moving an
already-running daemon out of the controller or manually creating sibling
cgroups owned by systemd. `Delegate=yes` reserves the service subtree for native
management; there is no claim that this alone constrains all Incus guest cgroups.

Before creating a domain the command queries the actual local systemd manager
and verifies its own MainPID, exact ControlGroup and `/proc/self/cgroup`
membership, unified cgroup filesystem, `ExitType=cgroup`, delegation, no restart,
no controller stop/dependency propagation, no timed/watchdog shutdown, no cleanup
commands, and no forced kill. It does not accept a version string or a unit file
as proof that the running manager implements those semantics. Unsupported or
missing properties fail closed before native launch. Boot/shutdown dependencies
are limited to an explicit small allowlist; custom Requires/Requisite/Conflicts,
reverse conflicts, uphold/enable/trigger edges and failure/success hooks are
rejected. Hosts with extra mount dependencies currently fail closed rather than
silently broadening this supported contract.

`ExitType=cgroup` lets systemd retain the service while its cgroup remains
populated even when the owner process exits. This uses the manager's cgroup
tracking, not a persisted PID or our own empty-list observation. The service has
no Install section, auto-restart, socket activation or controller dependencies.
Controller restart therefore has no prescribed stop/restart effect on it.

**Stopping/restarting this domain service, host shutdown, OOM or root actions
can affect running jobs.** Explicit service stop sends SIGTERM to its cgroup;
there is no forced SIGKILL timeout. This is not job-safe retirement. Never stop
this service to rebalance runners. An owner failure can leave the daemon alive
but unreachable by this code: existing directories are refused, identities are
not re-adopted, native operations are not replayed and no capacity is released.
Durable authenticated reconnect and native mutation/terminal accounting remain
unimplemented. Do not erase the birth directory to bypass that refusal.

No systemd unit was installed/started and no live host was changed. Unit-source contract checks and parser/subprocess tests do not qualify Incus helpers, native
cgroup behaviour, service crash/reboot recovery or production deployment.
`systemd-analyze verify` could not run in this executor because its attempt to
create `/run/systemd` was rejected by the read-only filesystem. No manager-level
verification or service crash/restart trial is claimed.

Systemd sources: [service ExitType](https://freedesktop.org/software/systemd/man/latest/systemd.service.html),
[manager cgroup interface](https://systemd.io/CONTROL_GROUP_INTERFACE/), and
[delegation ownership](https://github.com/systemd/systemd/blob/main/docs/CGROUP_DELEGATION.md).
