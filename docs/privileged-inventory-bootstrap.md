# Prepared-host inventory bootstrap

This is a separate privileged **Linux CLI** workflow, not a new permission on inventory
keys or the browser. It installs the inventory binary and one restricted key on
an already prepared Linux/OpenSSH host. It does not create accounts, change sshd
configuration, restart services, install providers, enroll capacity, or rotate
existing remote keys. Existing installations are never overwritten.

## Administrator preparation (required, not automated)

Through a trusted administrator session:

- Install `capacity-inventory-bootstrap` root-owned at
  `/usr/local/libexec/capacity-inventory-bootstrap`, mode0755, with root-controlled
  ancestors. Grant a separate bootstrap account noninteractive sudo for **only
  this exact helper with no arguments**. Never give inventory keys this authority.
- Create `garm-inventory` with a non-root UID/GID, no supplementary groups, **no
  sudo privileges or other elevated authority**, and an empty root-owned home
  `/var/lib/garm-inventory`. It needs a usable shell for OpenSSH forced commands.
- Prepare root-owned non-group/world-writable directories `/usr/local/libexec`,
  `/etc/ssh/authorized_keys`, and `/var/lib/garm-capacity-bootstrap`.
- Configure the inventory account's effective OpenSSH policy: UseDNS no,
  AuthenticationMethods publickey, PubkeyAuthentication yes; PasswordAuthentication,
  KbdInteractiveAuthentication, GSSAPIAuthentication, HostbasedAuthentication and
  PermitUserEnvironment no; AuthorizedKeysCommand, TrustedUserCAKeys and
  AuthorizedPrincipalsCommand none; AuthorizedKeysFile must be exactly
  `/etc/ssh/authorized_keys/garm-inventory` (not a tokenized or alternate path).
  AcceptEnv must be absent or limited to LANG and LC_*; wildcard or startup/loader variables are rejected.
  Configure every applicable source address consistently. The helper checks
  `sshd -T -C` for the reviewed source; it never changes this policy.

This preparation is a trust prerequisite. The helper checks account/group/home,
root-controlled installation paths and effective SSH policy, but does not prove
that arbitrary PAM modules, sudo rules or other host software are trustworthy.
Do not present an unprepared or compromised machine as safely bootstrapped.

## Review and install

Build a trusted **static Linux** `capacity-host-inventory` for amd64 or arm64.
Use a separately provisioned administrator private key in an owner-private file.
The inventory public key must differ from the administrator public key.
Create a JSON plan with `address` (literal IP), `port`, `admin_user`, independently
verified Ed25519 `host_key`, exact `controller_ip`, plain Ed25519 `inventory_key`,
`architecture`, and optional `artifact_sha256`/`admin_key_fingerprint`.

Run `capacity-bootstrap --plan plan.json --artifact capacity-host-inventory
--admin-key /trusted/admin-key`. This only prints the reviewed plan and digest;
it does not connect. Verify the destination, host fingerprint, separate admin
credential, controller source, inventory key and artifact digest. Then run the
same command with `--approve-digest EXACT_DIGEST` to perform the install.

The client invokes only `/usr/bin/sudo -n
/usr/local/libexec/capacity-inventory-bootstrap` over the pinned SSH connection.
Bounded structured stdin carries the artifact; no caller-supplied shell command
or script is executed. No PTY, ssh-agent, password, ambient SSH configuration,
forwarding or host-key discovery is used. A maximum60-second client deadline and
55-second helper deadline bound the operation.

## Partial installation and retry

A root-controlled durable intent binds the exact plan before installation.
Binary publication precedes key publication; each file is fully written/synced
then atomically linked without overwriting an existing destination. Matching
files make the exact-plan retry idempotent; different plans or existing differing
files fail closed. Protected temporary files can remain after abrupt termination;
an administrator may inspect/remove those, never the intent indiscriminately.

A timeout/lost reply is **uncertain**, not rollback. Retry only the identical
reviewed plan or have an administrator inspect the protected intent and files.
Changing an already-installed key/artifact requires a separate reviewed upgrade
or rotation procedure; this installer will not overwrite it. After success,
register the restricted inventory credential and separately authorize inspection
in the existing Servers flow. Success is not a physical-capacity attestation.

