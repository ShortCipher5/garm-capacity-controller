# Isolated inventory first boot (experimental supported profile)

`capacity-firstboot` is a separate Linux CLI for first-time inventory setup on a
trusted **Ubuntu 24.04, systemd, distro OpenSSH, Python3 and nftables** host. It is
also available through the separately enabled Servers review form. It requires an independently
pinned existing administrator SSH endpoint and a separate **full-administrator**
credential able to run noninteractive sudo Python. It must never use inventory
credentials or the limited helper authority from prepared-host bootstrap.

This implementation creates a second distro OpenSSH listener. It does not modify,
reload or restart the existing global SSH daemon or configuration. It does not
install missing packages, change existing firewall managers, configure NAT, or
add firewall ACCEPT rules. Unsupported profiles fail closed.

## Explicit trust and approval

Build the static Linux `cmd/capacity-inventory-shell` artifact from a reviewed
source revision. A deployment owner must supply an owner-private manifest with:
`profile: ubuntu-24.04-systemd-nftables/v1`, `architecture`, `artifact_sha256`, and
`installer_sha256` (SHA256 of `internal/firstboot/installer.py`). This manifest is
trusted deployment input, not a browser-supplied hash or a publisher signature.
Keep its ancestors trusted. Its artifact and installer hashes must match exactly;
the CLI does not manufacture artifact trust from whichever binary it is given.

The JSON plan contains `base` (existing pinned administrator endpoint, administrator
fingerprint, controller source IPv4, inventory public key, artifact hash and
architecture), `listen_address` (actual local IPv4), `inventory_address` and
`inventory_port` (intended external/NAT endpoint), `enable_at_boot`, `manifest`,
and `installer_sha256`. The last two may be filled from the supplied manifest;
base fields use the same names as prepared-host bootstrap. The local listener
port is fixed to **TCP 19922**. Administrator and inventory keys must differ.

Run the CLI with `--plan`, `--artifact`, `--manifest`, and `--admin-key` to print
the exact plan and approval digest without connecting. Inspect the privilege,
new account/service, source address, bind address/port, boot choice, keys and both
code hashes. Supplying `--approve-digest` with that exact digest permits execution.
Any plan/artifact/installer change invalidates approval.

Only the embedded fixed installer source enters the remote command. Request data
travels as bounded JSON on stdin; no request-supplied script, command or path is
executed. The remote administrator credential is not uploaded. The installer and
client have bounded deadlines; a disconnect or timeout is an **unknown outcome**.

## Owned resources and restrictions

- Account: `garm-inventory-listener`, system UID/GID, no supplementary groups or
  sudo rights, disabled password, root-owned home. Its login shell is the static
  inventory executable, **not a command interpreter**. Even alternate SSH access
  cannot turn it into an arbitrary shell.
- Files: `/opt/garm-inventory-listener`, `/etc/garm-inventory-listener`, and
  `/var/lib/garm-inventory-listener`. Existing foreign resources are not adopted.
- Separate host key, root-protected public-key authorization and isolated sshd
  config. Public key only; exact user/source; no includes, password, interactive,
  GSSAPI, hostbased or CA auth; no forwarding, PTY, tunnel, RC or user environment.
  Fixed ForceCommand invokes only the read-only inventory shell.
- Unit: `garm-inventory-listener.service`, exact root-owned fragment, no drop-ins.
  `Type=notify` requires daemon readiness; unsupported notification/startup fails.
  Its ExecStartPre verifies/restores its source-drop rule before **every start**,
  including boot. No existing SSH unit is restarted.
- Firewall: owned `inet garm_inventory_listener` table, tagged with the plan
  digest. Its drop rule matches only the reviewed local IPv4 destination and
  TCP19922, rejecting other source IPv4 addresses. It does not affect IPv6 or
  other local destinations. No ACCEPT rule bypasses existing host policy.

