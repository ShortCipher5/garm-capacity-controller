# Concrete capsule owner service composition (activation refused)

`capacity-native-domain --capsule-config=/absolute/root-private.json` deliberately
returns the confinement refusal **before configuration reads, manager requests,
device installation, process launch, storage or network changes**. It cannot be
combined with the legacy domain/control/boundary flags. There is no override.
The legacy v3 loader remains unchanged; capsule configuration accepts only v5.

The private implementation composes the actual components in this order:

1. Verify the real independent system manager identity/private cgroup mode;
   validate configuration, root-owned TLS material and pinned quota reader.
2. Observe actual finite cgroup limits and install the concrete device boundary.
3. Start the pinned staged capsule, obtain its actual peer and permanent birth.
4. Create fresh ZFS storage and explicit egress using that same peer.
5. Finish the staged image once, checking its native/guest quota helper hashes.
6. Derive domain ID, per-pool dataset and egress digest from those live births.
   The input may not supply these identities. Compile the protected v5 policy.
7. Build the private capsule native client, provision its fresh resources, then
   construct the pinned claim client and authenticated execution server.
8. Only if the unchanged execution boundary gate passes, open the listener.
   Periodically recheck the actual capsule scope; context cancellation or scope
   failure shuts down the listener and joins its serve loop.

The execution-server boundary gate currently refuses capsule authority. This
composition does **not** remove that gate, enable gateway provisioning, issue a
capacity permit, register a host, or qualify real root/Incus/ZFS operation.

Configuration has `control`, `limits`, `launch`, `storage`, `network`,
`network_tools`, `quota_reader`, `runtime_quota_helper_sha256`,
`device_registry`, and `device_pin_root`. Nested component configuration fields
are the components' existing exported field names. `control.policy` must use
`garm-native-policy/v5`, blank `domain_id`/`egress_digest`, and blank pool
`zfs_dataset`; actual values are never accepted from a receipt or operator flag.
The policy launcher hash must equal the staged image helper hash. The host and
image quota helper must be the same pinned artifact; the host quota-reader ZFS
hash must equal the storage constructor's ZFS hash. Native policy remains bound
to the actual image/native executable checks performed by the capsule.

Failures after any birth preserve permanent journals and uncertain resources.
Closing descriptors/listeners is not process termination, storage deletion,
namespace reclamation, allocation release or automatic retry. Existing domains
are never adopted. Positive root-host qualification is intentionally not mocked
by the command tests; tests cover input rejection and the pre-effect guard.

## Mandatory resource configuration

`partition` supplies finite bootstrap, daemon, monitor and guest CPU, memory and task
budgets. The shared kernel-side validator checks their total against the parent
capsule limits before birth. Construction uses the one-use birth-time
`StartWithPartition` path, never a later numeric-PID migration.

`storage_budgets` supplies a positive runtime budget plus every fixed stock
category, including deleted categories. The shared ZFS validator rejects missing,
unknown, unlimited and overflowing totals. Actual sealing runs after native
resource bootstrap and before the claim client or execution listener. The runtime
budget covers metadata, logs and temporary files together; it is not a separate
log-file quota.

`control.pool_reader` supplies a separately pinned root-owned zpool executable
and helper. It is mandatory in both capsule and legacy owner constructors; there
is no nil/optional accounting path. These inputs create no physical absence or
release authority. The command and owner execution boundary remain disabled until
the remaining namespace, containment and lifecycle proofs are implemented and
qualified.

Bootstrap charges remain permanently bounded after stage self-placement; moving the task does not move historical memory charges. Policy v5 emits only fixed LXC sibling paths. This construction remains guarded and is not live-kernel qualification.

Policy v5 fixes `raw.lxc` to relative=0, container `guests/<name>`, inner
`payload`, monitor `monitors/<name>`, and monitor pivot `monitors`. Names
must pass the existing separator/newline-free instance-name validator. The five
keys are registered in pinned LXC v6.0.4 `src/lxc/confile.c` and are not among
Incus v6.0.4 `internal/server/instance/instance_utils.go` raw-config exclusions.
This source compatibility is not proof of live namespace placement or security
qualification of the pinned runtime artifact. Existing v2/v3/v4 compilation does
not acquire these paths; capsule construction now requires v5.

## Required shared host artifact root

`artifacts` supplies a finite host-only ZFS quota, pinned ZFS executable/helper,
parent dataset/GUID, and the original reviewed image path/hash. Its tool pins must
match `storage`; its image hash must match `launch.ImageSHA256`. The external
image is an input copy, not claimed to be bounded by the new quota.

External `launch.Image`, `launch.Registry`, `launch.StateParent`,
`storage.Registry`, `network_tools.Registry` and `device_registry` must be blank.
The service calls the actual fresh `CreateHostArtifactRoot`, checks its opaque
`Paths`, and fills all six exclusively from that result. `StartWithArtifacts`
retains the same capability and independently derives its image/ID/state paths.
There is no arbitrary registry fallback, old-root adoption, automatic retry or
cleanup after partial setup. The fixed permanent host setup latch must already
exist with the required root ownership/mode. TLS/config originals, executables,
bpffs objects and the latch remain explicit host system reserve.

After potentially lengthy image copying, the service refreshes the exact same
manager namespace/root observation before device or capsule birth. It keeps the
artifact root alive through the TLS service lifetime; descriptor closure never
unmounts, destroys or releases its quota.
