# Stock provider-origin accounting

This is a distinct admission-ledger provenance contract for stock GARM provider
`CreateInstance` calls. It does not manufacture original-preparation receipts or
activate dynamic pool routes. The original prepared runtime path still requires
its original source receipt, row observation, immutable plan and host binding.

## Trusted ingress contract

Only a credential-owning server may construct `StockProviderAuthority`, after
verifying mTLS and an exact client-leaf/controller/pool/provider allowlist. It is
not a request body or an operator assertion. The server resolves the execution
image, resource charge and permitted host set from its own configuration.

`commandDigest` identifies a canonical versioned representation of stable,
nonsecret provider command fields. Do not include bootstrap tokens, cloud-init,
instance JWTs, tools credentials or callback query strings. A renewed secret is
not a new instance identity. The transport owns normalization and rejection of
unsupported command fields.

## Atomic boundaries

`AdmitStockProviderPlacement` records the immutable stock origin, execution plan,
reservation, selected host binding and audit in the existing capacity transaction.
Stock and prepared allocations share global, host and pool budgets. There is no
parallel grant balance or native-operation journal whose charges are invisible to
ordinary admission.

`ExecuteStockProviderDispatch` verifies that exact origin, digest and plan before
checking for a previous dispatch. It uses the canonical one-shot dispatch intent,
so `CancelUndispatched` cannot release an allocation after that intent commits.
Concurrent claims across database handles allow only one callback. Legacy/prepared
reservations cannot be adopted, and stock reservations cannot use the old dispatch
entry points. Replays do not update image/profile defaults or change host.

Callback errors, cancellation, panics and invalid/uncertain outcomes retain the
original charge and prohibit resubmission. A restart does not clear an intent.
An exact confirmed response can be read; it is not authority to create again.
Certificate rotation does not silently rewrite an existing origin.

Schema 16 preserves all prior charges and closes readiness for fresh reconciliation.
SQLite backups retain stock provenance and validate it against plans, audit and
canonical dispatch references. Restoration does not activate execution. Missing
stock-origin history cannot be reinterpreted as legacy/prepared provenance.

## Deliberately not supplied

This ledger contract is not native sole-writer enforcement, a clean-domain proof,
a host agent, job-safe draining, retirement finalization or deployment approval.
The callback's trusted implementation must independently enforce immutable native
placement and exclusive host authority. Existing retirement remains blocked; this
change adds no physical deletion or capacity-release path. Disposable fake-backend
checks are not qualification of live GARM, Incus, hosts or running jobs.
