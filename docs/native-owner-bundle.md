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
