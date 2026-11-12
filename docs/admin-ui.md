# Read-only management UI

The React client in `web/` is an experimental **real ledger view**, not a simulator and not a production qualification. It has no synthetic fallback. It reads only the same-origin `GET /admin/api/v1/overview` API on the separate TLS admin listener. It cannot access the provider RPC surface or change infrastructure.

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

Allocations are not measured CPU/RAM utilization. Unallocated capacity is not a guarantee of availability. Unknown and failed resources remain charged. Paused admission does not stop existing idle runners receiving jobs, and a draining mode is not drain completion. Historical usage telemetry, pool priority/label controls and safe retirement/release are not supplied by this UI.

## Verification

```sh
cd web
npm test
npm run test:ui
```

The browser suite builds the production assets and launches a disposable loopback TLS server using the **actual Go admin handler and SQLite store**, with explicitly test-only inventory. It never contacts Garm, Incus, runner infrastructure or any external API. Go 1.25+, CGO and a C compiler are required. Set `GO_BINARY` if `go` is not on PATH. Install Playwright's Chromium using its official installer, or set `CHROMIUM_PATH` to an existing Chromium executable. The fixture generates a fresh in-memory TLS key on every run. Its SHA256 public-key (SPKI) fingerprint is written to an ignored, temporary `.admin-test-spki` file before the server becomes ready and removed at shutdown. The browser trusts only that exact test key using a worker-scoped Chromium SPKI allowlist, including for credentialless fetches. Browser-wide certificate bypass is not enabled, and production application TLS and `credentials: "omit"` remain unchanged. Playwright’s server-readiness probe alone ignores the disposable self-signed certificate.

Browser checks cover real authentication and ledger values, repeated refresh, failed authentication and reconnect, interrupted login cancellation, logout and late-response fencing, failed refresh, page exit, reload and back/forward behavior, empty browser storage, mobile overflow, and desktop/mobile screenshots. The client unit suite separately verifies exact read-only fetch options, malformed payload rejection, disconnect fencing and stale-failure isolation.

These tests are local evidence, not live deployment or infrastructure qualification.
