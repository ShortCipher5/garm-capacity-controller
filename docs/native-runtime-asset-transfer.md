# Explicit authenticated runtime-asset transfer

`capacity-native-assets-transfer` is a separate deployment CLI for transferring
an independently approved runtime image and five pinned host-tool artifacts to
the bounded **prepared, not activated** staging flow. It is not a gateway route,
not automatic Add Server behavior, and does not alter the existing owner-bundle
SSH protocol. It does not install packages, modify `/usr`, start services, run
transferred host tools, mount/boot the image, or qualify runtime authority.

## Prerequisites and approval

The operator supplies an existing protected flat nativeassets bundle (canonical
`manifest.json`, runtime.raw, systemd-nspawn, zfs, zpool, ip, nft), separately
approved manifest SHA256, and separately built `capacity-native-assets` helper
and approved SHA256. The helper must be a trusted Linux executable for the remote
architecture, with compatible loader/libraries. Artifact hashes do not establish
provenance or ABI/boot correctness. No helper or package is downloaded by this CLI.

The remote host must already have SSH, pinned Ed25519 host identity, approved
administrator SSH credentials, and noninteractive sudo access to the fixed
`/usr/bin/python3 -I` recipe. Those existing powerful privileges are a prerequisite,
not something this tool grants. No trust-on-first-use, password fallback, arbitrary
command/path, credential logging, or host-key reset is provided. Connections use
only the approved literal IP, port, username, host key and signer fingerprint.

The CLI configuration is canonical JSON (no whitespace/newline or duplicate keys),
0600, owned by the invoking deployment account under protected ancestors. Fields:
`bundle_directory`, `helper_path`, `manifest_sha256`, `helper_sha256`,
`journal_directory`, `admin_key_file`, `target`. Target has `address`, `port`,
`admin_user`, `host_key` (public authorized-key text), `admin_key_fingerprint`.
The private key file is separately protected0600. Use one dedicated protected0700
journal shared by all CLI processes for this scope; do not copy/reset/roll it back.

1. `capacity-native-assets-transfer --config=/protected/transfer.json --mode=review`
   hashes held source files, then prints the exact public plan and review digest.
   This has no network effects and does not read the private SSH key.
2. Review the actual target, helper digest, every file size/hash, and transfer cost.
   Invoke `--mode=transfer --approved-plan-sha256=<review_digest>` only with approval
   for that exact plan. Changed files/target/script require new approval.
3. If the outcome is uncertain, preserve all state and use only
   `--mode=inspect --approved-plan-sha256=<same_review_digest>`.

The image may be64MiB–16GiB, each host tool at most128MiB, helper at most64MiB.
Files are streamed, not archived or encoded into browser requests. The remote
upload quarantine AND final staged copy persist, so peak/persistent disk cost can
approach twice the assets plus helper. Neither free space nor ZFS/host capacity is
proved; these deployment copies remain outside capsule quota/accounting. There is
no automatic cleanup or refund. Approval must account for this cost.

## Failure and recovery

A local immutable, fsynced ID intent is committed before connecting. Duplicate or
concurrent transfer, failed authentication, cancellation, timeout, restart, and
lost response never authorize a second upload. The remote fixed recipe also
creates/fsyncs a fresh ID quarantine before accepting bytes. It hashes every
bounded frame, closes unrelated descriptors when invoking only the exact pinned
helper FD, and passes the independently approved manifest digest to that helper.

Remote upload state: `/var/lib/garm-capacity-runtime-upload/<id>`.
Final fixed staging: `/var/lib/garm-capacity/native-runtime-assets/<id>`.
Neither path is selected by caller text. Inspection validates original upload
intent and pinned helper before invoking the helper's read-only completed-output
inspection. Partial state stays unknown. Missing local source/helper or journal
also prevents recovery; retain them. Receipt says `artifacts_prepared_not_activated`
and `activation_authorized:false`. Trusted-root rollback/concurrent mutation is
outside this deployment-state model, not a supported adoption mechanism.

There is a30-minute connection/operation deadline, not a promise to interrupt or
undo every remote write when a network is lost. Only inspect afterward. The
original manifest/helper pins and source files remain required for inspection.
No retry, removal, overwrite, repair, installation or activation command exists.

Tests use disposable SSH protocol doubles and synthetic nonexecuted artifacts.
They cover real encrypted/pinned transport and exact streaming, not root/sudo,
remote Python availability, actual helper execution or live-host qualification.
The owner runtime/dispatch guards remain unchanged.
