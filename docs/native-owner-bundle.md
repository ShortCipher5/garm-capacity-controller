# Offline native-owner bundle builder

`capacity-native-bundle` bridges the exported owner executables to the exact flat bundle required by the pinned SSH preparation workflow. It performs local file reads/writes only. It never executes an artifact, contacts a server, generates credentials, installs a unit, or grants activation.

Required inputs are an export directory, an explicitly trusted SHA256 digest of its `SHA256SUMS`, a private input directory, a fresh output directory, a 64-character lowercase hexadecimal preparation ID, and target `amd64` or `arm64`. The export manifest must list exactly `nativeprepare.ExecutableNames()`. Every copied executable is hash-verified and parsed as ELF64 for the target machine. A matching checksum manifest is integrity checking, not vendor signing or provenance: the deployment must supply the trusted manifest digest.

Private input contains `capsule.json` and the five fixed owner/server/client/CA files from #196. Files must be protected regular files with no hardlinks or symlinks; private files must be 0600. Actual certificate/private-key pairs must parse, controller CA material must parse, and the four role leaf fingerprints must be distinct. The supplied owner client and server certificates must also have different public keys. Controller certificate bytes are not supplied, so this does not establish separate custody of controller keys. The builder does not invent missing trust, image fingerprints, host limits, third-party binaries, network policy or storage configuration.

The existing v5 configuration is rewritten only for the new preparation ID's fixed private TLS paths, copied helper paths and their verified hashes. Controller, host, capacity settings, third-party binaries, protected image descriptors and image pins remain the operator's supplied inputs. Already-derived native domain and egress identities are rejected. Ambiguous duplicate JSON keys, including case aliases, are rejected instead of normalized silently. The output still must pass the actual private capsule checker on the target host. Rewriting helper hashes does not rebuild or qualify a runtime image.

Output is fresh, mode 0700, containing only the exact fixed preparation files and shared canonical manifest. Files are 0600 for safe transfer. A partial output is never overwritten, automatically retried, adopted or deleted. The success report contains ID, manifest digest, architecture, `state=bundle_created_not_prepared`, and `activation_authorized=false`. No raw configuration, private keys or PEM bytes appear in the report or error text.

Example shape (all paths and digests must be real approved deployment inputs):

```
capacity-native-bundle --export /private/export --export-manifest-sha256 <trusted-digest> --private /private/config --output /private/fresh-bundle --id <fresh-64-hex-id> --architecture amd64
```

Docker target `native-bundle-tool` only exports the builder executable. It is not part of the host executable allowlist and is never installed by the preparation helper. Unit tests use real certificate/key parsing and synthetic valid ELF headers to exercise the file protocol; they do not prove host, runtime image, or dynamic-loader qualification.

## Saved authenticated discovery selections

Optional paired `--selection /private/selection.json` and
`--expected-binding /private/approved-binding.json` inputs consume the saved
`native-discovery-selection/v1` configuration. Both files must be protected
0600 regular deployment files. The expected binding is an independently reviewed
`nativeplanning.Binding`, not something inferred from the selection: preparation
ID, policy host, inventory target/review/key pin, and full administrator target
must match exactly. Unknown/duplicate fields, stale/future observations,
qualification claims, overcommit, invalid partitions, or either missing input
are rejected before any output is created.

The builder only replaces the capsule's `limits` and `partition` fields from
the fresh selection. Existing artifact, storage, network and identity inputs
still need explicit approved configuration. OS inventory does not prove a ZFS
pool, quota, isolation boundary or available physical capacity. The output report
adds the selection digest for review; it does not contain secret bytes. A saved
configuration is not an opaque runtime observation or activation capability.

Building creates a new bundle digest. Load that actual output through the
preparation deployment loader and review its new plan before SSH preparation.
This is not an automatic deployment reload, start, enable or activation flow.
The existing selection-free builder remains available for explicitly supplied
configuration; supplying either selection flag never silently falls back.

The inventory and administrator endpoints may differ (for example, separately
pinned listeners or an explicitly configured NAT mapping). Their association is
an operator-approved configuration, not authenticated proof that the endpoints
share physical hardware. Equal addresses would not prove that either. The
builder checks the entire approved pair without inventing an endpoint mapping.

For assembly tied to a reviewed saved choice, also supply
`--selection-digest <expected SHA256>`. A mismatch is rejected before output
creation. The assembly caller must compare the returned selection digest again
before offering registry reload; the digest is not an activation capability.
