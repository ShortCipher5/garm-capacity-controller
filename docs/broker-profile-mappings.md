# Immutable broker profile mappings

Schema 20 records configuration mappings, not production activation authority.

stockidentity.Observe performs a verified, scoped GET against the configured
stock GARM pool. Its opaque result supplies repository ownership, provider and
image/flavor/OS fields. A caller cannot mint a valid observation by constructing
JSON. The stable ownership fingerprint excludes enabled/max/min/priority,
generation and tags so ordinary sizing changes do not require re-enrollment.

The source identity binds the configured normalized HTTPS API endpoint, not a
secret token. Stock GARM supplies no controller ID: endpoint-to-controller
association remains trusted operator configuration, not independently
authenticated controller provenance.

RegisterBrokerPoolProfile uses:
- that fresh observed stock ownership and matching source/provider;
- the saved size profile, read in the same SQLite transaction;
- exact execution fields accepted by each immutable native policy compiler;
- enrolled native domain records whose observed policy digests match those
  policy objects, including current independent host binding sequences;
- a distinct expected stock-provider leaf, never a native owner role leaf.

Registration records append-only revisions under expected-previous-sequence CAS.
Exact retries of the same facts return the existing record; changing the saved
size requires a new revision. Never re-register on every scheduler poll.
Historical charges and grants are not rewritten or released by profile edits.

BrokerGrantState.PoolProfiles exports the latest revisions in the same
transaction as policy, grants, charges and host bindings. The runtime must still
compare fresh stock source/ownership fingerprints and current host bindings.
The grant batch's existing expected ledger sequence detects intervening mapping
or policy changes. DemandObservation.SourceIdentity() is transport-minted and
is intentionally lost on generic JSON serialization; cached JSON is not a fresh
authenticated source observation.

This does not activate the native path, prove exclusive physical authority,
issue grants, change pool settings, or certify remote eligibility after the read.
The existing runtime refusal remains. Backup/restore preserves revision history
and audit linkage while restored readiness stays closed.

## Local operator registration

The concrete entry point is:
capacity-ledger register-pool-profile --source EXISTING_LEDGER --identity-file PRIVATE_IDENTITY --config PRIVATE_REGISTRATION_JSON

The private configuration contains endpoint, ca_file, token_file, request
(BrokerPoolProfileRequest), and domains entries with enrollment sequence and the
complete native PolicySpec. All policies are compiled before networking; the
existing runtime ledger identity is verified before the scoped stock read.
If request.SourceIdentity is omitted, the authenticated configured transport
supplies it. A specified expected source must match exactly. The command then
performs real ownership observation and records the mapping transactionally.
No activate option exists; successful output reports only the immutable revision.

## Existing console registration

The Pools section now shows immutable mappings alongside budgets and placement.
Repository weight (default 1), pool warm-idle and caps remain in the existing
Repository policy editor. Runner-size editing remains in Profiles; host discovery
headroom and editable runner mix remain in Servers. No duplicate pages are added.

An operator may set `admin.broker_profile_recipes_file` to a private JSON array.
Each entry uses the CLI registration shape (`endpoint`, `ca_file`, `token_file`,
`request`, `domains`) plus a stable `id`. Native policies and enrolled sequence
IDs are deployment-owned. Both the independent management credential and
`--allow-experimental-management` are required. Startup validates recipes without
network calls. Browser users cannot submit endpoints, provider credentials,
native policy bodies or arbitrary enrollment IDs.

`GET /admin/api/v1/broker-profiles` uses the read role and returns sanitized current
mapping sizes/hosts/revisions plus configured recipe IDs/pools. It does not expose
TLS fingerprints, owner policy details, transport addresses or upstream tokens.
`POST` requires the separate management role, same origin, strict bounded JSON,
a configured recipe ID, saved size name, expected previous mapping sequence and
reviewed resources. It performs a fresh verified stock ownership read, then calls
the authoritative transactional registration. Resources are compared with the
saved size inside that same transaction, so an intervening size edit cannot
silently change the reviewed allocation. Registration records its existing durable
audit event and never changes grants, native state or GARM pool settings.

The console never automatically retries uncertain writes. Reload before another
edit. Editing/deleting a saved size does not alter an existing mapping's captured
resources; adopting new resources requires a new mapping revision. A zero-disk
legacy profile cannot be selected for bounded native registration.

These workflows do not establish native exclusivity or production readiness.
Endpoint-to-controller association remains trusted operator configuration, not a
controller identity asserted by stock GARM or a browser user.
