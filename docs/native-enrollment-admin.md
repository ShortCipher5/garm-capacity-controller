# Native enrollment in the existing management console

This optional endpoint adds real pinned owner enrollment, not runner activation.
It depends on the canonical atomic first-setup barrier from the native enrollment
CLI. No HTTP request can supply a host URL, TLS credential, policy or ready flag.

Private gateway `admin.native_enrollment_recipes_file` selects an owner-private
JSON array of `{id,mode,owners}`. `mode` is exactly `initial` or `existing`.
Each owner has the same `endpoint`, `tls`, `owner_client_leaf`,
`owner_server_leaf` and complete `policy` fields as the CLI deployment file.
The normal explicit experimental-management opt-in and distinct management token
are required; absent management authority rejects startup, not just writes.
Credentials are loaded privately and frozen at startup. Review revision binds
endpoint, exact role leaves, controller certificate, trust material and policy.

`GET /admin/api/v1/native-enrollment` accepts the existing read/management bearer.
It returns sanitized recipe choices, complete canonical enrolled records and one
coherent audit sequence. Endpoints, credential paths/bytes and native keys are
not returned. `POST` requires same-origin management authentication and exactly
`{recipe,recipe_revision,expected_sequence}`. Initial mode rechecks the complete
host set, wholly unbound/uncharged/not-ready state and audit CAS in the same
transaction as its new fence. Existing mode never rebinds host identity.

Collection contacts only the configured pinned owner, then records canonical
immutable enrollment. It never completes reconciliation, changes readiness,
changes grants/pools, starts native work, exempts accounting or releases capacity.
A failure can occur after the initial fence or some host/domain records commit;
responses report confirmed domain count and possible partial changes. Read the
fresh canonical state after every attempt, especially a lost HTTP response.
Do not automatically retry or switch initial/existing modes. An already-bound
host uses a separately reviewed existing-mode recipe, never a reset/rebind.

Loopback mTLS/SQLite tests cover actual enrollment, frozen TLS credentials,
stale review, immutable replay, failed observation retaining only its fence,
read-token/cross-origin refusal and strict JSON. These are not live-host or
browser qualification, and no production execution guard is removed.
