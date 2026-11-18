# Isolated inventory key rotation and scoped revocation

`capacity-inventory-key-rotate` is an explicit Linux CLI operation for the owned
isolated listener installed by `capacity-firstboot`. It is not yet a browser
rotation form, and it cannot revoke permissions on other SSH services.

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
