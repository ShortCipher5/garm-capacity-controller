# Managed read provenance

The private supervisor now projects provider/repository-scoped pool and job
collections, exact configured pool ownership, and the existing provider-scoped
instance inventory. It does not forward stock credentials or arbitrary API
requests. Unknown provider-owned instance rows remain visible for conservative
accounting. Pool visibility never enrolls that pool.

`garmapi.NewManagedDemand(endpoint, serverKeyPin, expectedControllerID, trust,
providers, repositories)` uses normal CA/name validation, an independently
configured server SPKI pin and client TLS credentials. Its source identity is
empty until an actual successful response supplies the supervisor's authenticated
stock controller identity and bootstrap-bound GARM source identity. Changes to
that source permanently revoke this client instance. Each request is read-only,
loopback, exact-path and strips bearer authorization. No identity is minted by
configuration, a caller-supplied digest, or deserialization.

Managed pool ownership observations use that identical transport identity.
Registration checks the actual observed controller against the requested provider
authority. CLI `register-pool-profile` supports `managed_server_key_pin`,
`managed_controller_id`, `client_certificate_file`, `client_key_file` alongside
`ca_file`; `token_file` is forbidden in this mode. Direct stock-token mode is
unchanged. Private console recipes accept the same managed transport fields. Browser users still select only a server-owned recipe and saved size. Recipe review revisions bind the endpoint, proxy key pin, controller ID, CA and public client certificate identity; a restart with changed trust requires a fresh review. No private key is hashed or exposed.

These reads remain non-atomic; repeated pool snapshots detect some drift but do
not prove webhook completeness. TLS supervisor provenance is not native capacity
or exclusive physical authority. This change does not enable a scheduler,
runner creation, maximum increases, or retirement release. Deployment of the
root supervisor/Docker namespace boundary remains separately unqualified.
