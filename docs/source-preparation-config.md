# Source-bound preparation configuration (experimental)

This is a **configuration contract**, not an installation/deployment approval.
Use only with the exact reviewed v0.2.1 [source patch](../integration/garm-v0.2.1/README.md)
and controller versions qualified together by the mandatory source gate. Stock
Garm and the stock Incus provider cannot implement this protocol. Real issuers,
installed images, physical limits, alternate writers and job-safe lifecycle remain
unqualified even when the disposable integration tests pass.

## Separate capabilities

- The gateway's ordinary `provider_fingerprints` authorize Admit/Create/Cancel/read.
- `source_fingerprints` is a **different leaf allowlist** for Record/Validate only.
- Each `preparation_pools` entry additionally names exactly which source leaves may
  attest that pool's configured provider/entity/forge/generation route.
- Bearer/admin credentials, CA membership, an ordinary provider leaf, a globally
  allowed source leaf for another pool, or caller-provided authority fields cannot
  grant this capability. The same leaf in both global roles is rejected.

The bridge's Garm OS account can read both credential sets, so this does not create
process-compromise isolation or cryptographic GitHub attestation. Protect its keys
and trusted configuration independently; do not put them in source or frontend
build variables. Changing the route's whole source allowlist changes its scope and
invalidates old receipts, not transparent post-hoc credential rotation.

## Gateway additions

These are partial JSON fragments with placeholders, not runnable configuration.
Retain the existing private TLS, Garm reader, ledger lineage, host and pool policy.
The pool key must be the actual registered Garm pool UUID and the configured pool
policy must independently pin its image/flavor/architecture and resource bounds.

```json
{
  "bootstrap_mode": "gateway-owned-default-cloudinit/v1",
  "provider_fingerprints": ["<ordinary-leaf-sha256-lowercase>"],
  "source_fingerprints": ["<source-leaf-sha256-lowercase>"],
  "preparation_pools": {
    "<actual-garm-pool-uuid>": {
      "provider_name": "capacity-gateway",
      "entity_type": "repository",
      "entity_id": "<actual-garm-repository-uuid>",
      "forge_endpoint_name": "github",
      "generation": 0,
      "jit_enabled": true,
      "source_fingerprints": ["<source-leaf-sha256-lowercase>"]
    }
  }
}
```

`generation:0` is valid but must be the exact actual pool generation, not a default
or freshness assertion. Forge endpoint **name** is the real bounded Garm alias,
not an invented UUID. All active pools need an explicitly configured route.

## External wrapper additions

Its private JSON keeps the ordinary `tls` files and adds separately protected
source files and the exact bootstrap mode:

```json
{
  "gateway": "https://127.0.0.1:9443",
  "controller": "example-controller",
  "tls": {
    "ca": "/run/credentials/provider-ca.pem",
    "certificate": "/run/credentials/provider.pem",
    "key": "/run/credentials/provider.key"
  },
  "source_tls": {
    "ca": "/run/credentials/source-ca.pem",
    "certificate": "/run/credentials/source.pem",
    "key": "/run/credentials/source.key"
  },
  "bootstrap_mode": "gateway-owned-default-cloudinit/v1"
}
```

Record/Validate load only `source_tls`; all ordinary provider commands use `tls`.
Use verified mTLS/explicit leaf grants and private owner-only credential files.
These example paths must be adapted, never populated or committed with real keys.

The patched Garm external-provider stanza additionally requires:

```toml
interface_version = "v0.1.1"
admission_enabled = true
capacity_bootstrap_mode = "gateway-owned-default-cloudinit/v1"
```

This is a partial **external provider** section, not an entire Garm TOML file.
No caller JSON or provider-returned boolean can enable that trusted capability.

## Supported pool and failure behavior

Only normal Linux pools with canonical amd64/arm64, explicit public API
`template_id:0` (mapped to SQL NULL), shell disabled and empty extra specs are
supported by this fixed default-cloud-init contract. Custom templates/scripts or
options are rejected before admission/JIT, never silently discarded. The patched
public repo/org/enterprise API grants explicit zero only to this opted-in trusted
provider. Legacy/default/nonzero behavior for other providers is preserved.
Scalesets and stock delete-before-retry repair remain blocked.

Current activation, route/generation, image/flavor/OS/architecture and fixed
bootstrap policy are checked before reservation and again at later boundaries.
Known drift defers without new JIT or charge. This is not an upstream CAS; change
after preflight can still conservatively hold a reservation/registration. Never
repair it by guessing a new generation, deleting history or retrying another host.
Disabled original pending rows remain charged; disabling is not drain or revocation
of a previously delivered native callback. Confirmed read-only replay is not a new
create. Missing/unknown receipts cannot publish a Garm row or grant physical work.

Every pre-v8 reservation is excluded from retroactive source-proof issuance and
remains charged. The authenticated dashboard reports the missing source history;
there is no reset/import/activate button. Supported backups preserve receipt,
plan, binding, intent and permanent cancellation history, and cannot be started
as live scheduling ledgers by this service.
