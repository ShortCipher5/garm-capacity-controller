# Source-bound JIT preparation receipts: schema8 design

**Experimental schema8 implementation; installed infrastructure remains unqualified.** The zero-agent
preparation proof by itself does NOT fix default JIT normal pools. A later AgentID, status,
CreateAttempt, bootstrap boolean or operator acknowledgement is not original JIT
provenance. Scalesets stay gated until separately qualified.

## Trusted issuer and immutable tuple

The gateway must configure exact pool routes: provider alias, Garm entity type and
ID, forge endpoint NAME, original pool generation (zero is valid), and approved
source client certificate fingerprints for THAT route. The gateway derives its
controller and Garm source-endpoint scope from trusted configuration. A globally
allowed ordinary provider certificate cannot mint for any route. Browser
and ordinary read/write admin credentials cannot mint a receipt.

The `RecordPreparation` request is bounded strict JSON with eleven
required non-null fields, no additional/case-folded/duplicate keys:

- `protocol`: `garm-capacity-preparation/v1`
- `name`, `pool_id`, `instance_id`
- `agent_id`, `generation`, `jit_enabled`
- `provider_name`, `entity_type`, `entity_id`, `forge_endpoint_name`

Only normal-pool mode is initially supported. Row/entity IDs are canonical bounded UUIDs; pool/provider/forge endpoint aliases are exact bounded configured strings, not invented UUIDs;
`instance_id` is a freshly preallocated random UUID. JIT mode requires a positive
agent ID; non-JIT mode requires zero. Generation is exact uint64 bounded by
SQLite's signed integer range, INCLUDING zero. It is not a freshness fence.

A receipt binds this tuple to the ORIGINAL immutable execution plan/request,
backend-binding audit sequence, derived source scope and authenticated route
issuer. First storage and audit are atomic. Receipt, original source identity and
preexisting-history exclusions are immutable. Exact repeats return the original
sequence; changed row, agent, entity, provider, generation or execution identity
conflicts. No receipt may be manufactured for pre-schema8 reservations/history.
No JIT/JWT/configuration/token/credentials or raw upstream errors are persisted,
transmitted to the gateway, returned or logged.

This trusts the installed, reviewed Garm patch to validate its original GitHub
response. It is NOT a cryptographic GitHub attestation or an external writer fence.

## Required source ordering

1. Current authenticated pool route/bootstrap/activation policy is checked before
   pre-JIT admission atomically reserves the full allocation/plan. Known drift
   defers before reservation or JIT; no newer generation is silently adopted.
2. The pinned source performs normal JIT acquisition. Before trusting it, validate
   returned Runner.ID > 0 and Runner.Name exactly equals the requested name. Parse
   the preserved nested `.runner` JSON strictly: exact agentId/agentName must match
   that response, with no duplicate/case-folded/coerced/null safety fields. Do not
   send or log the JIT map. Non-JIT mode is explicit, not inferred from later state.
3. Preallocate the Garm instance UUID. A new INTERNAL `json:"-"` parameter carries
   it into the actual SQL constructor; the existing Gorm hook already preserves
   nonzero IDs. The legacy empty-ID path retains its behavior. Test this with the
   REAL pinned SQL store, not an assumed mock. Do not rely on CreateAttempt, which
   the pinned creation code currently does not copy.
4. Call the bounded optional provider RecordPreparation hook with exact original
   response/row/pool/entity/provider/generation metadata. Persist and acknowledge
   the immutable gateway receipt BEFORE the Garm database row becomes visible.
   This does not grant physical authority or release any allocation.
5. Only a fully validated ACK allows the DB insert using that exact UUID/tuple.
   Timeout, lost/unknown ACK, malformed output, refused issuer or commit uncertainty
   MUST NOT publish a row. Charge/history remain conservative. Exact idempotent
   retry is allowed; a different ID/agent is not. Once ANY Record attempt has started, retain its validated registration and
   ledger charge on refusal/unknown ACK too: a reuse conflict can refute freshness.
   Once SQL publication is attempted, an error may also hide a committed visible
   row. Only before any Record attempt can cleanup of a strictly validated original
   registration remain in scope; invalid/unproven identities are never deleted.
