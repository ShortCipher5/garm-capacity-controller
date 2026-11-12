# Atomic durable multi-host placement (experimental, schema7)

New reservations can select an enrolled candidate host inside the SAME SQLite
`BEGIN IMMEDIATE` transaction that checks global/pool/host budgets, writes the
reservation, records its immutable execution plan and audits the choice. No
physical request or JIT credential is needed for this choice. This is configured
placement, not dynamic server enrollment, fair scheduling or hardware attestation.

## Trusted startup configuration

A pool uses EITHER its existing `host` shorthand OR a nonempty `hosts` array, not
both. Every candidate must already exist in trusted gateway host configuration,
the ledger's host set and that pool's ledger eligibility list at startup. Unknown,
duplicate, empty or conflicting candidates fail startup. Default strategy is
`first_fit`; `least_runners` is also available.

```json
{
  "hosts": ["enrolled-host-a", "enrolled-host-b"],
  "strategy": "first_fit",
  "cpu_millis": 1000,
  "ram_mib": 1024,
  "flavor": "bounded-small",
  "image": "pinned-linux",
  "image_fingerprint": "REPLACE_WITH_EXACT_64_LOWERCASE_HEX_DIGITS",
  "profiles": ["qualified-runner-profile"],
  "type": "container",
  "arch": "amd64"
}
```

This is an illustrative pool object, not a deployable complete configuration.
There are no credentials, host addresses or live defaults in it. Qualify the exact
pinned image and effective profile/native isolation on EVERY candidate first.
Profile names/order are pinned, NOT the resolved mutable profile/device contents.
External profile administration must be separately fenced and qualified; explicit
instance CPU/RAM limits do not prove disk/device/security isolation.
A missing image or provider error does not authorize switching hosts after intent.

Candidate IDs are sorted for deterministic tie breaking. `first_fit` chooses the
first enabled, healthy, bound, eligible host with room. `least_runners` chooses
the fitting host with the fewest CHARGED runners, then ID. Creating, idle, busy,
failed, deleting and unknown charges all count. This is not measured utilization,
weighted CPU fairness, starvation prevention or an optimal bin-packing claim.
Global freshness and all budgets remain mandatory. A runtime ledger eligibility
edit can restrict this startup set but cannot secretly enroll another destination.
Restart configuration must agree with current eligibility; mismatches fail closed.

## Immutable identity, not ambiguous failover

The durable plan fixes controller/name/pool identity, selected host, its binding
audit sequence, CPU/RAM/one-runner allocation, image fingerprint, profiles, type,
architecture and Garm-side flavor/image identifiers. An exact identity replay
returns the ORIGINAL plan, even if new-request defaults, budgets or eligible
candidates have changed. It never generates another physical permit. Creation
requires that the current trusted startup policy STILL authorizes the exact
original execution settings and selected host. Revocation blocks creation; it
does not rewrite the old plan or move its charge. Reads/reconciliation preserve
historical identity even when new creation is no longer authorized.

Changing a configuration file cannot revoke a still-running old gateway process.
A deployment revocation must also fence old processes, credentials and already
accepted requests; there is no distributed/global policy-revocation claim.

Dispatch compares the exact plan and backend binding inside its one-shot intent
transaction. Changed caller fields fail before the callback. Active runtime
handles refuse the older planless Claim/Execute APIs; those remain trusted
offline simulation helpers only. The gateway renders
its approved default cloud-init from this plan plus the current authenticated
Garm bootstrap. Ephemeral tokens, userdata and credentials are NEVER persisted;
this does not claim identical bootstrap bytes across retries.

A lost POST, wait failure or crash leaves the original host charged with its
original dispatch history. There is NO retry-on-another-host or unknown-create
failover. Reads/listing and reconciliation locate known reservations using their
persisted host, not new defaults; removing eligibility cannot hide an old runner.
Eligibility is not accounting scope: old linked resources and observed orphans
remain charged on enrolled hosts even outside the current candidate list. No
public pool-host eligibility editor or dynamic enrollment API is enabled here.
Orphans are charged to the actually observed host. Unlocated Garm-only rows cannot
invent multi-host placement. Duplicate names and conflicting identity fail closed.

## Migration, history and recovery

Schema7 preserves all prior allocation, cancellation, dispatch, backend-binding,
operation-observation and runtime-lineage history. It closes readiness on upgrade.
It does NOT manufacture execution plans for old reservations. Such charges remain
visible and charged but cannot be reinterpreted for creation. The console exposes
a historical-unplanned count and warning, not a reset/import action. Existing
unproven histories require a separate qualified recovery/import workflow.

Plans remain immutable after never-dispatched cancellation, alongside its
permanent tombstone. Consistent backups and the actual operator CLI preserve
plans and dispatch/binding provenance. Archive/restored artifacts remain permanently
runtime-disabled, even after offline reconciliation. Qualified active schema6 can
migrate to7 without changing its independent runtime identity; old binaries must
not be run against the newer database. Migration does not stop a binary6 process
already holding a connection or revoke an earlier permit. The new runtime API
guard only governs the current binary. Stop and externally fence old writers
before upgrading; never infer a global fence from a schema version.

## Qualification boundaries

This does not solve hidden projects, foreign writers, separate daemons on one
physical machine, native parent quotas, same-lineage historical raw clones,
old-writer revocation, assignment-safe retirement or automatic release. See
[physical scope](physical-scope-qualification.md), [runtime lineage](runtime-ledger-lineage.md)
and [durable bindings](durable-host-bindings.md). No live deployment is implied.

A Garm-only preparation row can race reconciliation before provider creation.
It must not be treated as proven physical presence solely because of its state,
or silently discarded. A separate typed preparation-provenance guard is being
qualified: immutable original plan + no dispatch + exact source metadata + complete
fresh post-barrier physical/operation inventory. Existing ambiguous inventory is
never laundered, removed or released by that proof.
