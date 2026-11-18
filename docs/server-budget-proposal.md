# Versioned server-side host budget proposal

Discovery now contains `proposal.version=1`, optional known `budget` and
`reserve` dimensions, warnings, runner sizes and their source. The complete
proposal is included in the durable review digest before enrollment. UI uses
that server proposal rather than recalculating accepted discovery data locally.
Older views without a proposal retain the visibly advisory compatibility helper.
Unsupported or malformed proposals are rejected, never silently reinterpreted.

Algorithm v1 reserves 10% of each total, at least 1 vCPU, 2 GiB RAM and 10 GiB
disk, capped at that total. It subtracts every guest's configured limits,
including attributed guests. Disk additionally subtracts current used bytes and
caps the result to the selected pool's corresponding allowance. Without a
per-guest used-byte attribution the overlap is intentionally conservative.
Unknown totals, usage, inventory or limits leave the affected dimension absent.
An exhausted disk proposal is absent, never zero/unconstrained. CPU/RAM may be
zero to represent no new capacity. Arithmetic remains within JSON-safe integers.

The proposal includes a point-in-time snapshot of existing saved runner-size
profiles. If none exist, small/medium/large presets are explicitly illustrative.
If profile reads fail or exceed 512 profiles, sizing is explicitly unavailable,
not silently replaced with presets. An unconstrained disk profile has unknown
fit and cannot contribute to a bounded mix. It does not prevent planning with
other explicit sizes. Refresh discovery after changing saved profiles.

Sizing shows independent alternative maxima and the combined chosen mix. The
operator still reviews/edits budgets and submits enrollment explicitly. Mix
counts are not persisted or provisioned. This is sizing integration with saved
profiles, not scheduler/provisioner integration or automatic policy application.
No server connections, credentials, agents, host health, physical authority or
activation are created by this change. It remains a conservative advisory
proposal over an incomplete point-in-time scope, not qualified free capacity.
