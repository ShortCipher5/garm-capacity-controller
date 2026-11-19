# Read-only preflight from server onboarding

A deployment may add `preflight_sha256` to an existing approved firstboot target.
The value pins a separately installed, root-owned diagnostic executable at
`/usr/local/libexec/capacity-native-preflight`. This change does **not** install
that executable, broaden the restricted inventory account or change sudo policy.
The existing separate firstboot administrator credential and pinned Ed25519 host
identity are reused only after an explicit reviewed read action.

`GET /admin/api/v1/firstboot` includes an optional `preflight` recipe containing
its review digest, executable SHA256, embedded recipe SHA256 and
`read_only_unqualified` mode. The digest binds the original reviewed endpoint
plan and both artifacts. The read action is:

```
POST /admin/api/v1/firstboot/preflight
{"target_id":"server-one","review_digest":"<recipe digest>","confirm_read":true}
```

It requires the existing separate firstboot token and same-origin checks. No
credential, address, path, script, shell command or replacement pin is accepted
from the browser. An installation or diagnostic already running on that target
blocks another action. Closing the request cancels the local SSH transport; the
read-only remote subprocess also has a fixed timeout. There is no automatic retry
and no installation journal, ledger or enrollment mutation.

The embedded fixed root recipe checks every executable ancestor, rejects
symlinks and writable paths, special mode bits and file capabilities, hashes the
held executable and runs that same descriptor with closed unrelated descriptors
and a fixed environment. It returns a bounded nonce-bound report over pinned
SSH. The report always remains `unqualified_no_activation`; it cannot serve as
fresh physical-capacity evidence. The remote timestamp is informational and may
reflect clock skew. Unsupported LXC, ARC and privilege conditions are report
items, not instructions to weaken security. Missing binaries or unavailable
sudo/Python/pins return actionable unavailable status, never a fake success.

Tests use only an in-process local SSH fixture and mock state. No deployed host
has been contacted and no diagnostic executable has been installed by this work.
