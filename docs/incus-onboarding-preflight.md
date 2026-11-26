# Read-only Incus onboarding preflight and discovery

This is a real authenticated, audited **read-only transport check**, not server
qualification. Discovery extends the same boundary with read-only host, guest
and utilization inventory for an allowlisted profile, plus an advisory budget
suggestion. Separate from both, **enrollment** is an explicit operator write
that registers a new ledger host; discovery itself never enrolls, binds or
activates anything, and no successful observation is physical-hardware,
whole-project, foreign-project, parent-quota or capacity qualification.

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
- `GET /admin/api/v1/onboarding/profiles/{name}/discovery` runs one read-only
  discovery sweep on demand for an allowlisted profile. It requires the same
  flag-gated management role, same origin and `GET`; the browser cannot supply
  a request ID, endpoint, credentials or any other parameter. The result JSON
  has stable sections: `profile_id`, `observed_at`, `identity` (sanitized
  availability/version/binding class only), `totals` (unknown dimensions are
  omitted, never rendered as confirmed zeros), `guests` (name, status, managed
  flag and controller/pool/reservation attribution; resource limits omitted
  when unbounded from view), `utilization`, `suggested_budget`, `warnings` and
  `blockers`. Every backend call has a fresh 5 second budget; a failed section
  degrades to a section blocker instead of failing the run. A durable
  `onboarding_discovery_requested` audit commits before any transport read and
  a sanitized `onboarding_discovery_completed` audit records only counts and
  classes afterwards.
- `POST /admin/api/v1/hosts` is the explicit enrollment write, gated behind the
  independent management role (same authorization as ledger-policy edits). The
  body is at most 8192 bytes of exact, duplicate-free JSON:
  `{"name", "budget", "detected"?, "mode"}`. It fails with 409 when the host
  already exists: enrollment never silently adopts an existing host. The
  initial `mode` must be `disabled` or `draining`; `enabled` is always
  rejected — activation after qualification and authoritative reconciliation
  is a separate explicit action. When `detected` is present it is recorded as
  the host's last discovery totals. A later totals update checks the freshness
  of its sample and audits the change. Only a completed bounded inventory
  updates placement health and its observation timestamp. Enrolled hosts start in
  no pool and admission stays blocked for them until reconciliation observes them.

## Discovery result semantics

- `suggested_budget` is advisory and computed by one documented helper from
  the detected totals: total minus a fixed headroom reserve of 2 vCPU
  (2000 millicores), 2 GiB RAM (2048 MiB) and 10% of disk, floored at zero per
  dimension so it is never negative. Unknown totals yield a zero suggestion,
  which by itself constrains nothing. Runner count is not derivable from host
  totals and is left zero.
- Warnings always include a staleness note (the result is a point-in-time
  snapshot, never reconciliation evidence), the count of guests without
  controller attribution (`unmanaged guests: the shared limit may not cover
  them`), and, whenever any total dimension is zero/unknown, a detection
  incomplete listing of the unknown dimensions. When the authenticated identity
  cannot be re-validated during a run, a stale-identity warning states that
  previously recorded identity and detection data may be stale. Per-guest
  utilization failures are listed with the guest name and counted as unknown,
  not zero.
- `utilization` is measured guest usage reported as-is with `source:
  guest-state`. Instance-state CPU is cumulative time since guest start, not a
  rate: deriving a usage rate requires two consecutive samples; one sweep is a
  snapshot of cumulative time. The aggregated CPU field is JSON-named
  `cpu_cumulative_millis` precisely so one sweep cannot be misread as a rate.
- `blockers` always include `physical_capacity_scope_unqualified`. Discovery
  is read-only: it cannot register a host by itself, change placement, clear a
  legacy exclusion, release capacity or certify job-safe drain. Pool
  creation/deletion and minimum-idle decreases remain absent.
- `GET /admin/api/v1/overview` now includes per-host `detected` totals when
  discovery recorded them (absent means not discovered; it is display data and
  never an admission input). Live discovery warnings are available from the
  discovery endpoint only; the overview does not persist them.

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
admission state, plus discovery aggregation, unmanaged-guest warnings, suggestion
math, fail-closed malformed inventory, per-call read-budget enforcement,
duplicate-enrollment conflict and the
disabled-only initial enrollment mode. Hosted browser tests exercise the built
React client against that fixture. This is not installed-server, production or
lifecycle qualification. Safe enrollment still needs an independently qualified
full-scope ownership and external-writer protocol, durable registration,
binding/legacy-import rules and reviewed placement integration. Enrollment here
only registers a conservative, initially disabled ledger host; activation,
drain certification and any reset endpoint remain unimplemented.
