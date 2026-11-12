# Inactive pool enrollment and durable route catalog (design, schema9)

Not implemented or an activation authorization. Source8 normal-pool preparation
is the prerequisite, not a dynamic enrollment API. No live changes, server
onboarding, pool deletion, capacity release or job-safe drain are covered here.

## Goal and invariant

Create a Garm repository pool through its public API **disabled**, min-idle zero,
trusted bounded max>=1 and explicit native template zero. Stock v0.2.1 rejects
max zero; never use that as a staging trick. The patched provider capability,
Linux/architecture, empty extras, shell-disabled and fixed-bootstrap contract must
match. Capture the actual generated UUID in the bound mutation journal, then
install a durable INACTIVE controller route. Registration grants no admission.
Only a separately coordinated known enable outcome plus core activation can open
new runner creation. Existing jobs/idle runners are not fenced by these controls.

No raw StagedPool JSON, `disabled_readback:true`, recipe hash or opaque-reference
string supplied by a browser is authority. A trusted coordinator must look up the
exact confirmed request in the live independently bound management journal and
freshly read the actual disabled Garm pool. It must compare UUID, repository,
provider, exact created spec/generation and immutable trusted-template revision.
GET similarity never resolves an unknown POST. Lost ACK, uncertain journal commit
or unavailable source keeps the route absent/inactive and the mutation blocked.

## Split ownership and trusted catalogs

- The management service owns public create, remote intent/known-response history
  and explicit user authorization; it never mutates provider client allowlists.
- The admission store owns atomic pool budget/routing/audit insertion and current
  activation. The gateway resolves immutable snapshots, not mutable shared maps.
- Operator-only startup catalogs fix execution profile, candidate HOST GROUP,
  provider/source endpoint, allowed entity IDs and SOURCE ACTOR GROUP references.
  Caller input may select authorized references, not URLs, keys, scripts, arbitrary
  profiles, new hosts, new issuer leaves or caller-provided execution settings.
- Actual pool UUID and generation come from the journal plus authenticated source,
  never a guessed/requested ID. Creation-body hash and historical full spec remain
  preserved; activation is not an excuse to reinterpret them.

Profiles include pinned image fingerprint, image/flavor, CPU/RAM, Linux/arch,
container type and ordered profile NAMES. Names are not immutable native profile
contents or isolation proof. Candidate hosts must already be enrolled/bound; this
workflow cannot register a new daemon or bless hardware/foreign-project scope.

## Proposed coordinator contract (not shipped)

`RegisterInactivePoolRoute(ctx, trustedRegistration, expectedPolicyRevision)`:

1. Resolve the exact confirmed bound-journal request and fresh disabled pool
   outside the ledger write transaction, using only the fixed qualified source.
2. Resolve and compare trusted catalog references/revisions. Reject mismatched,
   missing, legacy, duplicate or already differently registered identities.
3. In ONE BEGIN IMMEDIATE transaction, recheck expected ledger policy revision,
   request/pool uniqueness, already-bound candidate identities and safe integers;
   insert canonical immutable route identity plus ledger pool budget, audit and
   idempotency history; close readiness and supersede its nonce.
4. Return immutable route identity sequence, current activation revision and
   replay classification. Exact request retry returns the same identity, never a
   second pool, budget change, activation or new remote POST. Different content or
   a different request for the same remote UUID conflicts. Audit failure rolls all
   insertions back. Ambiguous commit fails closed and keeps the operation held.

The private registration frame is produced by the trusted coordinator, not an
HTTP proof payload. Its nonsecret history references management identity, journal
lineage, request ID, pool/repository/provider UUIDs, actual generation/spec hash,
trusted profile/host/source-group references and resolved digests. The ledger
transaction cannot make two SQLite databases and a remote service atomic; INACTIVE
is why an interrupted registration cannot authorize native work.

## Identity versus activation

The route identity sequence is immutable. Its full plan and reference digests
must survive restart, backup and cancellation, never disappear because a startup
file omits a reference. A separate activation state/revision can change without
rewriting original plans/receipts. Missing or changed startup references refuse
new claims while preserving historical read/accounting data; no re-bootstrap,
automatic import, actor grant expansion or reinterpretation.

- **Enable:** keep local route inactive while the journaled remote enable is
  attempted. A lost/unknown outcome leaves local inactive even if Garm enabled it.
  After known ACK, durable journal confirmation and exact fresh readback, atomically
  CAS core activation and close readiness. A NEW complete reconciliation is needed
  before admission. Early Garm demand sees benign defer, zero JIT/reservations.
