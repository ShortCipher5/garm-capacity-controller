# Saved targets versus recorded charges

The existing repository policy GET now reads saved policy and charged resources
from the same `ReadBrokerGrantState` transaction. Its bounded public projection
includes sequence, policy revision, ledger readiness, saved pool cap/warm target,
configured pool budget, canonical pool charge and mapping-record status.
It exposes no source identity, certificate, execution profile or native secret.

The existing policy panel shows this comparison without a new page or write
control. Draft edits do not change its saved targets. Failed reloads and attempted
saves mark the comparison stale until successful reload; this cannot clear any
write uncertainty or grant permission. Legacy servers without this projection
show unavailable, never invented zeros.

All ledger pools remain visible even without saved policy. Policy pools absent
from the ledger have unknown budget/charge, not zero. Recorded mapping is not
current enrollment qualification; mismatched saved repository is explicit.
Canonical charges include unused grants, creating and unknown resources. Lowered
caps do not erase over-budget charges. These are NOT running/idle counts, usable
grants, native readiness, or a live demand-derived desired allocation. Those
require actual authenticated observations and the separately qualified runtime.
The UI no longer claims saved policy has no planner connection: it is an input to
the implemented planner, not an activation switch.
