# Managed inventory observation

In an isolated non-provisioning configuration, the explicit
`managed_inventory_observation: true` gateway setting enables a read-only
inventory probe through the existing managed mTLS client. It is disabled by
default and refused when incompatible provisioning, management, or stock
provider writes are configured. It neither starts a writer nor bypasses
physical reconciliation.

The authenticated admin overview omits the field when disabled. When enabled,
`managed_inventory_observation` reports `available:false` without a current
successful sample; on success it also includes the read start time
(`sampled_at`) and scoped `count` (including zero). The probe reads serially
with a five-second deadline and waits fifteen seconds between reads. Samples
expire after thirty seconds; failed or canceled observations never make a
previous result current. No runner rows, identities or credentials are exposed.
The observation cannot change ledger readiness or prove that physical capacity
is available.
