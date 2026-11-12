# Original-source preparation receipts, pinned normal pools

Status: offline source implementation and disposable fixtures only. Not deployed,
not production-qualified, and not a whole-host/external-writer fence. Requires the
matching separately reviewed gateway/schema/protocol implementation; this source
patch cannot safely operate with the old provider executable. No live installation
or migration is authorized here. Scale-set provisioning is explicitly blocked
when this receipt mode is enabled, including existing pending scale-set rows.

## Why the zero-agent exception was not a general JIT fix

Pinned normal-pool `AddRunner` obtains JIT configuration and copies returned
`Runner.ID` into the database row BEFORE the asynchronous provider poll/Create.
That normal JIT row has a positive agent ID. Scale sets also preallocate a positive
JIT ID before publishing their row. Thus the earlier zero-agent/pending marker
covers only non-JIT preparation, not normal JIT operation or scale sets. Never
loosen it to accept any positive agent ID or infer JIT origin from state/name.
Normal SQL rejects changing one established positive agent ID to a different one,
but permits zero-to-positive callback updates; privileged force updates and
same-name recreation also exist. An API row alone is not original-source proof.
Actual SQL initially stores `CreateAttempt=0`, despite callers requesting 1.
Exact receipt validation does not depend on that counter: a forced same-row
pending requeue with a changed counter must still consult the gateway receipt
and no-dispatch/one-shot authority.
This patch does not change or infer provenance from that counter.

## Source ordering and correlation

With explicit external `admission_enabled=true` AND
`capacity_bootstrap_mode="gateway-owned-default-cloudinit/v1"`:

0. Require Linux, supported architecture, no selected Garm template, no shell mode
   and empty/`{}` extra specs BEFORE admission/JIT. Only this explicit mode skips
   Garm wrapper injection and uses fixed gateway-owned cloud-init. Custom templates
   are rejected, never silently ignored. The gateway still rejects custom scripts.
   Public pool enrollment must explicitly request `template_id:0`. The reviewed
   pinned API extension permits it only for this exact configured provider
   capability and maps zero to actual SQL NULL, not foreign key zero. Legacy
   providers retain nil/default/nonzero behavior and cannot use zero. Updates use
   an internal `json:"-"` flag derived by the API policy, never a user-set bypass;
   removing an existing selected template increments generation. This is a source
   extension, not an existing stock Garm mechanism.

1. Reserve the generated normal-pool name before requesting JIT, as before.
2. For configured JIT, validate original returned runner ID is positive, returned
   name equals that generated name, and base64 `.runner` JSON has exactly matching
   nonsecret `agentId`/`agentName`. Duplicate/case-ambiguous fields and malformed
   contents fail closed. Strict validation also runs inside the actual GitHub
   getter before its cleanup defer can delete an unproven/unrelated ID. Legacy
   providers keep their previous getter behavior.
3. Generate a random v4 database row UUID BEFORE SQL insertion. Send bounded
   `RecordPreparation` metadata and receive its exact durable acknowledgement
   BEFORE `Store.CreateInstance` can publish the row. Metadata comprises protocol,
   name, pool ID, row UUID, original agent ID, generation (zero is valid), provider,
   entity type/ID, configured forge endpoint NAME, and explicit JIT enabled state.
   Garm v0.2.1 has no forge endpoint UUID. Cross-check entity and pool endpoints;
   do not fabricate one. No JIT configuration, JWT, bootstrap token or secret is
   transmitted in this metadata.
4. SQL copies the private `CreateInstanceParams.AdmissionInstanceID` into the row
   only when canonical random-v4 UUID validation succeeds. The field is `json:"-"`:
   public API JSON cannot choose it. Empty legacy values retain generated IDs.
5. Every pending poll validates the exact current original receipt BEFORE changing
   pending to creating. The common normal-pool create helper validates at entry
   and re-reads/revalidates the exact row immediately before provider Create.
   Direct helper calls and forced requeues cannot bypass the receipt lookup.
   Validation is not a dispatch permit; the gateway must still enforce its own
   immutable plan/binding/receipt and one-shot dispatch journal atomically.

