# Read-only Incus onboarding preflight

This is a real authenticated, audited **read-only transport check**, not server
enrollment. It cannot register a host, add a ledger budget, bind or activate a
backend, change placement, clear a legacy exclusion, release capacity or certify
job-safe drain. Pool creation/deletion and minimum-idle decreases remain absent.
A successful observation is not physical-hardware, whole-project, foreign-project,
parent-quota or capacity qualification.

## Configuration and authorization

The optional private gateway `admin.incus_onboarding.profiles` map contains 1–32
operator-configured transport references. References use 1–64 ASCII letters,
digits, underscores or hyphens. Use non-sensitive opaque references: these IDs
are disclosed to the admin read role and retained in audit. Each entry has the
same verified HTTPS `endpoint`, explicit `project` and private `tls` file shape
as an Incus transport. For example, the following is a fragment of private
configuration, with illustrative paths only:

```json
{
  "admin": {
    "incus_onboarding": {
      "profiles": {
        "candidate-a": {
          "endpoint": "https://127.0.0.1:8443",
          "project": "candidate-project",
          "tls": {
            "ca": "/private/preflight/ca.pem",
            "certificate": "/private/preflight/client.pem",
            "key": "/private/preflight/client.key"
          }
        }
      }
    }
  }
}
```

This fragment does not replace the mandatory gateway/admin TLS, ledger identity,
read-token or other service configuration. Profiles are separate from enrolled
`hosts` and active pool policy; adding one here never adds an admission candidate.
The browser cannot supply an endpoint, project, path, key or certificate. Only
trusted private configuration chooses outbound targets. Redirects, ambient proxies
and TLS-verification bypasses are not enabled. Use credentials restricted to the
required read capability where the installed server supports it; no credential
provisioning or permission elevation is performed by this feature.

Discovery is available with the separate admin read token but makes no Incus
request. Probe execution is off by default. It requires `--allow-experimental-onboarding-probes`,
the independent `admin.management_token_file`, and an explicit per-action token
submission. It does not enable ledger-policy or Garm writes; those retain their
independent CLI gates. The read token and provider mTLS identity cannot authorize a probe;
the management token does not authorize discovery. Neither credential is an Incus
credential. No automatic startup or polling probe is performed.

## API and evidence boundaries

- `GET /admin/api/v1/onboarding` returns only sorted allowlisted references and
  explicit false enrollment/capacity capabilities, with read authorization.
- `POST /admin/api/v1/onboarding/probe` accepts at most 2048 bytes of exact,
  duplicate-free JSON: `request_id` (canonical lowercase UUID), `profile_id` and
  `reason` (1–256 UTF-8 bytes, no controls or edge whitespace). It requires the
  independent management role, same origin and JSON media type. No cookies or
  cross-origin API are enabled.

A durable `onboarding_probe_requested` audit must commit before the transport
read. A fresh bounded authenticated daemon-identity GET is then attempted; no
Incus mutation method is available to this manager. A separate bounded completion
audit records only request/reference/reason, availability, safe version and an
advisory stored-binding comparison class. Audit failure rejects the result.
Caller cancellation does not erase an already completed read's audit; a crash
can leave only the requested record. Request IDs correlate reads and audit, not
an exactly-once or mutation-replay guarantee. There is no automatic retry.

Results never expose endpoints, project names, local paths, certificates,
fingerprints, raw daemon names or upstream errors. Do not include secrets in the
operator reason. The browser clears its management-token field before sending,
keeps no credential in browser storage and discards observations on disconnect.
A stale/ignored-abort response cannot restore a disconnected session.

`match_observed` warns that a verified transport observation overlaps an existing
stored key/origin/scope/name. `no_match_observed` means only that no such stored
match was seen at the read instant. Neither class proves distinct hardware,
complete host coverage, an independent budget or absence of foreign workloads.
These observations are not persisted as bindings or reused as reconciliation
fences. Admission readiness, reservations, resource charges, desired modes,
immutable legacy exclusions and pool policy remain unchanged; only audit grows.

## Qualification

Offline tests use fresh disposable mTLS servers and the actual SQLite/admin
handler. They cover separate roles/flags, strict input rejection before network,
audit-before-read, cancellation, collision warnings, sanitized output and unchanged
admission state. Hosted browser tests exercise the built React client against
that fixture. This is not installed-server, production or lifecycle qualification.
Safe enrollment still needs an independently qualified full-scope ownership and
external-writer protocol, durable registration, binding/legacy-import rules and
reviewed placement integration. There is no enrollment/activation/reset endpoint.
