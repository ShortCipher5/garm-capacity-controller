# Protected stock-provider bridge (dormant)

The native bridge uses the unchanged stock GARM provider protocol. It does not
invent original-preparation evidence and does not modify the prepared gateway.

## Implemented sequence

1. Match the authenticated provider leaf's exact controller/pool/provider scope
   to the saved immutable pool profile and its enrolled native domains.
2. Read the actual pending stock instance and owning pool over authenticated
   management TLS. Require exact instance UUID/name, pool, provider and agent ID;
   already-provisioned, nonpending and scale-set instances are rejected.
3. Fetch the three existing JIT credential files from the separately configured
   stock metadata origin using the supplied instance token. Preserve bytes and
   reject unsupported runner settings rather than rewriting them. Re-read runner
   settings and stock instance identity to detect changes. These reads are not a
   remote transaction or a lock.
4. Build canonical protected bootstrap JSON using only the frozen native policy's
   image descriptor. Opaque source proof retains both actual metadata registration
   and stock instance observations. Formatting is redacted and JSON serialization
   of the proof is refused; credentials are accessed explicitly in memory only.
5. Redeem an existing matching canonical broker grant. Execute through the native
   coordinator: stopped CREATE, accepted/terminal observation, quota, protected
   file delivery/readback, then START and its accepted/terminal observation.

Every uncertain mutation remains charged. A repeated Create is never forwarded
again; even an earlier confirmed record is not presented as proof that an old
instance is currently running. Native deletion/release is not supplied here.

## Selection and boundaries

`native_stock` is optional server configuration, keyed by pool UUID. Each entry
contains `metadata_url`, `github_url` and a `hosts` map. Host entries specify an
existing `domain_binding_sequence`, immutable protected `policy`, `owner_endpoint`,
`owner_tls` files and the enrolled `owner_client_leaf` / `owner_server_leaf`.
Management ownership reads use the configured GARM endpoint and management token,
never a bootstrap-supplied management URL or credential. Native pools must cover
all configured stock scopes; no failure falls back to the direct-Incus creator.

The startup builder and concrete facade **refuse runtime-lineage stores**. Flags,
configuration, enrollment records and passing tests cannot lift this gate. The
existing prepared gateway is unchanged. The bridge is create-only and does not
establish all-provider exclusivity, a sole management writer, protected native
startup authority, job-safe retirement, physical release, or arbitrary raw-image
rollback protection. Those boundaries must be independently implemented and
qualified before production activation.

Tests exercise actual loopback TLS, opaque source observations and canonical
SQLite admission/claims with a simulated owner. They are not live host qualification.

## Pinned stock API basis

The adapter was checked against stock GARM commit
`154638445c3949c1958b01812f69d9a1e4d82684`, not a modified controller tree:

- `apiserver/routers/routers.go:328–329` and
  `apiserver/controllers/instances.go:148–163` expose the instance **name** route.
- `runner/runner.go:925–934` requires the management credential and reads that
  instance; `database/sql/instances.go:161–167` preloads its pool, while
  `database/sql/util.go:92–97` supplies the provider and pool identity.
- `params/params.go:290–336` defines the exact JSON identity/status fields.
- `runner/pool/pool.go:973–992` saves pending state and the GitHub JIT agent ID;
  `1741–1757` sets `creating` before provisioning; `1131` invokes the provider.
- The existing stock metadata credential-file endpoints return the original
  stored JIT files. This bridge does not generate or rewrite those credentials.

Protected JIT must already specify an ephemeral runner, disabled self-updates and
`_work` as work folder. Unsupported source settings fail closed; the bridge does
not forge flags or claim an upstream atomic snapshot.
