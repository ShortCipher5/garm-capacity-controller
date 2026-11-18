# Native birth budgets (dormant)

The constructor requires four finite aggregate budgets: Bootstrap, Daemon,
Monitors and Guests. Their sum must fit the actual manager ancestor. It creates
root-owned ceilings before the first process, then creates nspawn directly in
`bootstrap` with `clone3(CLONE_INTO_CGROUP)`. No sampled PID is migrated.

The fixed launcher disables nspawn's extra cgroup namespace and binds the already
private, finite manager hierarchy, not the host hierarchy. ObserveManaged checks
the exact inherited namespace and root inode from the actual manager observation,
its private cgroup2 mount, and host-root ownership of ancestor control files.
Other namespaces retain their independent mapped-user ownership checks. Only the
new capsule's explicitly selected child directories/migration files are delegated;
aggregate maximum files and the manager root stay host-root-owned.

Pinned PID1 receives a version 3 canonical birth message, verifies its fixed
bootstrap/payload membership and daemon limits, and writes **0** to the fixed
init.scope cgroup.procs file. This means the calling process, not a sampled PID.
The parent accepts the transition only when the same namespace-bound PID1 executes
the pinned native ELF at that exact cgroup. Pre-move memory remains permanently
covered by Bootstrap; no historical charge migration or bootstrap release is
assumed. The nspawn monitor remains there. Descriptor closure is terminal and
idempotent across copied handles.

Stock LXC's fixed relative=0 configuration can then prune init.scope to compute
the capsule base and place monitors/guests under their fixed siblings. Compiler
and actual guest-path integration are still required; this source component does
not by itself authorize creation, retirement or release. The production gate
remains closed. No live kernel construction or nspawn mount qualification has
been performed by these tests. The trusted-native threat model does not claim to
stop a malicious privileged native daemon migrating itself into a delegated guest
subtree; the finite ancestor still contains its aggregate resource consumption.

The earlier freeze-and-migrate attempt was reverted: frozen tasks can still die,
pidfd polling does not make numeric cgroup.procs writes identity-atomic, and
process migration does not transfer historical memory charges.