6. Every actual source create/poll/retry path validates the held DB row's exact
   UUID/agent/generation/provider/entity against the receipt before invoking the
   external provider. No cached `prepared=true` flag. The gateway Create boundary
   independently requires the exact persisted receipt/plan/binding so direct RPC
   cannot bypass the source-side guard or substitute a bootstrap JIT boolean.
   Existing one-shot intent still commits BEFORE the single physical POST.

There must never be a visible row before its receipt. Recording after SQL commit
without a separate transactional visibility hold is insufficient: a reconciler
can import the ambiguous row irreversibly during that gap. No holding a different
SQLite database transaction magically makes the two systems atomic.

## Reconciliation and cancellation

A positive-agent preparation is eligible only when the authenticated canonical
Garm row matches the original source-bound UUID, name, pool, agent and generation;
current route authorization and original plan/binding also match. Pending/creating
status is a necessary shape restriction, never proof by itself. Full fresh
post-barrier instance and operation views and atomic no-dispatch/no-cancellation/
no-previous-ambiguous-inventory checks remain mandatory. Reservation charge stays
ONE; no physical or unknown allocation is released or relinked.

Cancellation after receipt ACK but before insert leaves the permanent tombstone.
A later row, duplicate receipt or polling request must not resurrect it. Receipt
creation after cancellation is refused. Timeout after receipt COMMIT but before
ACK retains both immutable receipt and charge; exact retry returns the same record.
Historical backups retain receipts/exclusions and remain runtime-disabled. Raw
same-lineage clones and old issued permits still require external fencing/history
proof. Upgrade does not revoke already-running old binaries.

## Protocol and failure contract

Commands are `RecordPreparation` and `ValidatePreparation` over an isolated
source-leaf mTLS role, with explicit per-route source grants, not ordinary provider
credentials or admin endpoints. The response echoes
the exact request and adds outcome (`recorded`/`validated`), reservation_key,
record_sequence, explicit replay and `dispatched:false`. A validation is NOT a
create permit; only the existing one-shot claim can dispatch. Output is bounded,
strict and accepted only with exit0. Exit75 is NOT a successful preparation ACK.
Raw child output/JIT material must never become logs or propagated error text.

An unknown receipt commit never means no write occurred. Stop row publication;
retain the allocation and require exact retry/recovery. A rolled-back refusal does
not justify changing the identity or retrying physical creation elsewhere.

## Required executable acceptance gates

- REAL pinned normal-pool AddRunner + SQL + mocked authenticated JIT response,
  proving stored preallocated UUID and exact positive original agent/generation.
- Invalid/nil/zero/negative/mismatched original runner name/ID and nested payload
  duplicate/case/null/coercion failures, with zero visible DB rows and zero POSTs.
- Force polling/reconciliation during receipt wait, after receipt commit before
  ACK, immediately before DB commit, immediately after visibility and before
  physical Create. No qualified row is prematurely imported as ambiguous history;
  no missing/unknown ACK publishes a row.
- Barrier/cancel/receipt/claim races in BOTH orders, audit and fsync ambiguity,
  caller cancellation, child timeouts/inherited pipes, crash/reopen, duplicate and
  changed identity/issuer/entity/row replay. At most one physical callback.
- Wrong route certificate, read-only/bearer/admin roles, unknown route and direct
  Create without matching stored receipt fail closed before POST.
- Receipt/plan/binding drift, row delete/recreate, late callback/Force AgentID fill,
  historical inventory, old backup and missing/wrong runtime lineage never mint
  proof. Native physical scope and external old-writer boundaries remain OPEN.
- Scaleset actual SQL currently omits generation and uses a different JIT path.
  Do not relax the scaleset gate or infer generation/attempt values from callers.

The exact patched-source harness, concrete provider protocol and gateway tests
must pass together. Unit parser/Facade fixtures alone do not establish the actual
DB/JIT publication ordering or production readiness.

## Runtime enforcement and remaining qualification (schema 8)

The runtime now requires an original receipt before active-ledger dispatch. The
source certificate has a separate leaf allowlist from ordinary provider RPC; the
same leaf cannot hold both roles. Every pool additionally grants specific source
leaves against a trusted endpoint/controller/provider/entity/forge-name/generation
route. Provider configuration uses separate `source_tls` files for
`RecordPreparation` and `ValidatePreparation`; ordinary Create credentials cannot
mint or validate source receipts. These files are readable by the same configured
Garm OS account, so this is capability separation, **not compromise isolation** or
cryptographic attestation of a GitHub response.

