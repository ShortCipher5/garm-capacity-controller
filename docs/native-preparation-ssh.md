# Explicit native-owner preparation over pinned administrator SSH

This is actual upload and disabled preparation code, not activation. It consumes
`nativeprepare`'s exact fixed bundle and validates the prepared owner configuration
using its pinned executable. No systemd unit is installed, enabled or started; no
existing job, host daemon, firewall, filesystem, or native resource is stopped or
adopted. A compatible runtime image and the external host dependencies still need
separate deployment/qualification. The native boundary refusal remains intact.

Startup requires `--allow-native-owner-preparation` plus the private admin
`native_preparation` object: `token_file`, `admin_credential_files` (alias-to-key
paths), and `targets`. Each target contains `target` (address, port, admin_user,
host_key, admin_key_fingerprint), `admin_credential_alias`, `bundle_directory`,
`journal_directory`, and exact `review_digest`. The deployment uses LoadBundle and
Plan to compute the review digest; there is no browser upload or chosen command.
The bundle is exactly the 18 executable files and six private configuration/TLS
files from nativeprepare plus manifest.json. Raw export SHA256SUMS is verified by
the deployment pipeline, not included as an unbound extra bundle file.

The separately protected deployment directory and journal must already exist.
Their ancestry is root- or service-owner-controlled and not group/world writable;
private bundle files are service-owned0600. A non-root gateway can therefore read
its own approved files without becoming a root service. Restricted inventory and
bootstrap-helper keys are refused as full-admin credentials. This action does
transmit the owner's private TLS keys/configuration to the explicitly approved,
pinned server; the UI must disclose and confirm this. They never enter a browser,
receipt, log, or canonical capacity ledger.

The fixed Python recipe requires existing Python3 and noninteractive sudo on the
target. It creates a fresh root-only `/var/lib/garm-capacity-upload/<ID>/bundle`,
streams exact bounded frames (no archive), hashes all bytes, and executes the held
pinned preparation helper. Destination is fixed by nativeprepare. Per-file and
aggregate limits are enforced independently at both ends. Child output is bounded
before append; only the owned helper child is killed on timeout. The recipe never
executes caller commands or interpolates request fields into a shell command.

## API and uncertainty

GET and POST `/admin/api/v1/native-preparation` use a distinct token and same-origin
checks. GET exposes only reviewed destination/key fingerprints, public digests,
byte count, preparation ID and historical state. POST accepts exactly
`target_id`, `review_digest`, `confirm_prepare:true`, and `inspect_only`.

A durable local unknown intent is synced before resolving credentials or starting
SSH, and before HTTP202. Work continues independently of a closed browser, bounded
to three minutes. A lost reply, partial copy, bad pin/hash, disconnected SSH, or
failed helper leaves unknown and preserves all files. An ID is never uploaded or
prepared again. Operator recovery is explicit inspect-only for the same reviewed
ID. The pinned helper verifies the original completed files; partial preparations
remain unknown and require private operator investigation, not automatic cleanup.

A successful receipt means only `prepared_not_activated`. GET's receipt is
historical, not a current remote attestation. An accepted later inspection returns
unknown; an old receipt does not establish that inspection succeeded. No endpoint
claims runtime qualification, physical completion, freed capacity, or deployment.
Source upload quarantine is retained. Equivalent-copy cleanup is not performed.

Tests use fake bundle bytes and disposable local SSH servers only. Python tests
exercise bounded harmless child output; they do not install anything or contact a
server. Actual Linux preparation, sudo/Python availability and live deployment
have not been qualified by these tests.

## Exact inspection outcomes

State now includes optional `attempt`: `sequence`, `mode` (`prepare` or `inspect`),
`review_digest`, `status`, `started_at`, optional `completed_at` and `receipt`.
Each target has a bounded append-only history (at most1024 attempts). Its unknown
intent is durable before credential resolution/network and before HTTP202. The
202 returns that exact attempt sequence. A completed matching receipt records
`prepared_not_activated`; a failed observation records `observation_unavailable`,
which does not prove no remote effects. A hard crash leaves that attempt unknown.

The original preparation receipt remains historical. Consumers must correlate
`attempt.sequence` and review digest to the accepted action before saying the
inspection succeeded. In particular an old top-level prepared receipt plus a new
unavailable/unknown attempt must not be displayed as inspection success. Timestamps
are local operation bookkeeping, not fresh physical authority. Restart preserves
these distinctions. Malformed/gapped/orphan journal state fails closed. Reaching
the bound requires operator investigation; records are not automatically removed.