- **Disable:** atomically make local inactive and close readiness FIRST, then
  attempt journaled Garm disable. Failure/unknown remote state cannot reopen it.
  This pauses NEW runner creation, not job assignment to existing idle runners.
- **Replay:** exact confirmed readback may still return an already verified guest.
  Unknown/native intent never redispatches, fails over or releases a charge.

Any known remote-policy drift repeats source8's early preflight refusal. A change
AFTER preflight is not covered by remote CAS and can conservatively hold a charge.
A later state read cannot clear an unresolved old remote mutation handler.

## Runtime and transaction integration

All resolution must use immutable snapshots or request-local store reads, never
unsynchronized edits of Facade.pools/preparationRoutes. Unknown pool IDs cannot
fall back to startup defaults. Registered inactive routes remain discoverable for
read/accounting, but cannot authorize Record/Validate/Admit/new native claims.

Selection, reservation, immutable execution plan, original route identity and
backend binding are compared in the SAME ledger admission transaction. Final
prepared dispatch rechecks current ACTIVE plus immutable identity atomically;
metadata acknowledgement or a cached active boolean is not permission. Route
updates cannot move an existing reservation to another host or execution profile.
Disabled canonical source placeholders remain fully charged and classifiable;
prior ambiguous/physical inventory is never laundered by re-enrollment.

New schema9 dispatch columns/triggers must also reject an older binary attempting
a new dynamic-route grant without its original route provenance. This is defense
in depth, not revocation of already-delivered permits/old credentials. External
writer fencing is still required. Existing source8 static plans remain historical
authority, not automatically registered/imported dynamic routes.

## Required tests before enabling a control

- Actual patched public API accepts disabled/min0/max>=1/template0; stock zero
  and caller-injected private capability flags refuse. No direct SQL enrollment.
- Lost POST ACK, committed journal capture with lost response, process restart,
  failed readback and journal lineage replacement never cause retry/activation.
- Two handles/processes registering same/different IDs, global/pool contention,
  all-or-nothing audit failure, no budget-charge release and exact idempotence.
- Source poll in every create/register/enable interval: inactive/deferred means
  zero JIT/rows/native writes; known activation then full reconciliation creates
  only the original assigned plan once under aggregate budgets.
- Disable/claim races in both orders, unknown physical create never moves host;
  old delivered callback remains explicitly an external fence counterexample.
- Restart/backup/restore preserve immutable catalog/plan/binding/source provenance;
  archives remain runtime-disabled. Missing/mismatched private refs fail closed
  without hiding old allocations, overwriting history or inventing new grants.
- Separate source/provider/admin roles and exact route/entity/template scopes;
  no arbitrary endpoint, key, script, host activation, reset/import/delete route.
- Real browser interrupted/repeated forms, invalid/stale CAS, unknown remote write
  and disconnect behavior must show INACTIVE/route-required honestly.

Physical authority/native common-parent hard caps, hidden projects, independent
ledgers, real issuer/job fences, exact patched image/UID/isolation and destructive
retirement remain separate deployment gates. Catalog correctness does not close
any of them.

## Current persistence checkpoint

Schema9 installs only the inactive route table and immutable identity guards.
Upgrading closes the previous reconciliation fence and does not import existing
static pools into the catalog. All existing source8 plans, preparations and
allocations remain historical accounting. File-oriented and live-store exports
validate canonical route records in both directions against permanent
registration audit history, existing pool accounting scope and original
backend-binding sequences; operator archives
and restores stay runtime-disabled. An inactive catalog cannot be activated by
editing its mode, and SQL guards deny new reservation/dispatch insertion for it.

This storage checkpoint does not install a management-journal adapter, dynamic
Facade resolver, current-ACTIVE claim protocol, activation API or UI. The remaining
steps above are prerequisites, not functionality provided by schema migration.

Original registration budgets are historical identity. Current ledger budget
policy may narrow or otherwise change through the separately authorized policy
path without rewriting that identity; normal admission/accounting guards still
enforce current budgets. A policy edit never activates an inactive route.

The isolated request-local resolver follow-on shares the complete canonical graph
reader with archive validation. It returns original inactive identity plus current
accounting policy/revision from one read-only SQLite snapshot. Private reference
absence or drift is reported separately without discarding historical accounting
or replacing a plan. A current reference match is not activation permission. It
makes no remote request, performs no write, and is not wired to Facade or admin
controls until transactional ACTIVE identity/revision guards are implemented.