Active/enabled UFW, firewalld or nftables service management is unsupported and
is rejected, not disabled. The same check runs before service startup. Other
administrative firewall manipulation or flushing owned rules is outside this
exclusive-table operating profile. SSH independently restricts the allowed
source/key. Existing firewall policy may still block the intended controller;
reachability is never fabricated or repaired with a broad allow rule.

## Crash, retry and rollback

A root-controlled lock serializes attempts. Immutable exact-plan intent and
fsynced phase records precede authoritative changes; files are atomically
published without overwriting different content. Generated private host-key data
and its parent directory are synced before the identity is returned. An immutable
public identity record prevents silently adopting a replacement host key; the
startup guard verifies that identity before each listener start.

Identical retries inspect account, files, unit, firewall and running state.
Changed plans, foreign accounts/groups, unit fragments/drop-ins, occupied bind
addresses, unknown rules or changed artifacts are refused. Start/commit failure
attempts to disable and stop only the verified-owned unit, then removes only its
exact verified rule set after the listener has stopped. It preserves protected
files/account/key/intent for inspection and retry. Failed rollback stays unknown;
journal persistence failures cannot suppress cleanup, and stop is attempted even
when disabling boot activation fails. Unpersisted cleanup remains unknown.
“stopped” is not a claim that all host changes or credentials were erased. A hard
crash during boot enablement can leave the listener active/enabled until an exact
retry or operator intervention; there is no transactional rollback across process
or host death. The startup guard still verifies the restricted rule before start.

The new listener public host key is returned over the **existing pinned admin
connection**. Use that authenticated receipt to pin subsequent inventory; never
trust unauthenticated key discovery from port19922. The receipt explicitly says
`network_reachability_verified: false`. Register the returned identity and perform
a separately authorized inventory probe. No physical capacity enrollment or
exclusive-host authority is established.

## Qualification status

Offline tests cover plan substitution, key-role separation, fixed-command pinned
SSH, receipt integrity, nft destination scope, immutable conflict handling and
mocked startup/retry/rollback (including failure after boot enablement). They do
not execute real sudo, useradd, sshd, systemd or nftables. Ubuntu locked-password /
UsePAM behavior, systemd hardening, nft JSON normalization, boot ordering and real
network reachability still require disposable supported-host qualification before
production rollout. Do not call this deployment-qualified based on unit tests.

## Optional Servers panel integration

The gateway enables browser review/execution only with the explicit
`--allow-isolated-inventory-firstboot` startup flag and deployment-owned
`admin.isolated_inventory_firstboot` configuration. This has its own `token_file`,
`admin_credential_files` alias map and `targets` map. Every target fixes `plan`,
`review_digest`, `admin_credential_alias`, `artifact_file` and `manifest_file`.
Manifest/artifact bytes and plans are read through owner-private checks,
validated and frozen at startup. The approved alias is resolved again only after
a durable operation starts; its signer fingerprint must still match the plan. Full-admin keys must differ from prepared-helper and inventory keys;
tokens must differ from every other admin capability. Loading status never opens
SSH. The browser cannot upload a key, change a destination, supply a command or
substitute an artifact/hash.

The existing Servers registration panel contains the separate first-time review.
Read the entire immutable plan and explicitly approve its administrator identity,
account/listener/firewall changes and boot choice. The gateway audits and commits
`unknown` before resolving any key or starting SSH. Cancellation/navigation only
stops the browser wait, never claims rollback. After interruption/restart, reload
status; an uncertain attempt requires explicit identical-plan retry using the
same request ID. The remote installer serializes retries. A recorded success and
its authenticated host-key receipt are committed together; failed completion
persistence remains unknown. A receipt is historical, not present host health or
capacity authority. Register/probe the returned inventory identity separately.

The optional `host_firstboot_operations` table is separate from prepared-host
operations and created only when this capability starts. No schema version bump
or existing operation reinterpretation is performed. Browser regression sources
cover exact approval, explicit retry and cancelling pending responses; local
browser execution was unavailable, so hosted CI must supply that qualification.
