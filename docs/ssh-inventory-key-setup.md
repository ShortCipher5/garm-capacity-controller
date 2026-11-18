# SSH inventory key setup

`capacity-ssh-key` creates a dedicated Ed25519 private key on the controller host
and prints only its public key in `authorized_keys` format. It does not connect
to a server, install the key, modify configuration, or start inventory.

Build with `go build -o capacity-ssh-key ./cmd/capacity-ssh-key`, then run:

```sh
./capacity-ssh-key --private-key /path/to/trusted/private-directory/inventory-key
```

The path must not exist. The tool creates an owner-only 0600 regular file and
never overwrites an existing path or follows a final symlink. The parent directory
and its ancestors must be trusted and inaccessible to untrusted users; the tool
does not establish that trust. Protect backups of the private key as secrets.
If output fails after saving, recover the public key with
`ssh-keygen -y -f /path/to/trusted/private-directory/inventory-key`. Never paste
or upload the private key into the web UI.

An operator must separately authorize the public key on the intended server,
configure the key file as an SSH inventory credential alias and registration
scope, and verify the server's host key independently before approving the
target in the Servers page. Use a restricted read-only account appropriate to
the fixed inventory command. This helper does not establish the account,
restrict its permissions, rotate credentials, revoke remote access, install an
agent, or qualify physical capacity. To rotate, create a new path, authorize its
public key and configure the new alias before withdrawing the old one; remove
the old key from the server separately after all intended uses have ceased.
