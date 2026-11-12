# Separate read-only administration surface

This experimental slice adds a **real SQLite ledger view**, not simulated data. It does not add policy writes, runner deletion, release, pool editing or a complete drain workflow. The provider's mTLS scope and startup provisioning flag remain separate.

## Optional configuration

The admin listener is OFF unless the private gateway configuration includes an `admin` object:

```json
{
  "admin": {
    "listen": "127.0.0.1:9444",
    "tls": {
      "ca": "/private/gateway/admin-ca.pem",
      "certificate": "/private/gateway/admin-server.pem",
      "key": "/private/gateway/admin-server.key"
    },
    "token_file": "/private/gateway/admin-read.token",
    "assets": "/trusted/read-only-build/web/dist"
  }
}
```

This is a fragment, not a complete deployable configuration. Endpoints and paths are generic examples. The listener must be an explicit loopback IP/port, uses TLS 1.3, and needs a server certificate covering that exact IP/hostname. Never bypass browser warnings or disable verification in the application. A private CA may require a separate user-approved trust setup.

The token file contains 32 cryptographically random bytes encoded as **64 lowercase hexadecimal characters**, optionally followed by a newline. Generate it with a trusted cryptographic tool into a new owner-only file, never into a repository, URL or command-line argument. Every config/CA/certificate/key/token file must satisfy the owner-only regular-file rules. Use separate OS identities for gateway and Garm, and keep the trusted static build directory unwritable by repository jobs or unrelated users. Empty `assets` gives an API-only listener; no fixture UI is supplied as a fallback.

The UI sends the read token only in an Authorization header and retains it only in tab memory. It is a scoped read credential, not a browser-safe public value. There is no cookie/session auto-login, permissive CORS, URL-token acceptance or provider credential in the UI bundle. File replacement/token rotation takes effect after restart; full OIDC/session management and a reviewed credential-rotation service remain future work. The paired React client is delivered in a separate follow-on slice and does not expand this read-only credential role.

## API and data boundary

Only same-origin `GET /admin/api/v1/overview` is available. It returns global/host/pool budgets and conservative allocations, last-observed health/freshness, admission readiness, experimental startup provisioning enablement, accounted state counts and recent target/mode audit events. It deliberately omits bootstrap, callback URLs, bearer/Incus keys, provider faults, guest userdata, instance names and arbitrary job details.

All counts come from one consistent ledger snapshot. Linked inventory is deduplicated against reservations; unknown/failed/missing work remains charged. These are declared allocations, not measured CPU/RAM utilization, guaranteed available slots, historical usage telemetry or a fresh physical-host probe. The timestamp identifies the served snapshot. Positive readiness does not qualify all physical scopes or production safety.

Each host also includes a non-destructive drain assessment: last-observed allocation counts and explicit blockers. It never grants delete/release permission or infers drain completion from zero rows or a mode. Existing idle runners can still accept jobs. See [job-safe retirement limits](job-safe-retirement.md).

## Read cancellation isolation

Administrative reads use an independent **SQLite `mode=ro`, `query_only` deferred transaction**. A browser disconnect/cancel rolls back only its view and cannot poison the shared admission writer. Expired readiness is displayed as false without modifying metadata/audit; admission/dispatch and reconciliation still enforce and persist their own freshness guards. Closed or poisoned writer state fails the view closed. The conservative ambiguous-write-commit handling is unchanged.

Tests cancel just before a read commit repeatedly, reject writes on the read connection, verify ordinary admission still works, and retain the original ambiguous-commit poison test. The admin token has no policy/provisioning route, even when experimental provider provisioning is enabled.

## Static delivery and shutdown

The public app shell contains no private ledger data. Assets are confined with `os.Root`, exact single-level names/extensions, no-follow/nonblocking opens, regular-file/size checks and no directory listings. API responses and assets use no-store, no-referrer and a restrictive same-origin Content Security Policy. No raw errors, request bodies, token or URL are logged.

The optional admin server shares lifecycle with the provider service but not its authentication role. Startup validates private full JSON configuration before serving; shutdown closes both listeners. Actual TLS lifecycle tests cover unauthenticated versus authenticated admin reads, provider reserve behavior, denied cross-role create attempts, failed authoritative reconciliation and shutdown.

No infrastructure is deployed or modified by this slice. Full mutation authorization, attributable admin writes, safe retirement/release and installed physical-host qualification remain explicit gates.
