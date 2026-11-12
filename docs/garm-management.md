# Narrow Garm v0.2.1 management client

`internal/garmapi.NewManagement` is an experimental operator integration, separate
from inventory and the ledger policy editor. It requires its own explicit HTTPS
endpoint, CA trust, token and exact Garm pool/repository UUID allowlists. Never
silently reuse the inventory token or expose either Garm credential to a browser,
job, provider process, response payload or error log. Writes default off.

## Supported scope

- Read sanitized configured pools/repositories using authenticated GET requests.
  No unscoped repository collection, provider specs, credentials, instance/job
  payloads or raw upstream errors are returned.
- Update existing pool tags, priority, maximum, minimum idle and enabled state.
  All inputs are typed and bounded. Minimum idle must not exceed maximum.
- Update an existing repository's balancer to `roundrobin` or `pack`.
- Verify fresh preflight settings against the caller's displayed fingerprint and
  read authoritative settings back after every successful write response.

No pool/repository creation or deletion, credential management, image/flavor,
provider, extra-spec, shell, OS, template or runner-group change is exposed.
Those generation-changing settings can cause runner replacement. **Minimum-idle
reductions are blocked** because they can initiate Garm's idle scale-down path;
assignment-safe retirement is not qualified. Existing startup-bound pool/provider
placement remains unchanged. An unbound Garm pool is not schedulable merely
because its ID appears in the ledger, and there is no path here to select an
unrestricted provider.

## Honest operational effects

- Changing tags does not relabel existing runners. Old idle runners can still
  accept jobs matching their existing labels. These are future-runner labels,
  not a job-assignment fence.
- Disabling a pool stops its new runner creation. It neither drains the pool nor
  prevents existing idle runners accepting jobs. No job cancellation or instance
  deletion is sent by this client.
- Increasing idle or maximum settings can request future runners. They pass the authoritative ledger/facade only for separately qualified gateway-routed pools. Garm maxima are demand controls,
  not atomic global capacity enforcement.
- Priority and repository balancing influence Garm selection. Repository balancing can affect every eligible pool in that repository, including pools outside this console allowlist; capacity promises do not extend to unqualified/unmanaged routes. They are not a
  fair scheduler or a change to a running job.

## Concurrency and ambiguous outcomes

Pinned v0.2.1 has **no atomic revision/ETag/If-Match update contract**. A local
mutex plus fingerprint preflight rejects ordinary stale edits but cannot stop a
Garm UI/CLI or another process writing between GET and PUT. These fingerprints
must never be called external CAS or a job-safety fence. Write enablement requires
external single-writer coordination covering every Garm settings writer. The
client does not implement that fence or prove it exists.

Write requests are never automatically retried, including redirects and error
responses. An error, malformed/oversized body, failed readback, concurrent protected
setting change or readback mismatch produces `ErrManagementOutcomeUnknown`: the
write may have committed. Refresh and reconcile authoritative state before any
further action. The error deliberately does not include upstream URLs, bodies or
credential-bearing network errors. A successful readback is evidence of that
observation only, not proof of an external atomic update.

This client alone does **not** supply durable cross-restart request intents or
audit storage. The separate [journal foundation](garm-management-journal.md) adds
those records and fail-closed uncertainty blocking, but intentionally cannot
resolve an ambiguous operation from a GET alone. The libraries themselves do not supply an external single-writer fence. The experimental console wiring below adds privileged HTTP routes, UI forms and startup credential loading. Any exposed management service must add explicit separate
write authentication, bounded operation contexts, durable pending/outcome records,
audit and honest UI handling of ambiguous outcomes before claiming those
properties. Never report an uncertain PUT as an unapplied failure or blindly
replay it after restart.

## Verified protocol evidence

Source pinned to Garm v0.2.1 commit
`154638445c3949c1958b01812f69d9a1e4d82684`:

- `params/requests.go`: `UpdatePoolParams` pointer fields; tags as strings;
  repository **request** field `pool_balancer_type`.