### Protected artifact and endpoint policy

Before granting the limited helper permission, install root-controlled
`/etc/garm-capacity-bootstrap/host.json` (including protected ancestors) containing
`address`, `port`, `controller_ip`, `local_address`, `local_port`, and
`artifact_sha256`. The first three bind the reviewed destination/source; the local
coordinates are the server's actual sshd listening address/port, including NAT
translation where applicable. The hash must be independently approved from a
trusted build, not copied from an untrusted bootstrap caller. A caller-supplied
hash alone is **not** artifact trust. The helper rejects unapproved artifacts.

The isolated inventory account must use `/bin/sh`, resolving to a root-controlled
shell, and ForceCommand must be `none` in the effective sshd policy so it cannot
override the restricted key command. The prepared inventory account's absence of
sudo or other direct per-user privilege is an administrator prerequisite; the
helper does not claim to audit arbitrary privilege-granting software.

## Verification boundary

Unit tests cover reviewed-plan substitution, scope/key separation, protected
artifact matching, effective-policy rejection, bounded output, immutable file
publication and retry, and a disposable in-process SSH server's pinned-host/fixed
command protocol. Temporary-directory filesystem tests inject ownership checks;
production always enforces root-owned paths. These tests are not real privileged
host preparation, actual sshd/PAM/sudo qualification, crash-power-loss testing, or
proof that a real deployment's isolation policy is correct. Those remain required
before deployment. No live hosts are accessed by the tests.

The CLI verifies that its actual administrator signer differs from the inventory
public key. The privileged helper cannot authenticate the originating SSH signer
from stdin: its permission boundary is the host's preconfigured sudo policy plus
protected artifact/endpoint allowlist. A directly authorized sudo caller can
submit a different claimed administrator fingerprint. Do not treat this field as
host-side proof of credential separation or an independent authorization grant.

## Optional Servers UI / API integration

Gateway `--allow-prepared-host-bootstrap` enables a distinct bootstrap role. With
no opt-in the route is disabled. `admin.prepared_host_bootstrap` contains:

- `token_file`: separate owner-private64-hex bootstrap token, unequal to every
  existing read/write/inventory/target-management token.
- `admin_credential_files`: deployment-owned alias-to-private-key-file map.
- `targets`: at most8 fixed target IDs, each with `plan` (the complete reviewed
  plan above), `review_digest`, `admin_credential_alias`, and `artifact_file`.

Artifact files are read once at startup through owner-private file validation;
use a trusted mode0600 artifact copy. Credential fingerprints must match the plan
and must not equal any configured inventory key, including another bootstrap
plan's inventory key. No browser may upload keys/artifacts, select filesystem
paths, change destinations or supply commands. Config validation is local-only.

The existing Servers **Add an SSH server configuration** panel contains the
prepared-host installer. Load targets/status using the separate bootstrap token,
select a target, review its exact destination/keys/artifact/digest, re-enter the
token and explicitly approve installation. An async worker returns promptly;
re-enter the token and reload durable status to check its outcome. The token is
cleared after use and on closing/clearing the panel. No background polling retains
an operator token.

Before credential resolution or network contact, SQLite atomically records the
request, audit and `unknown` outcome. Success only changes that record to
`installed` after the pinned helper returns the exact confirmation. Credential,
transport, audit-completion or shutdown errors leave it unknown. Restart never
replays the operation. Closing the browser does not cancel or roll back an
approved installation. A changed deployment plan conflicts with prior durable
state rather than replacing it.

An exact approval replay is a read of its existing outcome, not another attempt.
Retry requires loading the durable request ID and explicitly approving
`retry_unknown` for the same target/digest/request. Same-process overlap is
rejected. Different processes may overlap an explicitly approved identical
retry; protected remote immutable publication prevents a new or conflicting
installation. Retries are bounded to100 attempts and retained operations to1024.
Success cannot regress to unknown because of a late failed attempt. No automatic
retry, key rotation, server enrollment or capacity claim is implied.