The issuer is the installed patched Garm source through its explicitly configured
and authenticated receipt route, not GitHub cryptographic attestation. The gateway
must independently constrain the issuer certificate, exact Garm endpoint, provider,
entity and forge endpoint name. Trusted Garm configuration/administration and
external writer fencing remain assumptions; a provider boolean or unauthenticated
receipt cannot establish them. The browser and ordinary provider-create role must
not acquire original-receipt issuance authority.

## Failure behavior

Unknown, malformed, timeout or nonzero Record ACK: no SQL attempt/visible row, no
physical Create and NO automatic ledger cancellation after Record was attempted.
A receipt may have durably committed despite its lost reply, and an explicit
refusal may identify a reused agent/row identity. Retain the GitHub registration
as well as ledger charge once ANY RecordPreparation attempt begins: do not delete
a possibly reused original ID on refusal/unknown ACK. Before Record was ever
attempted, only strictly validated original registrations may be cleaned up within
the established scope. Invalid original JIT identity never authorizes deletion.

ANY attempted SQL insertion may commit before its error/lost acknowledgement.
Therefore suppress both GitHub registration cleanup and ledger cancellation after
publication is attempted. If an exact row became visible, a fresh independent poll
can validate its already-durable original receipt without obtaining fresh JIT.
If no row exists, reservation/receipt remain conservative, not proof of release.
There is no automatic replay of AddRunner with a fresh UUID or fresh JIT.

Validation refusal/unknown ACK holds the existing row without changing pending to
creating. A later refusal inside the create helper does not enter stock error/
provider-delete cleanup. Opted-in failed-attempt retry/cleanup is held entirely;
no reissue or lifecycle safety is inferred from an error-to-pending transition.
Existing native uncertainty, busy jobs, DELETE/retirement and missing-history
recovery remain separate gates. Source lookup success alone cannot release charge
or authorize a second backend request after a previously delivered dispatch.

## Actual disposable tests and remaining integration gate

`preparation-test.sh NEW-DISPOSABLE-DIRECTORY` fetches only pinned public source,
applies this exact patch, checks formatting and runs real upstream code with local
fixtures. It never contacts a real GitHub Actions/Garm/Incus service.

- Real normal-pool `AddRunner` plus real SQLite: pause Record before ACK, verify
  zero visible rows and zero poll Creates; then exact preallocated UUID/agent ID
  survives publication and original poll Create occurs once.
- Actual commit followed by a held/lost SQL ACK: poll the visible original row,
  retain original registration and reservation; no fresh JIT or cleanup inference.
- Unknown Record ACK leaves no row/cancellation; wrong or recreated UUID under the
  same name/agent/JIT cannot validate; early and immediately-preCreate validation
  refusal make zero provider Creates/deletes.
- Real GitHub client HTTP decoding with disposable `httptest` service: zero,
  mismatched name/ID/payload, duplicate/case-ambiguous file/agent fields, malformed
  base64 and nil return structures cannot publish proof or DELETE an unrelated ID.
  This tests source validation, not authenticity of a real GitHub registration.
- Actual public HTTP controllers plus real Runner/SQLite test explicit zero
  enrollment, SQL NULL, migration through an explicit public update after trusted
  provider configuration changes, legacy nil/default/nonzero preservation,
  unsupported provider/selected template/custom options rejection before writes,
  and ignored forged internal-clear JSON. The HTTP tests use a fixture admin
  context; authentication middleware is not claimed by those tests.
- Actual SQL validates/preserves the private UUID, rejects collisions/invalid IDs
  and ignores attempts to set the private field through JSON.
- Opted scale-set scale-up, existing-row poll and direct worker Create remain held.

The in-process source receipt fixture in these tests verifies ordering and identity
correlation. It is NOT the real gateway receipt ledger or a physical side effect.
The separately maintained compiled-provider/gateway wire qualification must be
updated and pass with this source AND schema/issuer/dispatch implementation before
claiming the complete integration works. Live canaries, installed binary/image
behavior, external authority, hardware bounds and job-safe retirement remain OPEN.