- `params/params.go`: response tags are objects with `name`; repository
  **response** field is instead `pool_balancing_type`; defaults use omitempty.
- `apiserver/controllers/pools.go` and `repositories.go`: authenticated
  `GET/PUT /api/v1/pools/{id}` and `GET/PUT /api/v1/repositories/{id}` contract;
  neither implements conditional HTTP updates.
- `runner/pools.go`: validates effective minimum/maximum together.
- `database/sql/util.go`: these five pool settings do not increment generation;
  image/flavor/specs/OS/shell/group do. Existing running resource rows are not
  directly edited by this settings update.
- `runner/pool/pool.go`: disabled pools skip create and scale-down; lowering idle
  target can invoke idle scale-down. This does not qualify the assignment race.

Local tests use disposable verified-TLS authenticated fixtures, covering scope,
write opt-in, private-data minimization, malformed data, stale edits, narrow JSON
requests, generation/protected-field readback, no redirects/retries and ambiguous
commit responses. The pinned upstream SQLite/Runner checks can be reproduced with
Go 1.26.2+, CGO and a C compiler:

```sh
integration/garm-v0.2.1/management-test.sh /tmp/new-disposable-garm-management
```

The pinned-garm-compat hosted CI also runs this management semantics script. The script refuses an existing directory, fetches exactly the public pinned
commit, adds only disposable tests, and runs no workers/providers or live APIs.
It verifies actual upstream decoding, validation and storage behavior, not live
infrastructure or end-to-end production lifecycle safety.

## Experimental authenticated console wiring

The gateway now wires this client **only through the durable journal manager** to
separate authenticated admin routes and real React review/apply forms. This is not
production qualification. No pool/repository registration, deletion, provider,
placement, credential, image/flavor/spec, job drain or retirement controls exist.

Add `admin.garm_management` in the private gateway configuration:

```json
{
  "endpoint": "https://garm.example.invalid",
  "token_file": "/private/garm-management.token",
  "tls": {"ca": "/private/garm-ca.pem", "certificate": "/private/client.pem", "key": "/private/client.key"},
  "providers": ["capacity"],
  "pool_ids": ["00000000-0000-4000-8000-000000000001"],
  "repository_ids": ["00000000-0000-4000-8000-000000000002"],
  "journal": "/private/management-state/garm-management.db",
  "journal_lineage": "<exact 64-character lineage returned by explicit initialization>",
  "qualified_admission_routing": false,
  "exclusive_settings_writer": false
}
```

These are the exact TLS field names used by this gateway. See [service configuration](gateway-service.md). The host above is deliberately invalid, not an infrastructure
address. The upstream token is loaded from an owner-only file and must differ
from the inventory token and both browser role tokens. It is never returned to
the browser. The journal must be a separate existing owner-owned private schema2 SQLite file, not the admission ledger, with an owner-owned0700 parent directory and0600 file. Normal gateway startup never creates or adopts one. Exact provider and UUID allowlists are trusted
startup configuration, not arbitrary browser endpoint or scope controls.

Before first service use only, explicitly initialize a **new** path under the preexisting private state directory:

```sh
go build -o ./garm-journal-init ./cmd/garm-journal-init
./garm-journal-init -journal /private/management-state/garm-management.db
```

The initializer prints `journal_lineage`. Save that exact identifier in independent trusted durable gateway configuration. It is not a bearer token, and must not be discovered/adopted automatically from whatever database happens to be mounted. The initializer refuses every existing path, including empty files, legacy journals and orphaned SQLite sidecars; it never changes arbitrary parent-directory permissions. Do not use initialization to recover a missing mount or erase pending history.

Normal startup requires both the existing path and matching `journal_lineage`, and refuses missing/empty/wrong-lineage/schema1 state without creating new history. A lineage guard is **not anti-rollback proof**: a historical raw copy of the same lineage can still erase later history if an owner manually substitutes it. No historical activation, migration/adoption or reset operation is supplied. Keep writes stopped and use separately qualified external completion/history evidence before any recovery.

With this configuration, discovery remains read-only by default. Actual settings
writes additionally require **all** of:

