# Managed provider path inspection (not activation)

`capacity-managed-garm inspect-provider` makes a bounded, read-only observation
of the running managed stock GARM process, its provider configuration and approved
executable bytes, authenticated actual stock pools, and enrolled ledger profiles.
It does **not** enable allocation, increase pool maxima, deploy software, install
credentials, or establish native execution authority.

## Actual profile source across the process ownership boundary

The root supervisor does not open the gateway's UID-owned SQLite database. The
gateway optionally exposes `GET /v1/provider-path-profiles` on its existing TLS
listener, enabled by a separate `provider_inspection_fingerprint` client leaf pin.
The role cannot alias configured provider, source, stock-provider or server leaf
identities. The inspector's key must also differ from the gateway server key,
stock API server key and inspected provider keys. There is no bearer fallback.

The supervisor uses protected root-owned TLS files, a pinned server public key,
TLS 1.3, and a freshly observed network namespace to reach only loopback port
9443. Every read has a fresh 256-bit nonce. Responses bind the actual controller,
ledger lineage fingerprint, transactional sequence and canonical profile payload
hash. Decode rejects aliases, duplicates, oversized data, responses older than
five seconds, and nonce/controller mismatches. Subsequent sequence rollback,
lineage change or profile-payload change permanently revokes that reader. A new
explicit inspection session is required after legitimate enrollment changes.
The endpoint only reads the existing coherent ledger snapshot; it does not alter
ledger ownership, allocate grants or return credential material.

## What is inspected

The observer checks actual managed topology before and after the read, then:

- PID/start identity, executable inode, exact command line, UID/GID, capabilities
  and no-new-privileges state;
- approved SHA-256 bytes of `/usr/bin/garm` and
  `/usr/local/bin/capacity-provider`, with bounded reads and file identity checks;
- exact generated stock configuration, with only the supported external provider
  sections, interface `v0.1.1`, JIT enabled and no forwarded environment prefixes;
- canonical provider JSON and fixed credential filenames, distinct maintenance
  and stock keypairs, and stock leaf identity matching the enrolled profile;
- the actual authenticated full stock pool collection, rejecting missing or
  extra pools, changed immutable ownership facts, or stale host bindings.

Current pool-scoped stock credentials require one external provider configuration
per enrolled pool. A shared provider name across several pools is refused, not
silently interpreted as authority for all pools. Paths, symlinks, file ownership,
permissions, sizes and hardlinks are checked; private keys are not emitted.

`RenderManagedProviderConfiguration` and `EncodeManagedProviderCredentials` are
pure configuration builders. They do not write to a host or prove that GARM loaded
the output. No arbitrary existing TOML configuration is adopted.

## Explicit limits and remaining activation requirements

Inspecting a file does not prove that stock GARM's current in-memory configuration
was loaded from those exact bytes. This component therefore returns an opaque
observation, **not a capacity-increase capability**. A controlled startup/loaded
configuration binding, enforceable native owner exclusivity, and safe physical
retirement remain separate prerequisites. Runtime guards remain intact.

The implementation follows stock GARM v0.2.1 commit
`154638445c3949c1958b01812f69d9a1e4d82684` (`config/external.go` and
`runner/providers/v0.1.1/external.go`), not custom GARM patches. Tests cover real
TLS endpoint authentication, canonical snapshot freshness and corruption,
sequence/lineage drift revocation, unchanged ledger sequence, and rooted file
and configuration refusal paths. No real root-managed Docker deployment or
upstream process startup was performed in the development environment.

The current inspector compares enrolled controller/pool/provider/template facts,
not the composite managed-source identity (proxy URL, server pin and actual
supervisor identity). An enrollment from a different origin with identical facts
must not be treated as activation authority. Binding that exact transport origin
is still required before any capacity-increase permit can consume this evidence.

Existing installations remain discovery/read-only inputs. The managed deployment
is an explicit separate topology, not transparent adoption of an arbitrary GARM
server. Direct changes through the GARM UI are incompatible with an enforced
single management writer; detected changes revoke inspection rather than being
silently overwritten. No two-way synchronization is introduced by this slice.

## Coherent accounting extension (protocol v2)

The same authenticated snapshot now includes the saved broker policy/revision,
reconciliation epoch, ledger readiness, global/host/pool budgets, canonical total
allocations, unused-grant resource subtotals and per-pool unused/consumed-unreleased
grant counts. All come from the existing single `ReadBrokerGrantState` transaction
and carry its audit sequence. No second ledger query or caller maxima are used.

Canonical allocated totals already include unused grants, unreleased physical
reservations, unknown outcomes and inventory charges. The unused subtotal must
not be added again. Expiry timestamps alone do not subtract unused charges;
consumed grants are counted through their unreleased reservations. Counts are
not idle-runner counts, eligibility, physical completion or permission to create.
Over-budget retained allocations remain visible rather than being clamped.

Dynamic accounting has a separate digest from immutable profile identity, so a
normal grant consumption does not falsely revoke an unchanged profile mapping.
Both digests, sequence, nonce and freshness are checked under the authenticated
transport. Version 1 responses are refused by version 2 readers rather than
being interpreted as zero budgets. The extension does not enable a writer or
remove runtime authority guards.

The v2 profile digest also binds the exact immutable native-domain enrollment
records referenced by the saved profiles, including the two distinct owner
certificate leaf fingerprints. These records are read in the same ledger
transaction as the profiles and accounting. Missing, extra or mismatched domain
records are refused. This is enrollment history, not a fresh native enforcement
observation: stale host bindings remain visible rather than being promoted into
current authority.
