# Security posture

This repository is experimental. Do not expose a simulator or future admin API publicly, provide production credentials, or use it to manage untrusted workloads yet.

## Required before a live integration

- Admin authentication (OIDC or another reviewed mechanism), authorization on every operation, short sessions, CSRF protection for cookie auth, TLS, and secure cookie policy. Loopback binding is not a substitute for authentication.
- Separate browser sessions, controller credentials and provider admission credentials. Do not return backend credentials to the browser.
- Read secrets from restricted files / a secret manager. Encrypt stored credentials with a key outside the database; document rotation and backup recovery. Never put secrets in URLs, audit records, errors, fixtures or logs.
- Verify Incus TLS peers and use narrowly scoped projects/certificates. An Incus admin identity is effectively host-level access. Never disable certificate validation to make a connection work.
- Configure Garm/Incus endpoints through trusted administration, not job payloads. Prevent SSRF, redirects to unapproved endpoints and access to metadata services.
- Treat repository code and runner images as untrusted. Isolate tenants and prevent access to host sockets or controller credentials.
- Append attributable policy/admission/drain/reconciliation audit events with retention, access control and redaction. Historical usage is not a safe place for raw job payloads.
- Backup/restore tests and fail-closed recovery after ambiguous commits, stale inventory and split-brain attempts.

The ledger contains allocation identifiers and reservation/audit history, not bootstrap payloads, Incus keys or Garm bearer tokens. Treat real allocation identities as private operational data. File locking assumes a trusted local filesystem and a private parent directory. It is not designed to resist another local user who can replace the ledger or lock file. Run under a dedicated account with restrictive directory permissions.

Do not post sensitive vulnerability details, credentials, or private infrastructure in public issues. Use GitHub private vulnerability reporting if enabled; otherwise ask the maintainer for a private reporting route first.

The experimental provider-only RPC boundary uses TLS 1.3, verified client chains plus an exact leaf allowlist, private owner-only credential files, bounded strict decoding and constant redacted error events. See [process/credential separation](docs/gateway-service.md). These controls do not supply the production-grade browser authentication (the experimental admin uses separate static read/write tokens), individual operator attribution, certificate rotation/revocation, complete audit-retention policy or production qualification.
