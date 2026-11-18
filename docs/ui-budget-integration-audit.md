# Existing UI integration audit

This audit checks connections, not production qualification.

- Servers already contains approved SSH target registration, host-key review,
  read-only OS inventory, prepared bootstrap/firstboot/rotation controls, and
  authenticated daemon discovery. These are separate capabilities; a successful
  OS probe is not enrollment or authorization to provision.
- The server enrollment planner already uses discovered totals, conservative
  existing guest allowances, system headroom, disk usage and selected storage
  pool limits. It presents saved runner sizes (or clearly illustrative presets),
  fit estimates and editable runner mixes without provisioning them.
- Runner-size CRUD and immutable pool mapping registration already exist on the
  current Profiles/Pools sections. Later size edits do not mutate captured pool
  mapping resources.
- Repository policy already stores equal default weight 1, custom weights,
  repository/pool caps and per-pool warm-idle targets with revision checks.
  Saving policy alone does not activate the runtime or change Garm settings.
- Allocation cards, charged-state breakdown and allocator process status are
  distinct. Process cycle success is not proof a runner started. A coherent
  per-pool policy/charge comparison is still a separate follow-up, not claimed
  by this change.
- Existing Garm reads already refresh while visible and preserve draft conflicts.
  PR #155 adds notices for clean-read and unmanaged-pool drift. Neither adopts
  existing pools or implements safe concurrent direct UI/CLI writes.

## Confirmed defect fixed here

The ledger policy form built a replacement budget from only CPU, RAM and runner
count. The API's budget is a full four-dimension replacement, so omitting disk
could turn an existing disk ceiling into zero (unconstrained). The form now
requires, preserves and reviews disk explicitly for global, host and pool edits.
The resource type contains exactly these four dimensions. The optional separate
CPU-cap pointer is intentionally omitted: the backend preserves that value when
absent, and existing validation still applies.

Disk now appears in overview, server and pool allocation displays. Zero is
labelled no ceiling, not free space or zero capacity. Missing display values are
unknown, never invented zero. Existing-host edits now show last-detected
system-buffer warnings and retained over-budget charges in the current form and
review. These warnings are advisory; they neither weaken ledger constraints nor
claim current free resources. Cancellation clears the preview. Existing revision
checks and separate write authorization remain unchanged.
