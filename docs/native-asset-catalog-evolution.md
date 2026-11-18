# Append-only native asset catalogs

This supports adding a previously unseeded approved asset slot without breaking
older registrations. It does not expand the existing eight-slot/static-target
limit, recycle a consumed identity, activate a host, or resolve runtime authority.

## What stays immutable

Each configured slot commits to the exact manifest and reviewed file inventory,
including the asset ID and helper hash. The original whole-catalog digest format
is unchanged. Protected `asset-catalog-<digest>.json` snapshots retain only slot
names and content commitments; no credentials, file contents or paths are copied.
Every snapshot must form a strictly growing subset chain and every old commitment
must still be present unchanged in the deployment's current catalog. Renaming,
removing, modifying, replacing or reusing any old entry is refused, including
entries that were approved but never used. Duplicate asset IDs remain forbidden.

Service setup anchors the deployment-approved catalog locally before restoring
recipes. A registration also ensures this anchor is durable before creating its
intent. Catalog checks and review constructors remain read-only. Snapshots and
intents use the existing protected exclusive-write/fsync discipline and shared
nonblocking workflow lock. Partial or malformed snapshots fail closed; they are
not overwritten or automatically repaired. Stale processes whose catalog omits
a newer durable version cannot register another target.

Historical recovery uses the original record's catalog digest, verifies that
its slot belonged to that anchored catalog and is unchanged today, reconstructs
the exact old review, and byte-compares both original intent and completion.
Existing record bytes and review digests are never rewritten. All reservations,
including unknown attempts, remain global across versions. A new catalog does
not make an old target or asset ID available again. A new registration still
needs current exact review and the existing separate approval; SSH staging is
still a second action.

## Migrating existing pre-snapshot history

First start the updated service with the **exact original catalog unchanged**.
This can anchor the original commitment even when the only records are unknown
attempts. No attempt is resumed or repaired. After the anchor is safely present,
add the new approved slots to deployment configuration and restart normally.

If additions were made first, the service refuses to infer an old catalog from
just its consumed entries. Those entries do not identify the original unused
slots. Restore the exact original deployment configuration to anchor it first;
never reset/remove intents, completion records, snapshots or unknown state.
The configuration-check command alone does not create the required anchor.

Old asset bytes, helper hashes and exact transfer/assembly bindings still need
to match. This change is not a migration mechanism for changed uploader scripts,
helper binaries, renamed targets, altered SSH pins or replacement assemblies.

Both filesystem history and approved source bundles must be retained. The
snapshots are integrity-checked commitments under the existing trusted local
configuration model, not signatures against an administrator who can rewrite
all local state. No live host access occurs during catalog anchoring.
