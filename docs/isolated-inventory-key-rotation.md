# Isolated inventory key rotation and scoped revocation

`capacity-inventory-key-rotate` is an explicit Linux CLI operation for the owned
isolated listener installed by `capacity-firstboot`. It cannot revoke permissions on other SSH services. An optional separately
enabled Servers form integrates rotation with controller target replacement.

The exact reviewed plan contains a fresh UUID `request_id`, the original `installation` plan and
`installation_digest`, authenticated `listener_host_key`, exact `old_key` and
`new_key`, current `program_sha256` and `restart_inventory_sessions: true`.
`--print-program-sha256` identifies the fixed compiled embedded program without
reading credentials or contacting hosts; changing any
field, original artifact or program invalidates approval. Supply owner-private
`--plan`, original `--artifact` and `--manifest` files. Plan loading validates the
original manifest and artifact, and rejects admin/inventory key-role reuse.

Without `--approve-digest` the CLI prints the plan and impact only. To execute,
supply the exact displayed digest, separate full-admin `--admin-key`, new
restricted `--new-inventory-key` and a precreated owner-private (0700)
`--state-dir`. Credential/manifest/state directory ancestors must be trusted.
Private keys are never uploaded. The existing full-admin SSH endpoint and new
inventory listener are independently pinned; there is no agent, TOFU or password
fallback. The reviewed administrator endpoint must not use the inventory port.

## Actual operations

1. Durably record unknown locally, before resolving credentials or contacting a
   host. A locked operation journal prevents concurrent local execution.
2. Under the original remote firstboot lock, verify exact root-owned installation
   intent, installed marker, binary, account, fixed sshd config, isolated unit,
   host identity and source-drop firewall. Refuse foreign state or other key sets.
3. Durably journal and atomically publish **old + new** restricted public keys.
   Old access is retained while the new pinned read-only inventory probe runs.
4. Only after that probe succeeds, durably journal commit intent and atomically
   remove the old key. Stop/start only the verified isolated listener with
   `KillMode=control-group` and `SendSIGKILL=yes`. This terminates its existing
   authenticated inventory sessions, which key-file removal alone cannot revoke.
5. Verify owned listener startup and new-key inventory access again; persist the
   authenticated scoped-revocation receipt locally.

The restart interrupts **inventory sessions**, not runner jobs. It does not edit,
reload, restart or stop global/admin sshd. Exact owned config disallows additional
key files, key commands, CAs and alternative authentication for this listener.
Other independently administered SSH authorizations remain outside this claim.
The full-admin SSH connection is not hosted by the isolated listener.

## Uncertain outcomes and limitations

Any disconnect, probe failure, file/journal failure or partial stop/start remains
unknown. Do not assume rollback: both keys may be installed, or the old key may
already be removed while the listener is stopped. Inspect protected journals and
use `--retry-unknown` only with the identical reviewed plan. A retry can recover a
stopped owned listener with the already committed new key, prove its access, and
finish session termination. It never restores a revoked old key automatically.
A request ID is permanently bound to its original plan in both journals; changing
a plan under that ID is refused. A genuinely new later rotation needs a fresh
request ID, even if rotating between previously used public keys. Changed
keys/configuration or reappearing old-only access after commit are refused.

Recorded success is historical, not continuous remote attestation. This workflow
rotates to a verified replacement and revokes the old key on the owned listener;
there is no disable-all-inventory-keys action or cross-service revocation. Local
controller target registration/credential alias updates remain separate explicit
operations. Real-host SSH/session teardown, systemd/PAM/nftables and network fault
qualification is not established by mocked tests. No live hosts were accessed
while developing this change.


## Optional combined Servers workflow

Enable `--allow-inventory-key-rotation` only alongside read-only SSH inventory
and SSH target-management capabilities. Configure deployment-owned
`admin.inventory_key_rotation` with its own `token_file`,
`admin_credential_files`, and `targets`. Each target fixes the complete managed
`plan`, `review_digest`, original `artifact_file` and `manifest_file`. The managed
plan includes `remote` (the exact rotation plan above), `admin_credential_alias`,
`old_target_id`/`old_config` and `new_target_id`/`new_config`. Both tuples must point
to the reviewed isolated listener and use distinct inventory credential aliases;
the new alias must exist in the deployment credential map and registration scope.
Full-admin keys cannot be inventory/prepared-helper keys. No browser request can
upload credentials, choose arbitrary endpoints, supply commands or change hashes.

The separate rotation token authorizes the explicitly reviewed **combined**
operation: staging a new remote key, proving its pinned access, atomically
replacing the old controller target, revoking the old remote key/sessions, and
final verification. Existing read/bootstrap/target-management tokens are not
accepted for this capability, including reverse initialization order.

The controller first records audited `unknown/approved`. A read-only registry
preflight rejects missing/revoked/mismatched old permission before credentials or
network access; exact own replacement receipts permit recovery after a lost reply.
It then records verified milestones `staged`, `proved`, `registry_switched`, and
`committed`. Registry replacement revalidates permissions atomically after the
new-key proof, with a distinct rotation audit role. If that switch fails, no
remote old-key removal is requested. A failed phase write halts later effects.
After remote revocation, a second pinned inventory probe and exact registry replay
check must succeed before the authenticated receipt and `complete/verified` are
committed atomically.

Milestones are historical facts, not proof that no later effect occurred. A lost
reply can leave the local target switched or remote old key removed while status
is unknown. Explicit same-request retry re-proves access and safely resumes;
progress never regresses and new target IDs never overwrite revoked identities.
Closing/cancelling/navigation clears the browser view and secrets, not the remote
operation. Concurrent manual local permission revocation does not cancel already
approved remote work; subsequent registry checks fail closed and completion stays
unknown if the new permission has been withdrawn. A recorded complete result is
historical, not continuous current-host/current-registry attestation.

This adds a separate optional `host_inventory_rotation_operations` table, without
changing SQLite user_version. It does not modify capacity, pool settings or runner
jobs. Browser regression sources cover exact approval, explicit retry and late
responses after cancellation; local browser execution remains unavailable, so
hosted CI is required. Real supported-host qualification remains outstanding.
