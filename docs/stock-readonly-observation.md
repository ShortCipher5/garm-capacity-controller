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
