# Dynamic reviewed Incus connections

This implements a bounded connection lifecycle, not SSH installation or a new
credential vault. It uses the existing private-file mTLS loader and explicit
server SPKI pin. All network tests use disposable fixtures; no live server,
credential or trust configuration is created by this patch.

## Deployment-owned authority

`admin.incus_onboarding.connection_targets` maps credential aliases to existing
trusted configuration: exact HTTPS literal-IP endpoint with explicit port,
project, owner-only TLS file references and optional required `server_key_sha256`.
There are at most 32 targets. The operator must provision these existing files
and authorize each destination independently. For example, a documentation-only
entry could use `https://192.0.2.10:8443`, project `runners` and alias `build-host`.
No arbitrary DNS address, proxy, redirect, userinfo, path, zone, browser-selected
credential path, shell command or SSH password is accepted. Configured endpoints
are literal IPs so the destination cannot change via DNS rebinding. Normal TLS
IP-SAN validation and the separately reviewed public-key pin both apply.

The existing management and onboarding-probe flags/roles are required. Static
profiles keep their existing behavior. Up to 32 dynamic connection records may
coexist with 32 static profiles; IDs may not collide. Revoked records are durable
tombstones and can be revised into a new draft, not silently replaced.

## Operator flow in Servers

A collapsible section on the existing Servers page lets the operator load the
approved destination catalogue and current records, save/edit a connection draft,
review its exact public-key fingerprint separately, approve it, or revoke it
locally. No additional settings page or credential material is introduced.

Saving a draft never connects. Approval requires the current revision and exact
expected key, but is still only operator permission to perform later discovery,
not proof that the key was independently checked or that a server is connected.
The UI requires an explicit confirmation of independent verification. An
approved record appears among discovery profiles; existing discovery and paused
enrollment flows then apply. No capacity/physical-host authority is granted.

Every request uses the management token transiently and clears it afterward.
Writes use optimistic revision checks. Conflicts and uncertain responses require
reload/review, never automatic retry. Approval, editing and revocation invalidate
older discovery reviews. Unknown local network outcomes are not reported as
successful state changes.

## Persistence and runtime enforcement

Schema v14 follows v13 discovery reviews. SQLite stores nonsecret connection
intent, monotonically increasing revision and `draft`, `approved` or `revoked`
state. Writes, audit and old-review invalidation share one BEGIN IMMEDIATE
transaction. Editing always returns to draft, including an approved record.

The connection service checks the current deployment target policy on each
resolution and constructs a fresh pinned transport from the current credential
file reference. There is no cached authority from the browser or a previous
approval. Revising/revoking serializes with resolution, cancels active read-only
contexts, and prevents new resolution. A late result must match the still-approved
connection revision in the same transaction that records its discovery review.
Every gateway startup invalidates dynamic reviews, including when all targets
are removed or onboarding is disabled. Connection intents remain durable across
restart, but stale deployment-policy observations cannot be reused.

A failed or missing local observation is unknown, not capacity available.
Profiles never activate hosts, free reservations, install software, or grant
provider access. Existing target credentials may possess broader Incus rights;
this service uses only the read-only observer surface. That is not proof the
credential itself has read-only permissions.

## Revocation boundary and remaining code

Local revocation stops this controller's future discovery and invalidates its
reviews. It does not remove Incus trust remotely, revoke other copies of the
certificate, terminate runners or retire capacity. Remote credential revocation
must be implemented and verified separately, with failure reported honestly.

Still outstanding: guided SSH bootstrap and independently approved installation
plans, credential creation/rotation/vault integration, remote trust revocation,
whole-host identity/authority qualification, provisioning activation and actual
allocation-plan execution. Arbitrary new addresses cannot be enrolled until a
deployment operator authorizes a corresponding exact destination/credential
reference. This deliberate boundary is not advertised as complete one-click
server provisioning.
