# Separate offline runtime artifact preparation

This helper prepares approved bytes in a fresh private directory. It does not
install system packages, modify `/usr`, execute tools, mount an image, change a
service, contact a host, or enable the native owner. Existing runtime guards stay
closed. The current SSH preparation stream is unchanged and does not transfer
these artifacts. A future authenticated transfer requires separate review.

## Exact input and approval

A deployment administrator supplies a protected flat directory containing exactly
`manifest.json`, `runtime.raw`, `systemd-nspawn`, `zfs`, `zpool`, `ip`, and `nft`.
The canonical JSON manifest is version `native-runtime-assets/v1`, a fresh 64-hex
`id`, `architecture` (`amd64` or `arm64`), and `files` mapping each exact filename
to `{"sha256":"<64 lowercase hex>","size":123}`. Canonical JSON uses the Go
Manifest field order and sorted map keys. Review the manifest digest independently
of the upload directory; do not derive approval from untrusted sibling metadata.

`runtime.raw` is bounded to 64 MiB–16 GiB, in whole MiB. Each host tool is bounded
to 128 MiB. Files are streamed, not loaded into memory or unpacked as archives.
Use the existing image compiler and read-only image inspector to verify a build
before assigning its approved hash here. This helper verifies bytes and actual
ELF header architecture/type only; it does not validate image filesystem contents,
tool dynamic loader/libraries, package provenance, kernel support or live behavior.
Those are still explicit deployment/qualification responsibilities.

## Local preparation, not activation

An administrator first provides the existing root-owned 0700 parent
`/var/lib/garm-capacity/native-runtime-assets`. Run the separately approved helper:

```
capacity-native-assets --prepare-source=/protected/reviewed-input --expected-manifest-sha256=<reviewed digest>
```

It creates only the fresh `<parent>/<id>` directory. Directory creation is the
permanent consumed intent; a later failure never permits automatic retry or
adoption. The canonical intent is fsynced before copying. Tools are staged0555,
image0400, directory0700, with exact hashes, sizes and source metadata checked.
An actual completed receipt says `artifacts_prepared_not_activated`, with
`activation_authorized:false`. Template paths must explicitly reference these
staged files in a separately reviewed configuration; this helper does not rewrite
templates or claim the staged programs can run safely on the target.

After a lost response, only inspect the exact approved ID and digest:

```
capacity-native-assets --inspect-id=<id> --expected-manifest-sha256=<reviewed digest>
```

Inspection rehashes all files and refuses missing, partial, linked or changed
outputs. Preserve unknown/partial directories. No cleanup, overwrite, permission
repair, ID reuse, remote retry or service enable/start path exists. Trusted-root
concurrent mutation and filesystem rollback are outside this preparation contract.

The separate Docker export target `native-runtime-assets-helper` emits only the
preparation executable and its SHA256SUMS. It has no entrypoint and does not run
host tools during build. It does not alter the existing owner-artifact export or
its fixed preparation allowlist. Docker build execution and remote deployment
remain separate qualification steps; source/unit tests are not evidence of either.

The staged copies consume deployment disk space outside the running capsule's
quota. Per-file bounds are not a physical-host free-space or capacity-accounting
proof. Review available space before preparation and retain uncertain copies;
this tool neither refunds capacity nor automatically removes deployment assets.
