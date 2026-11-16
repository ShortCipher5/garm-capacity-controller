# Provider-scoped discovery without enrolling pools

The existing operator-only GARM management configuration accepts an explicit
`discovery_only: true` mode. Supply the existing endpoint, dedicated token file,
TLS trust and nonempty provider scope. Leave pool_ids/repository_ids empty and do
not supply a journal, journal lineage, write-routing acknowledgements or enable
experimental GARM writes. Contradictory settings are rejected before access.

This mode creates a read-only client and admin adapter: authenticated existing
GET /admin/api/v1/garm returns optional pool_inventory, no write controls or
journal initialization. Pool discovery never registers, recreates or deletes
pools/runners and never changes capacity accounting. Missing configured pools in
normal management mode may be moved outside provider scope, not necessarily
deleted. Discovery fingerprints are not mutation preconditions.

UI refresh runs every 15 seconds while visible after initial load and on focus.
Dirty drafts are preserved and blocked on settings drift; explicit refresh
remounts fields, and fields are disabled while reads run. Existing write gates
are unchanged. Config changes are operator-only; this work performs no live
configuration, credential transmission, enrollment or deployment.
