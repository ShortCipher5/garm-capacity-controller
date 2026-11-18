# Native owner executable artifacts and private configuration check

This is an executable packaging path, not an activation path. The capsule owner
still refuses normal `--capsule-config` execution before reading configuration or
making any host changes. The legacy `deploy/native-domain` unit starts a different
bare-domain mode; do not use it as a capsule unit.

## Build the same-source artifacts

From a reviewed checkout, export the non-runnable Docker target:

```sh
docker build --target native-owner-artifacts --output type=local,dest=./native-owner-out .
(cd native-owner-out && sha256sum --check SHA256SUMS)
```

The target compiles the actual owner, capsule launcher/stage, image/ZFS/egress
helpers, quota reader, protected boot/listener launcher, stock ingress, managed
GARM/writer, gateway, provider and ledger commands. It uses CGO, matching the
canonical SQLite implementation. There is no ENTRYPOINT, installation, service
start, enrollment, key generation or bootstrap during export. The manifest binds
file bytes, not trust: review/pin the checkout and build images independently.
The Dockerfile's existing image tags are not a substitute for deployment digest
pins. The compiled dynamic libc dependencies must be qualified on the destination.

This does **not** include or manufacture Incus, the pinned patched liblxc, systemd
nspawn, ZFS, nftables, IP tools, a native runtime image, runner bundles or credentials.
Those remain separately pinned inputs. Pass the exported command hashes into the
existing image manifest and private capsule configuration. The in-image quota
reader and stage must match their configured exact hashes; the package does not
silently invent a qualified image or overwrite configured paths.

## Inspect the real private configuration without starting it

On the intended host, after placing configuration and TLS files under the existing
root-owned/private-file rules:

```sh
/usr/libexec/garm-capacity/capacity-native-domain \
  --capsule-config=/etc/garm-capacity/capsule.json --check-capsule-config
```

This explicit mode reads the actual config, validates all existing budget/policy/
artifact-path constraints, loads the actual certificate/key pairs and CA, and checks
four distinct owner/controller role fingerprints. It prints only bounded boolean
results and the `local-read-only` mode. It does not print keys, endpoints or config.
It does not contact the controller, create a listener, run helpers, inspect or
change a manager, create datasets/images, or enroll anything.

`configuration_valid` and `tls_roles_valid` describe syntax and local role material
only. `activation_authorized` and `host_qualified` are always false. This check does
not prove certificate validity at a remote peer, executable hashes, real physical
budgets, daemon confinement, guest networking, ABI compatibility or readiness.

The gateway continues to own a separate ledger and owner-only claim listener. Use
its existing enrollment/stock/profile configuration and exact role pins; neither
artifact export nor this check generates gateway authority, changes pool maxima,
removes runtime refusal or couples the owner's lifetime to gateway restarts. A
capsule systemd rendering/install path still requires the real qualified filesystem
mount contract; this package intentionally does not invent writable ZFS paths.
