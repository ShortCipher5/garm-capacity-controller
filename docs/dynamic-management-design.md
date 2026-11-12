# Dynamic host and pool management: design gaps and acceptance gates

**Design only. These operations are not implemented or enabled.** Existing host
mode and budget edits are ledger policy changes, not server enrollment, dynamic
runner placement, physical-capacity qualification, drain or legacy-scope import.

## Current invariants to preserve

- The gateway pins one trusted `PoolPolicy` (host, resources, image and profiles)
  at startup. Editing a ledger pool's host allowlist does not move that selection.
  Do not expose an allowlist editor as a working placement control.
- A reservation/retry identity must retain its originally admitted resources and
  execution plan. A later pool edit must not reinterpret an existing reservation.
- All declared budget reads, reservation changes and audit writes share one
  authoritative SQLite admission transaction. Unknown/dispatched resources stay
  charged. A missing row, timeout, canceled browser request or restart proves
  neither absence nor safe release.
- Backend identity bindings are authenticated **daemon observations**, not
  physical-hardware attestation, whole-host inventory or parent-quota proof.
  Nested/aliased hosts can still require separately qualified physical scope.
- Legacy charged hosts retain an immutable historical exclusion. Later
  cancellation or zero current charges does not authorize automatic binding.
  Qualified legacy scope import is a separate, unavailable workflow.
- Pausing new claims is not job-safe drain and does not retroactively fence an
  already committed in-flight claim or existing idle runner assignment.

## A bounded future activation model

1. **Trusted enrollment, outside job/browser payloads.** An operator enrolls a
   transport/credential reference through a reviewed secret-loading path. Browser
   requests may reference enrolled IDs, not arbitrary URLs, host paths, TLS
   credentials, projects or provider specs. No such enrollment API exists today.
2. **Fresh observation before activation.** The gateway collects authenticated
   daemon/project identity under a fresh reconciliation barrier, checks duplicate
   aliases and separately qualified capacity scope. A binding observation alone
   cannot satisfy physical-host or parent-quota acceptance gates.
3. **Versioned desired policy.** A future immutable policy version would reference
   enrolled hosts, binding sequences and fully validated fixed execution plans.
   Activation must compare the operator's expected revision inside the same
   durable writer transaction, close readiness and invalidate the old collector.
4. **Admission pins an immutable execution plan.** New reservations would atomically
   persist selected host/binding sequence, resources, image/profile plan and policy
   version. Existing reservations and retries keep their original plan even after
   placement policy changes. Do not hot-mutate the current facade maps.
5. **Dispatch revalidates identity, not mutable defaults.** It uses the reserved
   plan and its binding, never a newly selected host or newly edited resource
   profile. Failures and ambiguous outcomes remain charged and unreplayed.
6. **Fresh complete reconciliation before reopening.** Old collector observations
   cannot reopen a newly activated policy. Out-of-scope/unbounded inventory,
   unresolved resources, legacy exclusions or unqualified backend scope block
   activation/admission rather than trigger a hidden reset.

A deterministic atomic host chooser would be a separate implementation and test
contract. Neither a host list nor a Garm priority field proves fair scheduling,
starvation bounds, measured utilization or physical isolation.

## Garm and ledger policy are not one transaction

Pinned Garm v0.2.1 has no atomic conditional settings update contract. Local
fingerprint preflight plus readback cannot exclude native Garm UI/CLI writers.
Any future routing/create workflow needs externally qualified writer exclusion,
immutable remote intents and failure recovery, not a read-then-write CAS claim.

A safe future staged pool workflow would have to keep demand disabled while its
provider/ledger route and complete affected repository graph are qualified, then
activate under the admission barrier. A repository balancer can select pools
outside a console allowlist. No blanket capacity promise applies to unqualified
routes. Generation-changing edits, deletion and idle-target decreases must remain
unavailable until assignment-safe lifecycle/retirement is separately qualified.

There is no safe generic reset button for uncertain remote mutations or lost
history. A later matching GET cannot fence a delayed accepted PUT. An independent
journal lineage can reject fresh/wrong state, but does not prove that a historical
same-lineage raw copy has not been substituted. Backup/restore and external
request-completion/history fences require their own acceptance gates.

## Integration acceptance matrix for future implementation

These are required future tests, not green/skipped placeholders claiming a feature:

- Race policy activation against two admissions: each reservation uses one full
  old/new policy, never a mix of budgets, host and image/profile plan.
- Replay an old reservation after placement/resources change: retain original
  plan, charge and identity; never dispatch an altered create as a retry.
- Activate while a collector is in flight: its old fence cannot reopen readiness.
- Alias the same daemon under another name/project/certificate and exercise
  nested-parent quota limits: reject or remain blocked until scope is qualified.
- Attempt legacy binding after cancellation empties current charges: historical
  exclusion remains; no budget/mode edit or UI route imports it.
- Change endpoint/certificate after a permit but before creation: pin/recheck the
  admitted identity and preserve charge/unknown outcome on mismatch.
- Interrupt each stage before/after durable intent, remote acceptance and local
  outcome recording; restart against missing, empty, wrong and historical state.
  Never infer no effect from client cancellation or authoritative GET alone.
- Race another Garm writer between preflight and PUT: preserve the explicit lack
  of upstream CAS and conservatively stop on ambiguity/protected-field drift.
- Prove complete installed repository-to-pool-to-provider routing before any
  enabled operation is advertised as ledger-enforced.
- Exercise UI review/cancel, repeated submits, stale responses, disconnect and
  browser history; there must be no hidden enrollment/import/reset action.

Current negative API tests assert that enrollment, dynamic placement and legacy
scope-import routes are unavailable and leave ledger policy/audit unchanged.
