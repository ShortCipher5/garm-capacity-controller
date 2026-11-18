# Existing stock Garm: read-side reconciliation

Existing pools do not need to be recreated for discovery. The existing settings
panel polls its scoped read endpoint while visible and on focus. Fresh settings
replace the displayed baseline when no draft is pending. With an edited draft,
settings drift instead preserves the draft and blocks application until explicit
discard/refresh. Pending or unknown journal operations remain independently
blocked; a matching read cannot resolve a delayed write.

The panel now reports the latest detected public-field differences even without
a draft, including existing pools outside the management allowlist. Inventory
unavailability is not converted into an empty collection. An absent pool is
labelled not observed, never deleted. Tags are compared independent of order;
notices are capped at 50 differences with bounded field text. Identical polls do
not erase an unread notice; dismissing a notice only hides that notice and does
not clear a draft conflict or journal block. Failed reads mark retained notices
as historical, not confirmed current state.

This is a browser-session observation, not a durable external-edit audit or a
complete history. It cannot attribute an edit to a person/tool or detect a
change that reverted between polls. It does not adopt pools, enroll hosts,
release charges, reconcile writes automatically, or authorize increases.
Existing Garm UI/CLI writers remain unfenced by stock fingerprint checks.
Managed fresh single-writer provisioning is a separate installation mode, not
an automatic migration of discovered existing pools. No fork is required.
