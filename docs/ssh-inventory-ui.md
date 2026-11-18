# Staged SSH inventory in Servers

The existing Servers page now contains **Inspect a new server over SSH**, not a
new page. This connects the SSH transport to a separately authorized API and UI.
It remains an explicit read-only inspection stage, not installation, Incus
identity binding, enrollment or runner activation.

## Deployment boundary

A private gateway config may contain `admin.ssh_inventory`:

```json
{
  "token_file": "/private/admin-ssh-inventory.token",
  "credential_files": {"server-read-key": "/private/server-read-key"},
  "targets": {
    "candidate": {
      "address": "192.0.2.20",
      "port": 22,
      "user": "inventory",
      "host_key": "REPLACE WITH INDEPENDENTLY VERIFIED SSH PUBLIC KEY",
      "credential_ref": "server-read-key"
    }
  }
}
```

The separate `--allow-read-only-ssh-inventory` process flag is required. Merely
adding configuration never enables probes. The token is 32 random bytes encoded
as 64 lowercase hex characters, stored in a private file, distinct from read,
ledger-management and Incus-onboarding role tokens. Credential files must be
owner-private regular files. Startup validates configured aliases and keys without
contacting the host. No credential is uploaded from the browser.

The deployment operator authorizes the **whole tuple**: target ID, literal IP,
port, username, exact SSH host key and credential alias. An alias alone never
authorizes use against arbitrary destinations. The inventory endpoint cannot submit or edit
any of those values. The optional [target-registration workflow](ssh-target-registration.md)
adds browser-created targets under a distinct role and explicit deployment-scoped
credential/network/user/port policy. It is not enabled by the inventory flag alone.

## UI/API flow

1. Enter the separate SSH inventory token and load configured targets.
2. Review the displayed address, username, credential alias and SSH fingerprint.
3. Independently verify the host key and explicitly confirm that pairing.
4. Re-enter the token and run one read-only inventory probe.

GET `/admin/api/v1/ssh-inventory` returns approved tuples and their review digests.
POST accepts only target ID, exact review digest, a UUID request correlation and
`verified_host_key: true`. Both require HTTPS, the independent token and same
origin. Unknown fields, duplicate JSON keys, changed digests and unreviewed
requests fail before contact. The digest identifies the reviewed tuple; it is
not a bearer credential and cannot replace authentication.

The service records a durable requested audit before resolving a key or opening
SSH, and a completion audit afterward. Audit includes target ID, review digest,
request ID and outcome, not raw addresses, host keys, credential paths or output.
Audit failure prevents a successful result. Request IDs correlate read-only
observations; they are not an exactly-once/replay guarantee. Only one SSH probe
runs per service at a time. The HTTP stage uses an 8-second probe budget plus a
bounded 5-second completion-audit window.

Tokens clear before the request and are never placed in browser storage. Closing
the accordion, clearing/cancelling, navigating away or disconnecting discards
review/results and aborts the request. Late responses cannot restore an old
review. Neither network failures nor repeated clicks cause automatic retries.

Results distinguish unknown totals from known zero available memory/space. They
show the authenticated OS view and an explicit warning that it may be a VM or
container, not proof of physical capacity or an Incus storage-pool budget. No
button imports the result into admission configuration.

To withdraw a target, remove it from deployment configuration and restart/stop the
old process; disabling the flag removes the entire API. This local withdrawal
is not remote key revocation. Removing the SSH credential from the server and
agent installation remain separate future workflows.

## Verification boundary

Transport/service tests use in-process SSH fixtures. API tests cover separate
roles, origin restrictions, tuple substitution rejection, durable audit and
failure handling; startup checks make no host connection. Browser tests exercise
review, token clearing, unknown output, cancellation and late responses. No live
server is accessed, and no deployment or installation is performed by this change.
