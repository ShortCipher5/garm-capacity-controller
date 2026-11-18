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
