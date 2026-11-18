# Append-only native target catalog history

Existing target registration, resource planning, assembly, preparation and asset
limits remain unchanged: at most eight consumed server registrations, including
unknown outcomes. This does not add capacity, credentials, installation, runtime
activation, or any release/dispatch authority.

The configured catalog may now add templates, identity slots and administrator
aliases without invalidating an older registered target. Every previously
checkpointed alias must remain byte-for-byte equivalent: template configuration
and private source commitments, identity key/certificate commitments and paths,
administrator destination scope/key fingerprint, and private/output/journal
parents. New aliases are additions, not updates, key rotations or replacements.
All original source inputs remain required. No private file bytes are stored in
catalog history; configuration references and hashes are still protected data and
must not be published or returned to browsers.

## Durable history and migration

The existing protected registration directory holds a sequential hash-linked
`catalog-NNN.json` history under the same cross-process registration lock.
Snapshots preserve the original catalog revision hashing grammar. Each historical
intent is recompiled against its exact earlier snapshot using unchanged frozen
current source inputs, and its original complete metadata digest must match.
Old intent/completion files, materialized paths, bundle references and review
identifiers are never rewritten. Reservations remain global across revisions:
preparation ID, inventory target, host label, identity alias and both owner SPKIs.
Unknown registrations retain all those reservations permanently.

On the next registration, the exact current catalog is checkpointed before the
new intent or materialization. Explicit startup `AnchorCatalog` also checkpoints each validated additive revision.
For a legacy baseline it does so only when the still-configured original catalog validates every legacy intent and all
completed materialized outputs. Therefore upgrade/restart once with unchanged
original catalog inputs before adding entries to a populated old journal. An
addition-first migration without the original snapshot fails closed; it cannot
infer forgotten unused entries. Constructors, configuration-only checks and GET history/`Restore` remain read-only;
only explicit startup anchoring or registration writes a catalog snapshot. No journal deletion/reset/adoption procedure is provided.

History permits at most 16 catalog snapshots, each at most 2 MiB, while the eight
consumed-registration limit stays unchanged. A failed snapshot/intent boundary
may conservatively consume a history position. Missing, malformed, reordered or
changed referenced snapshots fail closed. A stale process using a smaller or
sibling catalog cannot register against a newer committed catalog head.
Trusted-administrator whole-state rollback remains outside the local journal
model, just as for the existing registration history; this is not anti-rollback
hardware or an independent authority.

This improves continuity while preapproved catalog choices evolve. It does not
provide unlimited fleet growth, free old slots, rotate used keys, replace a host,
provision TLS identities, or make the guarded runtime operational.
