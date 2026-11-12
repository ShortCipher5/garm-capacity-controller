# Ledger management UI

The React client in `web/` is an experimental **real ledger view**, not a simulator and not a production qualification. It has no synthetic fallback. It reads the same-origin `GET /admin/api/v1/overview` API on the separate TLS admin listener. An explicitly enabled separate management role can edit bounded ledger policy. It cannot access the provider RPC surface or mutate Garm/Incus infrastructure.

## Build

Requires Node 22.12+ (or a supported newer version) and npm:

```sh
cd web
npm ci
npm test
npm run build
```

Set the gateway's optional admin asset directory to the absolute path of `web/dist`. See the backend admin configuration documentation for listener, TLS and read-token setup. The built page uses self-hosted JavaScript and CSS only; it works with the admin handler's strict Content Security Policy. No external fonts, analytics, CDN scripts or inline scripts are required; no provider credentials are bundled. There is no standalone demo server or external backend URL setting. The Vite development server alone does not supply the admin API.

## Connect and disconnect

Enter the admin service's 64-character lowercase hexadecimal read token. It grants only read access to this admin API, not provisioning permission. The client sends it in the Authorization header, never in a URL, cookie, localStorage or sessionStorage. The input clears immediately on submission. The token and rendered ledger data are discarded on disconnect, cancel, request failure, rejected authentication, or page exit. Reloading requires reconnection. The browser or a password manager may have its own behavior; client-side memory is not an operating-system secret vault.

Requests are same-origin, omit credentials, disallow redirects, and disable caching. Only one current request can update the screen. Cancellation, disconnect and reconnection fence late responses so an interrupted request cannot reopen an old session. There is no automatic retry, polling, token persistence or background data collection.

Refresh obtains another snapshot without changing the ledger. The displayed timestamp identifies the snapshot's server time. Host health and freshness reflect the stored observation, not a new physical-host probe. Admission readiness and experimental provisioning enablement are separate indicators, not a certification of operational safety.

## What is shown

- Global, host and pool allocations versus declared budgets in millicores, MiB and runner counts.
- Configured host mode, last observed health, observation freshness and whether new claims are paused.
- Conservative accounted resource-state counts and ledger audit events, newest first.
- Explicit empty states when the real ledger has no hosts, pools, resources or audit records.

Allocations are not measured CPU/RAM utilization. Unallocated capacity is not a guarantee of availability. Unknown and failed resources remain charged. Paused admission does not stop existing idle runners receiving jobs, and a draining mode is not drain completion. Historical usage telemetry and safe retirement/release are not supplied by this UI. Separately configured experimental Garm settings controls are documented below.

## Verification

```sh
cd web
npm test
npm run test:ui
```

The browser suite builds the production assets and launches a disposable loopback TLS server using the **actual Go admin handler and SQLite store**, with explicitly test-only inventory. It never contacts Garm, Incus, runner infrastructure or any external API. Go 1.25+, CGO and a C compiler are required. Set `GO_BINARY` if `go` is not on PATH. Install Playwright's Chromium using its official installer, or set `CHROMIUM_PATH` to an existing Chromium executable. The fixture generates a fresh in-memory TLS key on every run. Its SHA256 public-key (SPKI) fingerprint is written to an ignored, temporary `.admin-test-spki` file before the server becomes ready and removed at shutdown. The browser trusts only that exact test key using a worker-scoped Chromium SPKI allowlist, including for credentialless fetches. Browser-wide certificate bypass is not enabled, and production application TLS and `credentials: "omit"` remain unchanged. Playwright’s server-readiness probe alone ignores the disposable self-signed certificate.

Browser checks cover real authentication and ledger values, repeated refresh, failed authentication and reconnect, interrupted login cancellation, logout and late-response fencing, failed refresh, page exit, reload and back/forward behavior, empty browser storage, mobile overflow, and desktop/mobile screenshots. The client unit suite separately verifies exact read-only fetch options, malformed payload rejection, disconnect fencing and stale-failure isolation.

These tests are local evidence, not live deployment or infrastructure qualification.

## Experimental ledger policy editor

Management is **off by default**. Supply a separate owner-only `admin.management_token_file` containing an independently generated 32-byte random lowercase-hex token, and start the gateway with `--allow-experimental-management`. Both are required; a file without the flag, a flag without the file, or reuse of the read token fails startup. Provider mTLS identities and the read token never authorize these writes. The management token does not implicitly grant the read role. No Garm/Incus credential belongs in either browser field.

