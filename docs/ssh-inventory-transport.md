# Secure SSH inventory transport

`internal/sshinventory` is the first SSH onboarding transport, not the complete
Add server workflow. It opens one explicitly scoped connection to a literal IP
and port using one deployment-provided public-key credential alias. The expected
server public key must be supplied from an independently verified source.

There is no trust-on-first-use, automatic host-key discovery/acceptance, password
prompt, ssh-agent, ambient SSH configuration, proxy, forwarding, PTY, sudo, remote
package installation or remote configuration write. Changed host keys fail before
user authentication. Ed25519, ECDSA and RSA keys of at least 3072 bits are supported;
RSA host signatures must use SHA-2. Host certificates and authorized_keys options
are not accepted. Credentials are never emitted in errors or results.

`PrivateKeyFiles` resolves only a copied map of deployment aliases. It reads
owner-only regular private-key files through the existing securefiles utility,
rejects final symlinks, and rejects encrypted keys rather than guessing a password
flow. Private key byte buffers are cleared after parsing; the signer necessarily
retains signing material for the connection. No credential is persisted by this
transport. Custom resolvers must honor cancellation and the same private-credential
boundary.

## What the probe does

One fixed `/bin/sh -s` command receives a static read-only script. No user input is
interpolated into it. It reads online logical CPU count, `/proc/meminfo`, and root
filesystem `df` data using absolute Linux tool paths. It neither installs a small
agent nor enumerates processes, files, containers or private workload details.
The remote SSH service's normal authentication/audit behavior is outside this
transport; this is not a claim that an SSH login has no system logging side effect.

Results have unknown values omitted, not fabricated as zeros. Available memory and
root filesystem free space are current observations; totals are the authenticated
OS view, which can belong to a VM or container. They do **not** establish physical
host identity, whole-host ownership, storage-pool capacity or a safe runner budget.
`physical_capacity_qualified` always remains false. Values are bounded and
cross-checked; malformed/oversized responses fail without returning partial data.

The whole operation uses a 15-second context deadline and a 5-second connect
limit; cancellation closes the underlying connection, including during the SSH
handshake. Stdout and stderr have separate 4-KiB limits. Only parsed numeric
inventory is returned, never raw stdout/stderr, hostnames, credentials or addresses.
A host with missing inventory commands can produce explicit unknown fields;
unsupported/malformed shell output fails closed.

## Deliberately not implemented here

- Arbitrary server entry, SSH credential upload or secret storage.
- Key rotation, bootstrap/install approval or host-agent setup.
- Remote credential revocation or removing access from the host.
- Binding this OS view to the existing Incus discovery/review/enrollment identity.
- Runner activation, admission, resource grants, retirement or deployment.

The current Incus connection flow uses endpoint/project/TLS-key identities. An SSH
key is a different identity namespace; treating one as the other would be unsafe.
A later enrollment workflow must explicitly bind both observations and handle
revoked or changed credentials. The separate opt-in [staged SSH inventory UI/API](ssh-inventory-ui.md)
now wires this transport to reviewed read-only inspection, never host enrollment
or activation. No live host was accessed during development.

Tests use disposable in-process SSH servers, generated keys and temporary private
files. They exercise exact key pinning, fail-before-authentication mismatch,
read-only command restrictions, bounded output, malformed data, unknown fields,
credential aliases and cancellation during an unresponsive handshake.
