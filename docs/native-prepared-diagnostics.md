# Read-only diagnostics from the prepared owner bundle

After a real native preparation completes, its existing Add Server view offers
an optional separately reviewed diagnostic read. Unlike the legacy firstboot
preflight, this uses the already staged `capacity-native-preflight` from the exact
owner bundle; no separately installed `/usr/local/libexec` copy is required.

`POST /admin/api/v1/native-preparation/diagnostic` uses the separate preparation
token and same-origin checks. Its strict JSON body contains `target_id`,
`preparation_id`, the displayed diagnostic `review_digest`, and `confirm_read:true`.
The review binds the current complete preparation plan, manifest, SSH endpoint
and both key pins, diagnostic artifact hash, and embedded fixed recipe. The
response echoes those request identifiers with a bounded unqualified report.

The service requires actual durable completed preparation history and the current
registered plan, excludes concurrent preparation/replacement/diagnostics, and
joins cancellation on shutdown. It changes no upload intent, attempt or receipt.
A failed read is unavailable, not a new preparation or automatic retry. Browser
cancellation cancels this read; each subsequent read requires explicit approval.

The existing approved SSH administrator and noninteractive sudo/Python remain
prerequisites. The remote recipe derives only the fixed prepared ID path,
checks protected metadata and the exact pinned manifest/receipt/unit, hashes the
held regular diagnostic descriptor, and executes that descriptor with unrelated
FDs closed and a fixed environment. It does not inspect every other bundle file,
install a unit, start the owner, alter storage/network configuration, or mount an
image. Trusted root/loader integrity remains a prerequisite. Checks of the
manifest and diagnostic bytes are not boot or executable provenance qualification.

All reports retain `unqualified_no_activation`. They are informational observations,
not capacity enrollment, physical isolation, dispatch permission or release/refund
evidence. Normal owner activation and runtime guard refusals remain unchanged.
Tests use local SSH protocol doubles and unprivileged parser/hash fixtures; no
remote root helper, real host diagnostic or live deployment was exercised.