- The existing separate `admin.management_token_file` and
  `--allow-experimental-management` ledger-management role opt-in.
- `--allow-experimental-garm-management` for remote settings writes.
- `qualified_admission_routing: true`, acknowledging separately verified installed gateway/provider routing for **all demand these operations can affect**, including the complete repository → matching-pool → provider graph for every managed repository balancer, not merely the listed `pool_ids`. The startup guard rejects obvious mismatches: remote-write endpoint must match inventory endpoint, management providers must be in inventory provider scope, and managed pool UUIDs must have startup facade policies. These checks do not prove the installed routing or patched binary.
- `exclusive_settings_writer: true`, explicitly acknowledging operator-managed
  exclusion of every other Garm UI, CLI and API settings writer.
- A valid, available durable journal with no unresolved pending/unknown operation.

Both acknowledgements are operator assertions, not evidence: neither implements or proves the external settings-writer fence or installed admission-routing graph. Do not enable repository balancing writes until every pool/provider it can select has separately been qualified, including ones outside the console allowlist. Do not
set it for a shared or production installation merely because fixtures pass.
There is no production unblock method after an ambiguous write. Read the
[recovery evidence protocol](garm-management-recovery.md) before any real rollout.

`GET /admin/api/v1/garm` requires the read token and returns scoped sanitized
settings, effective write enablement, settings availability, durable pending
status and the up to 25 newest currently scoped intent records (before/desired/result plus audit reason).
When Garm discovery fails but the journal can be read, status/history still appear
with settings unavailable and writes disabled. History is additionally capped at512KiB with an explicit truncation marker, so large histories cannot crowd pending status out of the bounded response. Reading does not reconcile or
clear pending records. Shared role attribution is not individual operator login.

`POST /admin/api/v1/garm/pool` and `/repository` require the distinct management
bearer token, explicit remote opt-in, TLS and same-origin JSON rules. Bodies are
at most 8192 bytes, reject unknown/duplicate/case-folded/null fields and use an
immutable review UUID as `request_id`, exact `target`, displayed `fingerprint`,
`reason` and either a typed `pool` update or `balancer` string. These routes are
not arbitrary JSON proxies. HTTP contexts are bounded to ten seconds. No cookies,
CORS, redirects or automatic write retries are enabled.

The browser sends only changed settings. Tags are one per line and affect future
runners only; comma-containing labels are preserved. Priority and repository
balancing are not fair scheduling. Lowering minimum idle is blocked; increasing
it can request new runners, which require the independent admission ledger only when installed gateway routing has separately been qualified.
Maximum/idle/priority settings are bounded by the client to 1,000,000. Disabling
new runner demand does not prevent existing idle runners accepting jobs.

A successful remote response plus matching readback is journalled before success
is reported. Reusing a confirmed review UUID with identical input returns the
recorded historical result without a second PUT; altered input is rejected. The success response says `recorded_confirmed`, not that a replay just wrote or observed current state. The UI separately refreshes authoritative settings. An interrupted
or ambiguous response leaves a durable pending/unknown record and blocks later
writes across restarts. The UI also stops further changes in that session and
tells the operator to inspect status rather than blindly retry. A later GET that
matches the desired state is not sufficient to clear the block. No recovery,
backup/restore, journal replacement or history truncation action is exposed in
the browser. Never delete/replace/roll back a journal to bypass unresolved writes.

Tests use disposable authenticated TLS Garm API fixtures, the actual gateway
admin handler, SQLite journal and React assets. They cover real pool/repository
writes, credential separation, immutable review/replay, commit-then-error, restart
blocking, upstream-read outage with durable status, strict decoding and browser
cancel/reconnect. No live infrastructure is contacted.

The separate [disabled pool onboarding foundation](garm-pool-onboarding.md) adds a
strictly default-off typed native-zero creation/journal primitive only. It has no
runtime/HTTP/UI wiring, durable route ACK or activation path; existing console
creation/deletion remains unavailable. It never expands configured pool scopes.
