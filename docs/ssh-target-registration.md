# Browser-created SSH inventory targets

This opt-in feature adds **Add an SSH server configuration** to the existing
Servers page. It saves a new destination and verified public host key without
editing the deployment config for every host. It does not contact that server,
install an agent, enroll an Incus backend, change admission budgets or allocate
runners. Inventory remains a separately authorized operation below the form.

## Deployment authorization

Enable both `--allow-read-only-ssh-inventory` and
`--allow-ssh-target-management`. Extend `admin.ssh_inventory` with:

```json
{
  "target_management_token_file": "/run/secrets/ssh-target-management-token",
  "registration_scopes": {
    "inventory-key": {
      "networks": ["192.0.2.0/24"],
      "users": ["inventory"],
      "ports": [22]
    }
  }
}
```

The example address range is documentation-only. The alias must already exist in
`credential_files`, whose private-key file is owner-only. No credential is
uploaded by the browser. The target-management token is another owner-only
64-character lowercase hexadecimal secret, distinct from read, policy-write,
onboarding and SSH-inventory roles. It authorizes approving exact tuples inside
these scopes, not creating credentials or arbitrary outbound access.

Networks must be canonical IPv4 prefixes /24 or narrower, or IPv6 /64 or narrower.
Use /32 or /128 when only one server is permitted. Literal canonical IP addresses
only; no DNS, loopback, link-local, multicast, unspecified or IPv4-mapped IPv6
destinations. Private unicast addresses are supported. Credential alias, username
and port must all match the deployment scope. Keep these scopes and remote SSH
accounts least-privileged. Existing static `targets` remain immutable/reserved.

## Workflow and persistence

1. Enter the separate target-management token and load permitted scopes.
2. Enter a new unique server ID, IP, port, user, approved credential alias and a
   public SSH host key obtained through a trusted independent channel.
3. Review the server-computed SHA256 host-key fingerprint and exact pairing.
   Changing any field invalidates review. No network request to the host occurs.
4. Explicitly confirm the independently verified key and intended pairing, then
   re-enter the target-management token to save.
5. In the inventory section, load approved targets using the separate inventory
   token and independently authorize the read-only probe.

The approval and redacted audit commit atomically in the existing SQLite database
in a separate `ssh_inventory_targets` table. The durable digest binds ID, address,
port, user, public host key and credential alias. A digest mismatch fails closed.
No private key, credential path or target address is copied to the audit.
Identical repeated saves are idempotent; existing IDs cannot be repointed or have
their host key silently replaced. The combined configured and active registered target
list is capped at 32. Active plus revoked records are bounded at 1,024; hitting
that history bound fails closed rather than deleting tombstones. The registration-only store uses the existing transactional
and backup facilities, not a separate file-copy backup scheme.

Restart keeps saved targets, but every list/probe revalidates them against the
current deployment scopes. Removing a credential scope or narrowing its network,
user or port list makes matching saved targets unavailable. Disabling target
management also disables use of its dynamic targets. An already-started probe is
bounded and is not retroactively undone. Reload configuration by restarting the
process; policy is immutable within a process.

Cancellation or a lost save response **does not undo a committed approval**.
The UI clears secrets and late responses but warns that the approval may have
saved. Reload approved inventory targets before retrying. No host-side changes
can occur during configuration approval.

## Deliberate remaining boundaries

This is durable target creation and read-only inspection, not full guided server
installation. Private-key provisioning/rotation, direct target
editing, agent install/update/revocation, SSH-to-Incus identity linking and safe
physical-capacity enrollment remain separate work. Host-key changes require a
new reviewed ID; old target access can be withdrawn through deployment scopes.
The public key must be independently obtained, not accepted from a blind scan.

## Withdraw a saved target

Load permitted scopes with the separate target-management token, select the
saved target under **Withdraw an SSH target**, review its exact identity and
fingerprint, confirm, and re-enter the token to revoke its local inventory
permission. Even records no longer matching current scopes can be withdrawn.
Static deployment targets are not managed here; remove those from deployment
configuration instead.

The exact reviewed digest is required. The approval record is moved atomically
into a durable tombstone alongside its revocation audit; original configuration
is retained, not irreversibly deleted. Subsequent inventory requests cannot find
it. Previously reviewed client state cannot resurrect it, and repeated identical
revocations are idempotent. A new approval needs a different target ID. Revoking
frees an active target slot but never removes history.

Database triggers prevent an older gateway writer from reinserting a tombstoned
ID. Older readers see only the active table, from which revoked records were
removed atomically. Thus rolling back the application binary does not restore
these local permissions. Restoring an older database backup is a separate rollback
of authorization history and must be handled accordingly by the operator.

Already in-flight read-only requests may complete within their existing bounded
timeout. This is not remote key revocation, SSH session termination, agent removal,
runner deletion, retirement or capacity release. It makes no physical-authority
claim. A canceled/lost revocation response can have committed: reload targets;
never assume aborting the browser restored permission.
