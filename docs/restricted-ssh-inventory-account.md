# Restricted server-side SSH inventory

`capacity-host-inventory` is an optional, unprivileged OpenSSH **forced command**.
It gives the existing inventory client a constrained server account without
allowing the credential to run arbitrary scripts. The normal client still sends
`/bin/sh -s`; OpenSSH replaces it with this binary. The binary accepts only that
exact original command and discards at most 8 KiB of stdin without interpreting it.
A hard ten-second process timer bounds clients withholding EOF or blocking output. It reads fixed Linux CPU/memory
files and root filesystem statistics directly, without invoking any shell or
external programs. It returns the same six-line inventory protocol.

This is OS-view inventory only, including when the server is a container or VM.
It does not install runner providers, enroll physical capacity, mutate workloads,
or prove exclusive host ownership. No code in this change contacts live hosts.

## Installation on a reviewed host

The following are operator setup steps, not commands executed by the controller.
They change SSH access and must be specifically authorized for the target host.

1. Build for the target Linux architecture:
   `go build -o capacity-host-inventory ./cmd/capacity-host-inventory`.
2. Through an independently trusted administrative session, install the binary at
   `/usr/local/libexec/capacity-host-inventory`, owned by root, mode 0755. Neither
   this path nor its ancestors may be writable by the inventory account.
3. Create or select a **dedicated, unprivileged** account. Give it no sudo access,
   no other login keys and no writable startup scripts or writable parent
   directories in which startup scripts could be created or replaced. The account needs a usable
   shell for OpenSSH forced-command execution, not an interactive shell permission.
4. Generate a dedicated key with `capacity-ssh-key`. Keep the private key on the
   controller in a trusted owner-private directory. Copy only the public key.
5. Build `capacity-ssh-authorize` and generate the restricted authorized-key entry:
   ```sh
   ./capacity-ssh-authorize --controller-ip 192.0.2.10 < inventory-key.pub
   ```
   Use the controller's actual egress IP, not the example. This helper only prints
   a line; it never edits `authorized_keys` or contacts a host. It rejects keys
   containing existing options, additional keys, certificates and non-Ed25519 keys.
6. Install that entry in a root-controlled authorized-key store such as
   `/etc/ssh/authorized_keys/%u`, configured through sshd's `AuthorizedKeysFile`.
   The file and **every ancestor directory** must be inaccessible for writes by
   the inventory account; a root-owned file beneath a writable `.ssh` or home
   directory can still be renamed/replaced and is not sufficient. The `restrict`
   option denies forwarding, PTY allocation, agent forwarding, X11 and user rc;
   the fixed `command` prevents shell/exec/subsystem access. Require an OpenSSH
   version supporting `restrict`; never remove restrictions to resolve a failure.
   Configure sshd for the dedicated account to disable password and interactive
   authentication and disallow user environment settings. Do not accept arbitrary
   environment variables for this account. Check effective sshd configuration.
7. Obtain and verify the host's public key independently. Register the existing
   private-key file alias and restricted network/user/port scope in the gateway,
   then approve the exact target through Servers and separately request inspection.

The restriction belongs to every authorization path for that account. A second
unrestricted key, password login, writable authorized-key store, or permissive
sshd override can invalidate it. This binary is not an SSH daemon and cannot
configure or attest to sshd policy. No automatic installation is implied.

## Rotation and revocation

For rotation, install a second restricted public-key entry, verify the new key
through a new credential alias and target approval, then remove the old entry
through the trusted administrator session. For revocation, remove the specific
public-key entry remotely and separately revoke the controller's local target.
Local target revocation alone does not remove the server-side key. Already open
SSH sessions may finish; these are bounded read-only probes, not runner operations.