The configured mode is `gateway-owned-default-cloudinit/v1`. The gateway checks
actual authenticated pool metadata for template ID zero and empty extra specs,
then actual row UUID, name, pool, provider, original agent and generation. Unknown
runner status, jobs, provider IDs and scalesets cannot qualify a new Create.
Stored metadata or `jit_enabled=true` is never a substitute for that row read.
The source patch must reject custom templates/options before admission/JIT and
must not silently discard generated bootstrap scripts. Stock v0.2.1 cannot select
this no-template pool through its ordinary create/update API; a narrowly opted-in
provider API extension and actual public-API/SQLite/source-to-gateway tests are
therefore part of the explicit compatibility contract, not stock support.

Original receipts are immutable and charged. New pending-create **or creating**
normal-pool rows can be classified as preparation only with that exact original
receipt, plan and backend binding, no intent/cancellation/prior ambiguous
inventory, and complete fresh post-barrier physical/operation inventories. The
commit transaction rechecks the receipt sequence and row tuple. No dispatch check
is relaxed and no capacity is released. A row or unknown operation previously
imported without provenance remains blocking; later metadata cannot launder it.

Every existing reservation on a schema 7-or-earlier upgrade is excluded from
new source-proof issuance, even when it already has an execution plan. This closes
readiness and invalidates the old fence, without erasing its charge or history.
The authenticated console reports these as `legacy_source_preparations`; there is
no reset/import action. Consistent archive/restore validates canonical original
receipts and exact dispatch references, retaining permanent cancellation history
and runtime-disabled lineage. Copying raw active databases, old credentials,
foreign projects and already-delivered old callbacks remain external gates.

The route scope includes the **entire sorted source allowlist**. Adding/removing a
leaf deliberately changes scope and invalidates old receipts. Exact retry via a
different leaf works only when both leaves were already authorized in an unchanged
route set; this is not transparent post-hoc certificate rotation.

Tests exercise actual SQLite, separate handles/processes, one-shot unknown
 dispatch, rollback/commit uncertainty, source/ordinary/cross-route certificate
 negatives, real TLS RPC, exact Garm metadata parsing, barrier reconciliation and
 archive validation. The mandatory fresh-source gate additionally exercises the
 patched public API, SQL, AddRunner/JIT/helper and compiled provider/Gateway path;
 see [exact executed paths and limitations](../integration/garm-v0.2.1/source-gateway-qualification.md).
 Its mocked JIT/JWT, fixture authentication context and fake native service do
 **not** qualify actual issuers, deployed binaries, native isolation/capacity or
 external writer/job fencing. No live infrastructure has been changed.

Follow-on qualification: actual public repo/organization/enterprise HTTP pool
controllers, Runner and SQLite now exercise the explicitly patched no-template
extension. It is limited to trusted providers with both admission and exact
`capacity_bootstrap_mode`; explicit template zero maps to SQL NULL. Legacy nil
still selects its default, legacy zero remains rejected, valid nonzero templates
retain validation, and caller JSON cannot enable the capability. The source mode
also rejects shell access and non-Linux/unsupported architectures before JIT.
The gateway independently requires actual `enable_shell:false` (missing/null/
aliases are malformed), template zero and empty specs. The compiled complete
source-to-gateway pipeline is a separate mandatory gate; its success is not
inferred from these public-API source fixtures.

Known route/pool drift is now checked before reservation: a prepared runtime
reads the actual configured pool and compares provider/entity/forge/generation,
fixed default bootstrap, shell-disabled policy, image, flavor, Linux and current
architecture. Mismatch or unavailable metadata returns benign defer/exit 75
before `AdmitPlacement`, so repeated known-bad requests do not burn capacity or
proceed to source JIT. The receipt and final dispatch independently repeat their
checks. This is not an upstream CAS: a change after preflight can still cause a
conservative charged hold, never silent generation adoption or receipt rewrite.


Current pool activation is deliberately separate from historical identity.
Canonical disabled pending/creating source rows may remain nonphysical
preparation while fully charged; this does not grant creation. New source
admission/Record/Validate and new native claims require current enabled=true.
A confirmed exact-identity replay may read back the existing verified instance
without another POST. Unknown replay never dispatches. This is neither a
no-new-job fence nor revocation of a callback already delivered before disable.
