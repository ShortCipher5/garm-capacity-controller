# Fresh native capsule construction (dormant)

The construction implementation is not wired into the active owner command.
That command's hard refusal remains mandatory. The following code is actual
construction, not a successful host qualification or production release.

1. `nativecapsule.Start` pins the vendor nspawn binary and dedicated launcher,
   probes systemd 257 through the descriptor-sanitizing helper, pins a supplied
   raw image and reserves a never-reused UID/GID range. It creates an exclusive
   bootstrap socket and child cgroup and uses clone3 cgroup placement from birth.
2. The fixed image stage connects while PID1 remains paused. `capsulepeer`
   obtains real Unix credentials, pidfd, six held namespace FDs and namespace
   ownership; checks mapped UID/GID, initial process, private cgroup and executable.
3. `nativezfs.CreateFresh` creates the host-owned quota parent and disposable
   runtime/pool children. It initializes only the fresh runtime's ownership,
   unmounts it, then delegates through the held actual user namespace FD. No
   existing dataset is adopted. A consumed attempt cannot be retried or released.
4. `Staged.Finish` accepts that opaque birth, rechecks actual quota/GUID and paused
   identity, and fsyncs an exclusive release record before sending a single boot
   message. Lost acknowledgments or failed execution leave the attempt unknown.
5. The stage verifies actual delegated identities/quota, mounts the fixed runtime
   path and execs only the pinned Incus daemon. Its acknowledgment means only
   `native_exec_pending`, not successful readiness.
6. `Running` is returned only after the same held PID1 accepts its private Unix
   socket as the mapped UID/GID with the expected native executable and unchanged
   namespace/map/cgroup identity. Requests recheck live host quota and parent
   limits. Client claims share a one-use latch across copied values.

## Remaining construction and integration

- Produce and qualify the pinned raw runtime image containing the stage, matching
  ZFS binary/quota helper, Incus and its actual runtime dependencies. The current
  constructor accepts a supplied image; it does not build one.
- Implement the external network uplink and qualified device policy. Current
  nspawn configuration is private-network, with no host uplink setup. Root-side
  fresh Incus bridge/pool provisioning must consume the real Running handle.
- Wire the separately managed owner service to the fresh capsule instead of the
  old unconfined native-domain constructor. No automatic adoption after owner
  death, raw journal rollback, or ambiguous native delivery is permitted.
- Qualify exact nspawn/Incus/ZFS/kernel interaction, descendant placement and
  guest containment. Socket, mount or quota readbacks alone do not prove these.
- Safe acceptance fencing, physical deletion and release remain separate work.

## Threat model and lifetime

Trusted host root can replace binaries/images, remap accounts, modify cgroups,
change ZFS delegation or roll back journals. This implementation does not defend
against that actor. In particular nspawn reopens the trusted image pathname after
its initial hash check; concurrent host-root replacement is excluded, not proven
impossible by holding an inode. The range/dataset journals are durable first-use
records, not independent anti-rollback hardware.

Closing observation descriptors does not signal processes or delete storage.
A process death, partial write, missing socket, failed readback or uncertainty
never grants new capacity. Stopping the independent owner/capsule service can
impact running jobs and is not an automatic controller recovery procedure.

Parser, protocol refusal and source tests do not exercise privileged namespaces,
ZFS mounts, device ioctl isolation, or real Incus startup in this environment.