After connecting with the read token, the policy section offers host enabled/disabled mode, global budget, host budget and existing ledger-pool budget. Review a concrete change and an audit reason, then enter the separate management token to apply it. The token input clears on submission; each action requires it again. Cancel clears the review without submitting. Disconnect/page exit removes the form, clears credentials and fences late responses. An interrupted request may already have committed: reconnect and inspect policy/audit rather than assuming cancellation undoes it.

`POST /admin/api/v1/policy` accepts at most 8192 bytes of strict JSON (no unknown/duplicate fields). It requires TLS, the distinct bearer token, JSON media type and same-origin browser requests, with no cookie auth or CORS. Its bounded fields are `revision`, `action`, `target`, optional `budget` or `mode`, and `reason` (1–256 non-control bytes). Actions are `global_budget` (empty target), `host_budget`, `pool_budget`, and `host_mode` (`enabled` or `disabled`). Resource budgets are nonnegative integers no larger than JavaScript's maximum safe integer. The overview supplies `policy_revision` and `management_enabled`.

The revision hashes managed policy, excluding changing health observations. Comparison, policy edit, readiness/fence invalidation and durable audit happen in one `BEGIN IMMEDIATE` transaction, serialized with admission. A stale policy returns 409, without a write. Effective edits close readiness until fresh authoritative reconciliation; a no-op preserves the current fence. Audit records include the management role (not a per-person identity), prior affected value, concrete edit and reason. Do not put credentials or sensitive data in reasons. Existing charges are never released, including when budgets fall below allocation. The recent audit view is bounded to 100 events, not a retention/export policy.

**Disabled is not drained.** Existing idle runners may still receive jobs, and a later mode change does not fence an already committed in-flight claim. Enabling is not immediate admission: it requires fresh reconciliation. These controls do not register hosts/repositories/pools, edit Garm tags/idle/max/priority, alter actual runner placement, stop jobs or retire resources. Pool host eligibility is deliberately not editable because the gateway pins placement at startup. Unknown write outcomes are reported explicitly, never automatically retried.

The disposable browser suite also covers review/cancel, read-token write rejection, actual persisted mode and budget changes, audit/readiness effects, repeated apply and disconnect during an interrupted write. Passing it does not qualify production management, individual operator attribution, rotation or audit retention.

## Existing Garm pool and repository settings

When separately configured, **Load Garm settings** reads exact allowlisted real
Garm objects using a server-held dedicated upstream token. These are not ledger
pool IDs or dynamic-placement controls. The existing read token discovers settings;
the separate browser management token authorizes each reviewed write. Remote
writes require their own additional explicit startup flag. Review only changed
settings, then enter the write token for that one action. Cancel clears the review
without sending it. No secrets are stored in browser storage or build variables.

The console supports future-runner tags, pool priority, maximum and non-decreasing
minimum idle, pool enabled state, and repository round-robin/pack balancing. It
cannot register pools/repositories, choose an unrestricted provider, change actual
runner placement, drain jobs or retire runners. Garm's lack of external atomic
conditional updates is shown prominently. Every mutation traverses the separate
durable intent journal. Pending/unknown writes block new changes even after a
restart; the console has no blind clear/retry or recovery button. Status and the
up to25 recent scoped sanitized operation records remain visible when the upstream service is
unavailable, provided the journal itself can be read. See the full
[configuration, concurrency and recovery boundaries](garm-management.md).

## Backend binding status (schema4 prerequisite)

The host view exposes only `backend_binding_status`: `unbound`,
`bound_observation`, or `legacy_scope_import_required`. A bound observation is an
administered daemon identity observation, not physical-hardware attestation,
whole-host inventory, parent-quota qualification or production readiness. Raw
fingerprints and daemon/server names are not returned in this status field.

A legacy scope banner means a previously charged host is excluded from automatic
binding, even if later cancellation leaves no current charges. Qualified legacy
scope import is a separate workflow and is not implemented. Mode/budget controls
remain distinct; there is no clear/reset/import button. Enabling host mode cannot
erase historical exclusions or qualify backend capacity. See the
[dynamic management design gaps](dynamic-management-design.md).
