# Unwired protected stock-read-only preboot observation

The private `observeStockReadOnly` implementation is a bounded diagnostic
snapshot, not a permission, durable namespace seal or authenticated session.
There is no CLI or production caller, successful runtime test hook, conversion to
managed evidence, namespace opener, login/bootstrap, writer or restart operation.
Existing managed verification is unchanged.

It reads only `/etc/capacity-stock-readonly/reviewed-profile.json`, with protected
root-owned ancestors and a private regular root-owned file. The fixed schema
`stock-readonly-profile/v1` requires the verified stock reference, linux/amd64
platform, all three distinct container IDs, distinct exact image IDs and all
three executable hashes. Stock image/hash are fixed to #239 measurements.
Gateway/ingress values must come from independently reviewed **actual v2** hosted
artifacts. No synthetic test value is a usable deployment pin.

The directory field binds only a protected local layout with the fixed #248
fixture/private, fixture/state, joined/gateway-private, joined/state and
joined/ingress-private subdirectories. Their parents must be root-controlled;
role directories must be owned by the exact nonroot role and mode0700. The
ordinary runner-owned hosted temporary layout is not thereby admitted. No
profile is installed by this change.

Actual observation uses the existing private root-socket/peer-authenticated
inspect-only Docker reader, a five-second overall bound and bounded kernel/file
reads. Checks include fixed umask shell launch and actual executable argv, role
images/UIDs/capabilities/cwd/mounts, executable hashes/inodes, shared isolated net
namespace and separate non-host PID namespaces, no published ports, no other
running Docker namespace member, exact four IPv4 loopback listeners, repeated
process lifetimes/namespaces/container descriptions and reviewed-profile equality.
Membership is checked again. Failure returns no observation.

These checks are sequential, not atomic. A trusted privileged host actor can
change namespace membership, listeners, files or processes between samples or
after return. Host processes entering a namespace are not an exhaustive Docker
membership inventory. The result therefore has no accessor or consumer that can
open a namespace, authenticate, grant authority or promise continuing isolation.
Before a future consumer, that consumer's temporal trust boundary must be reviewed
explicitly, along with actual root-owned mount layout and hosted runtime results.

Tests inject only descriptions into pure checks and exercise canceled/nil calls
before I/O. No test launches Docker or successfully invokes protected observation.
Root-owned protected measurement, actual v2 artifact review, authentication and
Menkent qualification remain separate prerequisites.

## Held listener comparison primitive

The private listener-set comparison now distinguishes four-port preboot from a
five-port description only when a retained accepting IPv4 TCP socket has exact
address `127.0.0.1:9445`, no reuse-port setting, and an inode matching the table's
single extra listener. Its actual descriptor identity is checked before and after
comparison; closing or replacing it invalidates the comparison. Tests create only
local disposable sockets and synthetic tables, not a protected runtime.

The actual observer still calls only the four-port branch. No supervised observer,
namespace-opening capability, server, authentication or session is added. A later
sealed lifecycle must establish the provenance of the namespace table and socket,
retain and revoke the listener, and prevent descriptor handoff. This comparison
cannot establish sole custody, atomicity, or immunity to privileged duplication or
changes between/after samples. It is not an admission or connection permit.

## Retained preboot identity

A separate private constructor now brackets retention of the actual observed
stock network nsfs descriptor with two complete protected preboot observations,
within one five-second context budget. It compares exact role PIDs, process start
times, PID/network namespace identities and protected profile, plus nsfs inode.
The descriptor is neither exported nor usable through an accessor. Recheck repeats
the actual preboot observer; any failure closes and clears the retained object.
Explicit close is idempotent. Tests use canceled contexts, ordinary disposable
files and pure lifetime comparisons, not successful injected runtime admissions.

This preserves a namespace reference, not a live process guarantee. It does not
freeze processes or make sequential observations atomic. The holder alone remains preboot-only: adding a fifth listener causes its
preboot recheck to fail and revoke. Only the separate private lifecycle below
owns the supervised transition; the holder exposes no namespace capability.

## Private supervised lifecycle candidate

The private lifecycle now consumes the retained holder, rechecks exact preboot,
then creates only `127.0.0.1:9445` on a dedicated locked OS thread in that retained
network namespace. It verifies entry and restoration against actual namespace
inodes; restoration failure closes the socket and exits the still-locked thread,
so that thread is never returned to the Go pool. The constructor joins the worker
before releasing the namespace holder. It then runs the full observer with the
actual held accepting socket identity and exact five-port table, retaining the
same role lifetime/profile identity. Drift, cancellation or failure revokes both
socket and binding. No Accept/Serve, credentials, session, exported handle or
production caller exists.

Local tests cover only refusal, close/revocation and race-safe lifetime cleanup;
no namespace transition or lifecycle bind has been executed. Cross-namespace
creation/restoration and cancellation still require separately approved disposable
qualification. The context deadlines bound context-aware operations, not mutex
acquisition, synchronous filesystem/hash operations or kernel calls. Cancellation
cleanup can therefore wait for an in-progress recheck; it is not a hard real-time
revocation guarantee. Sampled checks still cannot exclude privileged interference
or prove exclusive socket custody.
