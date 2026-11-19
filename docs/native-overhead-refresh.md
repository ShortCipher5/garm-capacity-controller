# Refresh retained native overhead

`capacity-ledger refresh-overhead --source LEDGER --identity-file IDENTITY --config PRIVATE_JSON`
performs a bounded, opt-in one-shot collection into an existing runtime ledger.
Run as its existing ledger owner. It does not create or adopt an enrollment.

The private JSON contains `targets`, each with `domain_binding_sequence`,
`endpoint`, `tls` (the existing secure TLS-file configuration), and `kind`:
`domain`, `artifact`, or `both`. No supplied descriptor, resources, quota,
readiness flag or detached evidence is accepted. At most 100 distinct existing
bindings are accepted; each target has a 15-second deadline.

The command reads the exact immutable enrollment and current host binding,
performs a real pinned owner identity exchange, obtains fresh nonce-bound opaque
observations, and invokes the canonical recording transaction. For `both`,
artifact collection precedes domain collection. A failure can therefore leave
an earlier additive charge committed. The output reports committed audit
sequences even on failure; it is not an atomic multi-owner authority snapshot.
Keep all charges and reread the ledger before planning. Repeating a pass fetches
new evidence; lower ceilings never release permanent debt.

Missing enrollment, unavailable owner, drift, invalid TLS, expired observations
or incomplete native enforcement fail closed. This command never issues grants,
changes GARM settings, lowers charges, reconciles inventory, releases resources,
starts a daemon or enables runtime execution. Scheduling repeated invocations
is an operator deployment decision, not an automatic effect of this command.
