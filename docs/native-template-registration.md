# Scoped native template registration

`nativebundle.RegistrationCompiler` compiles a new target's configuration from a
private deployment catalog. It does not create a native domain, a preparation
Plan, a live observation, or capacity/release authority. Existing production
activation guards remain closed.

The deployment supplies an exact pinned export, a private capsule template, and
explicit artifact/storage/network reference names. Those references select the
frozen sections of that template, not arbitrary browser JSON. Artifact roots,
ZFS dataset GUIDs, finite category quotas, runtime-image pins and egress rules
are explicit inputs; OS discovery cannot infer any of them. The compiler checks
these sections' shape and uses the existing builder's role/path rebinding. The
real pinned owner checker remains mandatory before a prepared-host receipt;
compilation does not claim complete executable, ABI, network or host validation.

An administrator alias grants only explicit canonical CIDRs, users, ports, and
one credential fingerprint. A request may specify a NEW IP and Ed25519 SSH host
public key within that scope. This is not a prelisted-server selector. The backend
supplies the currently registered inventory target, while the request explicitly
binds the distinct native administrator endpoint. No equality of IPs is treated
as proof of physical identity. An owner TLS identity slot is deployment-issued,
limited to a template/administrator alias and optional host-label prefix. The
compiler never issues credentials or claims its TLS SAN covers an SSH endpoint.

The compiler freezes the actual private template, export checksum list, TLS
certificate/key bytes and administrator scope into one catalog revision. It
rejects shared owner public keys across slots. Public metadata has both owner
SPKI fingerprints, exact Binding, profile digests, registration digest and fixed
local paths; it has no private keys or fabricated readiness. Raw internal paths
must not be exposed by browser DTOs.

## Required registration transaction

1. Validate an explicit user-reviewed request against the current catalog revision
   and the actual current inventory target. `Compile` has no filesystem effects.
2. Under a cross-process durable registration journal, permanently reserve the
   preparation ID, identity alias **and both actual SPKI fingerprints** before
   materialization. Reserve fingerprints across catalog changes and alias renames.
   A failed/uncertain write must latch the registration, never permit reuse.
3. Call `Materialize` once. It creates a fresh private directory and fsynced 0600
   inputs, including the exact expected Binding. Existing paths are never adopted
   or repaired. Any failure leaves the registration consumed and output retained.
4. Only record completed configuration after `InspectMaterialized` succeeds. That
   method can reverify a completed registration after restart, but does not replace
   the durable completion record or authorize retry of an unknown registration.
5. Register a pending planning/assembly target, NOT a fake baseline Bundle/Service.
   Actual discovery, selected resources, bundle build, explicit first-plan review
   and registration must precede the ordinary separate SSH preparation approval.

The in-process one-use bit is copy-safe but is not a persistent identity journal.
The compiler deliberately relies on the registration service for step 2. Root
mutation or rollback of the private catalog/journal is not a supported recovery
mechanism. No upload, SSH, executable execution, unit installation or activation
occurs here. Tests use disposable files, synthetic non-executed ELF headers and
local generated identities only.

## Explicit pending-target integration

`nativeplanning.NewDynamicService` and `nativeassembly.NewDynamicService` are
separate opt-in constructors. They permit an empty initial map, without changing
the ordinary static constructors. After durable completed registration, install
only exact immutable records with `AddBinding` and `AddRecipe`; changes and more
than eight entries are refused. Planning validates the currently saved inventory
target before adding a binding. Assembly requires that exact planning binding.
All map access is synchronized, with assembly acquiring planning only in that
order. Repeating an identical registration is configuration idempotency, not a
second build or host operation. These maps are not the durable registry: startup
must replay only verified completed registration records. Partial installation
may be completed with the identical record; it must never fabricate a baseline
preparation service before a real bundle exists.
